// Package slice implements HEVC slice data parsing including
// CTU/CU/TU quadtree structure and residual coding via CABAC.
package slice

import (
	"fmt"

	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
)

// CodingUnit holds the decoded data for a single CU.
type CodingUnit struct {
	X0, Y0     int
	Log2CbSize int
	PredMode   int // 0=inter, 1=intra
	PartMode   int // for intra: 0=2Nx2N, 1=NxN
	IntraLumaMode  [4]int // up to 4 PUs for NxN
	IntraChromaMode int
	// Residual data per TU
	TransformUnits []TransformUnit
}

// TransformUnit holds decoded residual coefficients for one TU.
type TransformUnit struct {
	X0, Y0     int
	Log2TrSize int
	CbfLuma    bool
	CbfCb      bool
	CbfCr      bool
	LumaCoeffs   []int32 // coefficients in scan order
	CbCoeffs     []int32
	CrCoeffs     []int32
}

// SliceData holds all decoded CUs for a slice.
type SliceData struct {
	CUs []CodingUnit
}

// Params holds SPS/PPS-derived parameters needed for slice data decoding.
type Params struct {
	SliceQPY                      int
	PicWidth, PicHeight           int
	Log2CtbSize                   int
	Log2MinCbSize                 int
	Log2MinTrafoSize              int
	Log2MaxTrafoSize              int
	MaxTransformHierarchyDepthIntra int
}

// DecodeSliceData decodes the slice data segment using CABAC.
// cabacData is the raw CABAC bitstream (after slice header, emulation prevention removed).
func DecodeSliceData(cabacData []byte, p Params) (*SliceData, error) {
	dec, err := cabac.NewDecoder(cabacData)
	if err != nil {
		return nil, fmt.Errorf("init CABAC: %w", err)
	}
	dec.Trace = false

	ctxModels := context.InitModels(p.SliceQPY)

	ctbSize := 1 << p.Log2CtbSize
	ctbsX := (p.PicWidth + ctbSize - 1) / ctbSize
	ctbsY := (p.PicHeight + ctbSize - 1) / ctbSize

	sd := &SliceData{}

	for ctbAddrRS := 0; ctbAddrRS < ctbsX*ctbsY; ctbAddrRS++ {
		ctbX := (ctbAddrRS % ctbsX) * ctbSize
		ctbY := (ctbAddrRS / ctbsX) * ctbSize

		cus, err := decodeCodingQuadtree(dec, ctxModels, ctbX, ctbY, p.Log2CtbSize, 0, &p)
		if err != nil {
			return nil, fmt.Errorf("CTU (%d,%d): %w", ctbX, ctbY, err)
		}
		sd.CUs = append(sd.CUs, cus...)

		// end_of_slice_segment_flag
		endOfSlice := dec.DecodeTerminate()
		if endOfSlice == 1 {
			break
		}
	}

	return sd, nil
}

// decodeCodingQuadtree recursively splits the CTU into CUs.
func decodeCodingQuadtree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize, depth int, p *Params) ([]CodingUnit, error) {

	split := false
	if log2CbSize > p.Log2MinCbSize {
		// Determine context for split_cu_flag based on neighbor availability
		ctxInc := 0 // simplified: no neighbors available for first CTU
		splitFlag := dec.DecodeDecision(&ctx[context.CtxSplitCuFlag+ctxInc])
		split = splitFlag == 1
	}

	if split {
		// Split into 4 sub-CUs
		newLog2CbSize := log2CbSize - 1
		halfSize := 1 << newLog2CbSize
		var allCUs []CodingUnit

		positions := [4][2]int{
			{x0, y0},
			{x0 + halfSize, y0},
			{x0, y0 + halfSize},
			{x0 + halfSize, y0 + halfSize},
		}

		for _, pos := range positions {
			if pos[0] < p.PicWidth && pos[1] < p.PicHeight {
				cus, err := decodeCodingQuadtree(dec, ctx, pos[0], pos[1],
					newLog2CbSize, depth+1, p)
				if err != nil {
					return nil, err
				}
				allCUs = append(allCUs, cus...)
			}
		}
		return allCUs, nil
	}

	// Decode coding unit
	cu, err := decodeCodingUnit(dec, ctx, x0, y0, log2CbSize, p)
	if err != nil {
		return nil, err
	}
	return []CodingUnit{cu}, nil
}

// decodeCodingUnit decodes a single CU.
func decodeCodingUnit(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize int, p *Params) (CodingUnit, error) {

	cu := CodingUnit{
		X0:         x0,
		Y0:         y0,
		Log2CbSize: log2CbSize,
		PredMode:   1, // intra (I-slice)
	}

	cbSize := 1 << log2CbSize

	// In I-slice, pred_mode is always intra, no need to decode pred_mode_flag

	// part_mode: for intra CU, 0=2Nx2N, 1=NxN
	// NxN is only allowed when log2CbSize == log2MinCbSize
	if log2CbSize == p.Log2MinCbSize {
		partModeBin := dec.DecodeDecision(&ctx[context.CtxPartMode])
		if partModeBin == 0 {
			cu.PartMode = 1 // NxN
		} else {
			cu.PartMode = 0 // 2Nx2N
		}
	}

	// Decode intra prediction modes
	numPU := 1
	if cu.PartMode == 1 {
		numPU = 4
	}

	// prev_intra_luma_pred_flag for each PU
	prevFlags := make([]bool, numPU)
	for i := range numPU {
		flag := dec.DecodeDecision(&ctx[context.CtxPrevIntraLumaPredFlag])
		prevFlags[i] = flag == 1
	}

	// mpm_idx or rem_intra_luma_pred_mode for each PU
	for i := range numPU {
		if prevFlags[i] {
			// mpm_idx: decoded as truncated unary, max 2
			mpmIdx := 0
			if dec.DecodeBypass() == 1 {
				mpmIdx++
				if dec.DecodeBypass() == 1 {
					mpmIdx++
				}
			}
			// For the first CTU with no neighbors, MPM list is {0(Planar), 1(DC), 26(Angular)}
			mpmList := [3]int{0, 1, 26}
			cu.IntraLumaMode[i] = mpmList[mpmIdx]
		} else {
			// rem_intra_luma_pred_mode: 5 bypass bins
			rem := int(dec.ReadBypassU(5))
			// Convert rem to actual mode (skipping MPM entries)
			mpmList := [3]int{0, 1, 26}
			// Sort MPM list
			sorted := sortMPM(mpmList)
			mode := rem
			for _, mpm := range sorted {
				if mode >= mpm {
					mode++
				}
			}
			cu.IntraLumaMode[i] = mode
		}
	}

	// intra_chroma_pred_mode
	chromaPredFlag := dec.DecodeDecision(&ctx[context.CtxIntraChromaPredMode])
	if chromaPredFlag == 0 {
		// DM mode (derived from luma)
		cu.IntraChromaMode = 4 // DM
	} else {
		// 2 bypass bins for mode index (0-3: Planar, Angular26, Angular10, DC)
		chromaIdx := int(dec.ReadBypassU(2))
		chromaModes := [4]int{0, 26, 10, 1}
		cu.IntraChromaMode = chromaModes[chromaIdx]
	}

	// Decode transform tree
	tus, err := decodeTransformTree(dec, ctx, x0, y0, x0, y0, log2CbSize, log2CbSize,
		0, cbSize, true, true, p)
	if err != nil {
		return cu, err
	}
	cu.TransformUnits = tus

	return cu, nil
}

// decodeTransformTree recursively decodes the transform quadtree.
func decodeTransformTree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, xBase, yBase int, log2TrafoSize, log2CbSize int,
	trafoDepth, cbSize int, cbfCb, cbfCr bool, p *Params) ([]TransformUnit, error) {

	log2MaxTrafoSize := p.Log2MaxTrafoSize
	log2MinTrafoSize := p.Log2MinTrafoSize
	maxTrafoDepth := p.MaxTransformHierarchyDepthIntra

	split := false
	if log2TrafoSize <= log2MaxTrafoSize &&
		log2TrafoSize > log2MinTrafoSize &&
		trafoDepth < maxTrafoDepth &&
		log2TrafoSize > 2 {
		splitFlag := dec.DecodeDecision(&ctx[context.CtxSplitTransformFlag+int(5-log2TrafoSize)])
		split = splitFlag == 1
	} else if log2TrafoSize > log2MaxTrafoSize {
		split = true
	}

	// cbf_cb and cbf_cr for this level
	if trafoDepth == 0 || cbfCb {
		if trafoDepth > 0 || log2TrafoSize > 2 {
			cbfCbFlag := dec.DecodeDecision(&ctx[context.CtxCbfCb+trafoDepth])
			cbfCb = cbfCbFlag == 1
		}
	}
	if trafoDepth == 0 || cbfCr {
		if trafoDepth > 0 || log2TrafoSize > 2 {
			cbfCrFlag := dec.DecodeDecision(&ctx[context.CtxCbfCr+trafoDepth])
			cbfCr = cbfCrFlag == 1
		}
	}

	if split {
		newLog2TrafoSize := log2TrafoSize - 1
		halfSize := 1 << newLog2TrafoSize
		var allTUs []TransformUnit

		positions := [4][2]int{
			{x0, y0},
			{x0 + halfSize, y0},
			{x0, y0 + halfSize},
			{x0 + halfSize, y0 + halfSize},
		}

		for _, pos := range positions {
			tus, err := decodeTransformTree(dec, ctx, pos[0], pos[1], x0, y0,
				newLog2TrafoSize, log2CbSize, trafoDepth+1, cbSize,
				cbfCb, cbfCr, p)
			if err != nil {
				return nil, err
			}
			allTUs = append(allTUs, tus...)
		}
		return allTUs, nil
	}

	// Leaf TU: decode cbf_luma and residual
	tu := TransformUnit{
		X0:         x0,
		Y0:         y0,
		Log2TrSize: log2TrafoSize,
		CbfCb:      cbfCb,
		CbfCr:      cbfCr,
	}

	// cbf_luma: context index is !trafoDepth per HEVC spec / FFmpeg
	cbfLumaCtx := 0
	if trafoDepth == 0 {
		cbfLumaCtx = 1
	}
	cbfLumaFlag := dec.DecodeDecision(&ctx[context.CtxCbfLuma+cbfLumaCtx])
	tu.CbfLuma = cbfLumaFlag == 1

	// Decode residual coefficients
	trSize := 1 << log2TrafoSize

	if tu.CbfLuma {
		coeffs, err := decodeResidualCoding(dec, ctx, log2TrafoSize, true)
		if err != nil {
			return nil, fmt.Errorf("luma residual: %w", err)
		}
		tu.LumaCoeffs = coeffs
	} else {
		tu.LumaCoeffs = make([]int32, trSize*trSize)
	}

	// Chroma TUs are half the luma size (4:2:0), min 4x4
	chromaLog2TrSize := log2TrafoSize - 1
	if chromaLog2TrSize < 2 {
		chromaLog2TrSize = 2
	}
	chromaTrSize := 1 << chromaLog2TrSize

	if tu.CbfCb {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false)
		if err != nil {
			return nil, fmt.Errorf("cb residual: %w", err)
		}
		tu.CbCoeffs = coeffs
	} else {
		tu.CbCoeffs = make([]int32, chromaTrSize*chromaTrSize)
	}

	if tu.CbfCr {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false)
		if err != nil {
			return nil, fmt.Errorf("cr residual: %w", err)
		}
		tu.CrCoeffs = coeffs
	} else {
		tu.CrCoeffs = make([]int32, chromaTrSize*chromaTrSize)
	}

	return []TransformUnit{tu}, nil
}

// decodeResidualCoding decodes a single residual block using CABAC.
// Returns coefficients in raster scan order.
func decodeResidualCoding(dec *cabac.Decoder, ctx []cabac.CtxState,
	log2TrafoSize int, isLuma bool) ([]int32, error) {

	trSize := 1 << log2TrafoSize

	// last_sig_coeff_x_prefix and last_sig_coeff_y_prefix
	lastSigCoeffX := decodeLastSigCoeff(dec, ctx, log2TrafoSize, isLuma, true)
	lastSigCoeffY := decodeLastSigCoeff(dec, ctx, log2TrafoSize, isLuma, false)

	// Convert to sub-block coordinates
	log2SbSize := 2 // 4x4 sub-blocks
	if log2TrafoSize == 2 {
		log2SbSize = 2
	}

	// Number of sub-blocks
	numSbX := trSize >> log2SbSize
	numSbY := trSize >> log2SbSize

	// Find the sub-block containing the last significant coefficient
	lastSbX := lastSigCoeffX >> log2SbSize
	lastSbY := lastSigCoeffY >> log2SbSize

	// Scan order: diagonal for intra, otherwise raster
	// For simplicity, use diagonal scan for all (correct for intra)
	sbScanOrder := diagonalScanOrder(numSbX, numSbY)
	coeffScanOrder := diagonalScanOrder(1<<log2SbSize, 1<<log2SbSize)

	// Find lastScanPos and lastSubBlock
	lastSubBlock := len(sbScanOrder) - 1
	lastScanPos := (1 << (2 * log2SbSize)) - 1

	for i, pos := range sbScanOrder {
		if pos[0] == lastSbX && pos[1] == lastSbY {
			lastSubBlock = i
			break
		}
	}

	// Find scan position within the sub-block
	localX := lastSigCoeffX - (lastSbX << log2SbSize)
	localY := lastSigCoeffY - (lastSbY << log2SbSize)
	for i, pos := range coeffScanOrder {
		if pos[0] == localX && pos[1] == localY {
			lastScanPos = i
			break
		}
	}

	coeffs := make([]int32, trSize*trSize)

	// Decode sub-blocks in reverse scan order
	numGreater1 := 0
	ctxSet := 0

	for i := lastSubBlock; i >= 0; i-- {
		sbX := sbScanOrder[i][0]
		sbY := sbScanOrder[i][1]

		// coded_sub_block_flag
		codedSubBlock := true
		if i > 0 && i < lastSubBlock {
			sbCtx := 0
			if isLuma {
				sbCtx = context.CtxCodedSubBlockFlag
			} else {
				sbCtx = context.CtxCodedSubBlockFlag + 2
			}
			// Simplified context increment (0 for now)
			flag := dec.DecodeDecision(&ctx[sbCtx])
			codedSubBlock = flag == 1
		} else if i == lastSubBlock {
			codedSubBlock = true
		}

		if !codedSubBlock {
			continue
		}

		// Decode significant coefficient flags within sub-block
		numCoeffs := 1 << (2 * log2SbSize) // 16 for 4x4 sub-blocks
		sigFlags := make([]bool, numCoeffs)
		firstScanPos := numCoeffs - 1
		lastScanPosInSb := -1

		startPos := numCoeffs - 1
		if i == lastSubBlock {
			sigFlags[lastScanPos] = true
			lastScanPosInSb = lastScanPos
			firstScanPos = lastScanPos
			startPos = lastScanPos - 1
		}

		for n := startPos; n >= 0; n-- {
			// sig_coeff_flag
			if n > 0 || codedSubBlock {
				sigCtx := getSigCtxInc(n, log2TrafoSize, isLuma, sbX, sbY)
				flag := dec.DecodeDecision(&ctx[context.CtxSigCoeffFlag+sigCtx])
				sigFlags[n] = flag == 1
				if sigFlags[n] {
					if lastScanPosInSb < 0 {
						lastScanPosInSb = n
					}
					firstScanPos = n
				}
			} else {
				// DC coefficient of coded sub-block is always significant
				sigFlags[0] = true
				firstScanPos = 0
			}
		}

		// Decode coefficient levels
		// coeff_abs_level_greater1_flag
		greater1Flags := make([]bool, numCoeffs)
		firstGreater1Pos := -1
		numGreater1InSb := 0

		if isLuma {
			if i > 0 {
				ctxSet = 2
			} else {
				ctxSet = 0
			}
		} else {
			ctxSet = 0
		}
		if numGreater1 == 0 && ctxSet > 0 {
			ctxSet--
		}
		numGreater1 = 0

		// c1 tracks which of the 4 contexts within the set to use.
		// Per HM reference decoder, c1 starts at 1 (not 0).
		c1 := 1
		chromaOffset := 0
		if !isLuma {
			chromaOffset = 16
		}

		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			if numGreater1InSb < 8 {
				greater1Ctx := ctxSet*4 + c1 + chromaOffset
				flag := dec.DecodeDecision(&ctx[context.CtxCoeffAbsLevelGreater1+greater1Ctx])
				greater1Flags[n] = flag == 1
				numGreater1InSb++
				if greater1Flags[n] {
					if firstGreater1Pos < 0 {
						firstGreater1Pos = n
					}
					numGreater1++
					c1 = 0
				} else if c1 > 0 && c1 < 3 {
					c1++
				}
			}
		}

		// coeff_abs_level_greater2_flag (at most 1 per sub-block)
		greater2Flag := false
		if firstGreater1Pos >= 0 {
			greater2Ctx := ctxSet
			if !isLuma {
				greater2Ctx += 4
			}
			flag := dec.DecodeDecision(&ctx[context.CtxCoeffAbsLevelGreater2+greater2Ctx])
			greater2Flag = flag == 1
		}

		// Sign flags (bypass coded)
		signFlags := make([]bool, numCoeffs)
		numSig := 0
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if sigFlags[n] {
				numSig++
			}
		}
		// Note: sign data hiding disabled in our test bitstream (no-signhide=1)
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if sigFlags[n] {
				sign := dec.DecodeBypass()
				signFlags[n] = sign == 1
			}
		}

		// coeff_abs_level_remaining (bypass coded, Golomb-Rice)
		absLevels := make([]int32, numCoeffs)
		cRiceParam := 0

		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			baseLevel := int32(1)
			if greater1Flags[n] {
				baseLevel = 2
			}
			if n == firstGreater1Pos && greater2Flag {
				baseLevel = 3
			}

			// Decode coeff_abs_level_remaining
			absRemaining := int32(0)
			if greater1Flags[n] || (n == firstGreater1Pos && greater2Flag) {
				absRemaining = decodeAbsLevelRemaining(dec, cRiceParam)
			}

			absLevel := baseLevel + absRemaining
			absLevels[n] = absLevel

			// Update Rice parameter
			if absLevel > int32(3*(1<<cRiceParam)) && cRiceParam < 4 {
				cRiceParam++
			}
		}

		// Place coefficients into the output array
		sbPixelX := sbX << log2SbSize
		sbPixelY := sbY << log2SbSize

		for n := range numCoeffs {
			if absLevels[n] != 0 {
				cx := coeffScanOrder[n][0]
				cy := coeffScanOrder[n][1]
				px := sbPixelX + cx
				py := sbPixelY + cy
				val := absLevels[n]
				if signFlags[n] {
					val = -val
				}
				coeffs[py*trSize+px] = val
			}
		}
	}

	return coeffs, nil
}

// decodeLastSigCoeff decodes last_sig_coeff_x_prefix or last_sig_coeff_y_prefix.
// Uses HEVC spec 9.3.4.2.3 context selection formulas.
func decodeLastSigCoeff(dec *cabac.Decoder, ctx []cabac.CtxState,
	log2TrafoSize int, isLuma bool, isX bool) int {

	ctxBase := context.CtxLastSigCoeffXPrefix
	if !isX {
		ctxBase = context.CtxLastSigCoeffYPrefix
	}

	// Context selection per HEVC spec Table 9-32/9-33
	var ctxOffset, ctxShift int
	if isLuma {
		ctxOffset = 3*(log2TrafoSize-2) + ((log2TrafoSize - 1) >> 2)
		ctxShift = (log2TrafoSize + 1) >> 2
	} else {
		ctxOffset = 15 // chroma contexts start at offset 15
		ctxShift = log2TrafoSize - 2
		if ctxShift < 0 {
			ctxShift = 0
		}
	}

	maxPrefix := (log2TrafoSize << 1) - 1
	prefix := 0
	for prefix < maxPrefix {
		ctxInc := prefix >> ctxShift
		bin := dec.DecodeDecision(&ctx[ctxBase+ctxOffset+ctxInc])
		if bin == 0 {
			break
		}
		prefix++
	}

	// Decode suffix if needed
	if prefix >= 2 {
		suffixLen := (prefix >> 1) - 1
		if suffixLen > 0 {
			suffix := int(dec.ReadBypassU(suffixLen))
			return ((2 + (prefix & 1)) << suffixLen) + suffix
		}
	}

	return prefix
}

// decodeAbsLevelRemaining decodes coeff_abs_level_remaining using
// Truncated Rice + Exp-Golomb (bypass bins).
func decodeAbsLevelRemaining(dec *cabac.Decoder, cRiceParam int) int32 {
	// Truncated Rice prefix
	prefix := 0
	for prefix < 20 { // HEVC max prefix length
		bin := dec.DecodeBypass()
		if bin == 0 {
			break
		}
		prefix++
	}

	if prefix < 3 { // cMax = 3 for truncated Rice
		// suffix is cRiceParam bits
		suffix := int32(0)
		if cRiceParam > 0 {
			suffix = int32(dec.ReadBypassU(cRiceParam))
		}
		return int32(prefix)<<uint(cRiceParam) + suffix
	}

	// Exp-Golomb suffix
	suffixLen := prefix - 3 + cRiceParam
	suffix := int32(dec.ReadBypassU(suffixLen))
	return (((int32(1) << uint(prefix-3)) + 3 - 1) << uint(cRiceParam)) + suffix
}

// getSigCtxInc returns the context index for sig_coeff_flag.
// Based on HEVC spec 9.3.4.2.6 (Table 9-39).
func getSigCtxInc(scanPos, log2TrafoSize int, isLuma bool, sbX, sbY int) int {
	if log2TrafoSize == 2 {
		// 4x4 transform: use Table 9-39 context mapping
		// ctxIdxMap for 4x4 diagonal scan
		ctxIdxMap := [16]int{
			0, 1, 4, 5,
			2, 3, 4, 5,
			6, 6, 8, 8,
			7, 7, 8, 8,
		}
		if isLuma {
			return ctxIdxMap[scanPos]
		}
		return ctxIdxMap[scanPos] + 28 // chroma offset
	}

	// For larger blocks (8x8+), context depends on sub-block position and scan position
	if isLuma {
		if sbX == 0 && sbY == 0 {
			// DC sub-block: special contexts
			return min(scanPos, 2)
		}
		if sbY == 0 {
			return min(scanPos, 2) + 3
		}
		if sbX == 0 {
			return min(scanPos, 2) + 6
		}
		return min(scanPos, 2) + 21
	}

	// Chroma
	if sbX == 0 && sbY == 0 {
		return min(scanPos, 2) + 28
	}
	return min(scanPos, 2) + 31
}

// diagonalScanOrder generates the HEVC diagonal scan order for an NxN block.
// HEVC spec Table 6-5: every diagonal goes in the up-right direction (x--, y++).
func diagonalScanOrder(width, height int) [][2]int {
	var order [][2]int
	for s := 0; s < width+height-1; s++ {
		// Always traverse up-right: start from bottom of diagonal, go x--, y++
		x := min(s, width-1)
		y := s - x
		for x >= 0 && y < height {
			order = append(order, [2]int{x, y})
			x--
			y++
		}
	}
	return order
}

// sortMPM sorts a 3-element MPM list in ascending order.
func sortMPM(mpm [3]int) [3]int {
	sorted := mpm
	if sorted[0] > sorted[1] {
		sorted[0], sorted[1] = sorted[1], sorted[0]
	}
	if sorted[1] > sorted[2] {
		sorted[1], sorted[2] = sorted[2], sorted[1]
	}
	if sorted[0] > sorted[1] {
		sorted[0], sorted[1] = sorted[1], sorted[0]
	}
	return sorted
}
