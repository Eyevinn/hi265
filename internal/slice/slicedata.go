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
	SkipFlag   bool
	MergeIdx   int
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
	TransformSkipLuma bool
	TransformSkipCb   bool
	TransformSkipCr   bool
	LumaCoeffs   []int32 // coefficients in scan order
	CbCoeffs     []int32
	CrCoeffs     []int32
}

// SaoParams holds SAO parameters for one CTU (per component: 0=luma, 1=Cb, 2=Cr).
type SaoParams struct {
	TypeIdx [3]int    // 0=none, 1=band offset, 2=edge offset
	Offsets [3][4]int // 4 offset values per component
	BandPos [3]int    // band position (BO mode)
	EoClass [3]int    // edge offset class (EO mode)
}

// SliceData holds all decoded CUs for a slice.
type SliceData struct {
	CUs       []CodingUnit
	SaoParams []SaoParams // one per CTU, indexed by CTU raster address
}

// Params holds SPS/PPS-derived parameters needed for slice data decoding.
type Params struct {
	SliceType                       int // 0=B, 1=P, 2=I
	SliceQPY                        int
	PicWidth, PicHeight             int
	Log2CtbSize                     int
	Log2MinCbSize                   int
	Log2MinTrafoSize                int
	Log2MaxTrafoSize                int
	MaxTransformHierarchyDepthIntra int
	MaxNumMergeCand                 int
	SignDataHidingEnabled           bool
	TransformSkipEnabled            bool
	SaoLuma                         bool
	SaoChroma                       bool
	Trace                           bool
}

// DecodeSliceData decodes the slice data segment using CABAC.
// cabacData is the raw CABAC bitstream (after slice header, emulation prevention removed).
func DecodeSliceData(cabacData []byte, p Params) (*SliceData, error) {
	dec, err := cabac.NewDecoder(cabacData)
	if err != nil {
		return nil, fmt.Errorf("init CABAC: %w", err)
	}
	dec.Trace = p.Trace
	ctxModels := context.InitModels(p.SliceType, p.SliceQPY)

	ctbSize := 1 << p.Log2CtbSize
	ctbsX := (p.PicWidth + ctbSize - 1) / ctbSize
	ctbsY := (p.PicHeight + ctbSize - 1) / ctbSize

	sd := &SliceData{}
	modeMap := newIntraModeMap(p.PicWidth, p.PicHeight)
	depthMap := newCuDepthMap(p.PicWidth, p.PicHeight, 1<<p.Log2MinCbSize)

	numCTUs := ctbsX * ctbsY
	sd.SaoParams = make([]SaoParams, numCTUs)

	for ctbAddrRS := 0; ctbAddrRS < numCTUs; ctbAddrRS++ {
		ctbX := (ctbAddrRS % ctbsX) * ctbSize
		ctbY := (ctbAddrRS / ctbsX) * ctbSize

		// Decode SAO parameters for this CTU
		if p.SaoLuma || p.SaoChroma {
			sd.SaoParams[ctbAddrRS] = decodeSaoParams(dec, ctxModels, ctbX, ctbY, ctbsX, ctbAddrRS, sd.SaoParams, p.SaoLuma, p.SaoChroma)
		}

		cus, err := decodeCodingQuadtree(dec, ctxModels, ctbX, ctbY, p.Log2CtbSize, 0, &p, modeMap, depthMap)
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

// decodeSaoParams decodes SAO parameters for one CTU per HEVC spec 7.3.8.3.
func decodeSaoParams(dec *cabac.Decoder, ctx []cabac.CtxState,
	ctbX, ctbY, ctbsX, ctbAddrRS int, saoParams []SaoParams,
	saoLuma, saoChroma bool) SaoParams {

	var sao SaoParams

	// sao_merge_left_flag
	if ctbX > 0 {
		mergeLeft := dec.DecodeDecision(&ctx[context.CtxSaoMergeFlag])
		if mergeLeft == 1 {
			return saoParams[ctbAddrRS-1]
		}
	}

	// sao_merge_up_flag
	if ctbY > 0 {
		mergeUp := dec.DecodeDecision(&ctx[context.CtxSaoMergeFlag])
		if mergeUp == 1 {
			return saoParams[ctbAddrRS-ctbsX]
		}
	}

	// Decode per-component SAO parameters
	// Per HEVC spec 7.3.8.3, type is decoded for luma (cIdx=0) and
	// once for chroma (cIdx=1), with cIdx=2 sharing type/class from cIdx=1.
	for cIdx := range 3 {
		if cIdx == 0 && !saoLuma {
			continue
		}
		if cIdx > 0 && !saoChroma {
			continue
		}

		// sao_type_idx: decoded for cIdx 0 and 1 only
		typeIdx := 0
		if cIdx < 2 {
			bin0 := dec.DecodeDecision(&ctx[context.CtxSaoTypeIdx])
			if bin0 == 1 {
				bin1 := dec.DecodeBypass()
				if bin1 == 0 {
					typeIdx = 1 // Band Offset
				} else {
					typeIdx = 2 // Edge Offset
				}
			}
			sao.TypeIdx[cIdx] = typeIdx
			if cIdx == 1 {
				// Cr shares type from Cb
				sao.TypeIdx[2] = typeIdx
			}
		} else {
			typeIdx = sao.TypeIdx[2]
		}

		if typeIdx == 0 {
			continue
		}

		// sao_offset_abs[i] (4 values per component)
		for i := range 4 {
			sao.Offsets[cIdx][i] = decodeSaoOffsetAbs(dec)
		}

		if typeIdx == 1 {
			// Band Offset: decode sign flags and band position
			for i := range 4 {
				if sao.Offsets[cIdx][i] != 0 {
					sign := dec.DecodeBypass()
					if sign == 1 {
						sao.Offsets[cIdx][i] = -sao.Offsets[cIdx][i]
					}
				}
			}
			// sao_band_position (5 bypass bins)
			sao.BandPos[cIdx] = int(dec.ReadBypassU(5))
			if cIdx == 1 {
				sao.BandPos[2] = sao.BandPos[1]
			}
		} else {
			// Edge Offset: categories 1,2 positive, 3,4 negative (spec Table 8-12)
			sao.Offsets[cIdx][2] = -sao.Offsets[cIdx][2]
			sao.Offsets[cIdx][3] = -sao.Offsets[cIdx][3]
			if cIdx < 2 {
				// sao_eo_class (2 bypass bins) - only for luma and once for chroma
				sao.EoClass[cIdx] = int(dec.ReadBypassU(2))
				if cIdx == 1 {
					sao.EoClass[2] = sao.EoClass[1]
				}
			}
		}
	}
	return sao
}

// decodeSaoOffsetAbs decodes one sao_offset_abs value using truncated Rice (bypass bins).
// Per HEVC spec 9.3.3.3, max value for 8-bit is (1<<(min(bitDepth,10)-5))-1 = 7.
func decodeSaoOffsetAbs(dec *cabac.Decoder) int {
	val := 0
	for val < 7 { // max for 8-bit
		bin := dec.DecodeBypass()
		if bin == 0 {
			break
		}
		val++
	}
	return val
}

// decodeCodingQuadtree recursively splits the CTU into CUs.
func decodeCodingQuadtree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize, depth int, p *Params, modeMap *intraModeMap, depthMap *cuDepthMap) ([]CodingUnit, error) {

	split := false
	if log2CbSize > p.Log2MinCbSize {
		// Determine context for split_cu_flag per HEVC spec 9.3.4.2.2 Table 9-37
		ctxInc := 0
		// condL: left neighbor at (x0-1, y0) has depth > current depth
		depthL := depthMap.get(x0-1, y0)
		if depthL >= 0 && depthL > depth {
			ctxInc++
		}
		// condA: above neighbor at (x0, y0-1) has depth > current depth
		depthA := depthMap.get(x0, y0-1)
		if depthA >= 0 && depthA > depth {
			ctxInc++
		}
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
					newLog2CbSize, depth+1, p, modeMap, depthMap)
				if err != nil {
					return nil, err
				}
				allCUs = append(allCUs, cus...)
			}
		}
		return allCUs, nil
	}

	// Store CU depth in map
	cuSize := 1 << log2CbSize
	depthMap.set(x0, y0, cuSize, depth)

	// Decode coding unit
	cu, err := decodeCodingUnit(dec, ctx, x0, y0, log2CbSize, p, modeMap)
	if err != nil {
		return nil, err
	}
	return []CodingUnit{cu}, nil
}

// decodeCodingUnit decodes a single CU.
func decodeCodingUnit(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize int, p *Params, modeMap *intraModeMap) (CodingUnit, error) {

	cu := CodingUnit{
		X0:         x0,
		Y0:         y0,
		Log2CbSize: log2CbSize,
	}

	cbSize := 1 << log2CbSize

	// For P/B-slices: decode cu_skip_flag
	if p.SliceType != context.SliceTypeI {
		// cu_skip_flag context: 3 contexts based on left/above neighbors
		ctxInc := 0
		// Check left neighbor skip
		// For simplicity, use availability-based context (similar to split_cu_flag)
		if x0 > 0 {
			ctxInc++
		}
		if y0 > 0 {
			ctxInc++
		}
		// Clamp to valid context range (0-2)
		if ctxInc > 2 {
			ctxInc = 2
		}
		skipFlag := dec.DecodeDecision(&ctx[context.CtxCuSkipFlag+ctxInc])
		cu.SkipFlag = skipFlag == 1

		if cu.SkipFlag {
			cu.PredMode = 0 // inter
			// merge_idx: truncated unary, bypass coded with context for first bin
			if p.MaxNumMergeCand > 1 {
				// First bin is context-coded
				bin0 := dec.DecodeDecision(&ctx[context.CtxMergeIdx])
				if bin0 == 1 {
					idx := 1
					for idx < p.MaxNumMergeCand-1 {
						bin := dec.DecodeBypass()
						if bin == 0 {
							break
						}
						idx++
					}
					cu.MergeIdx = idx
				}
			}
			// Store mode in map (use DC as placeholder for intra mode map)
			modeMap.set(x0, y0, cbSize, 1)
			return cu, nil
		}
	}

	cu.PredMode = 1 // intra

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
	puSize := cbSize
	if cu.PartMode == 1 {
		numPU = 4
		puSize = cbSize / 2
	}

	// PU positions (raster order within CU)
	puPositions := make([][2]int, numPU)
	if numPU == 1 {
		puPositions[0] = [2]int{x0, y0}
	} else {
		half := cbSize / 2
		puPositions[0] = [2]int{x0, y0}
		puPositions[1] = [2]int{x0 + half, y0}
		puPositions[2] = [2]int{x0, y0 + half}
		puPositions[3] = [2]int{x0 + half, y0 + half}
	}

	// prev_intra_luma_pred_flag for each PU
	prevFlags := make([]bool, numPU)
	for i := range numPU {
		flag := dec.DecodeDecision(&ctx[context.CtxPrevIntraLumaPredFlag])
		prevFlags[i] = flag == 1
	}

	// mpm_idx or rem_intra_luma_pred_mode for each PU
	for i := range numPU {
		px, py := puPositions[i][0], puPositions[i][1]

		// Derive MPM list from left and above neighbors (HEVC spec 8.4.2)
		candA := modeMap.get(px-1, py) // left neighbor
		candB := modeMap.get(px, py-1) // above neighbor
		mpmList := deriveMPM(candA, candB)
		if prevFlags[i] {
			// mpm_idx: decoded as truncated unary, max 2
			mpmIdx := 0
			if dec.DecodeBypass() == 1 {
				mpmIdx++
				if dec.DecodeBypass() == 1 {
					mpmIdx++
				}
			}
			cu.IntraLumaMode[i] = mpmList[mpmIdx]
		} else {
			// rem_intra_luma_pred_mode: 5 bypass bins
			rem := int(dec.ReadBypassU(5))
			// Convert rem to actual mode (skipping MPM entries)
			sorted := sortMPM(mpmList)
			mode := rem
			for _, mpm := range sorted {
				if mode >= mpm {
					mode++
				}
			}
			cu.IntraLumaMode[i] = mode
		}

		// Store mode in map so subsequent PUs can use it as neighbor
		modeMap.set(px, py, puSize, cu.IntraLumaMode[i])
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
		0, cbSize, true, true, p, cu.IntraLumaMode, x0, y0)
	if err != nil {
		return cu, err
	}
	cu.TransformUnits = tus

	return cu, nil
}

// decodeTransformTree recursively decodes the transform quadtree.
func decodeTransformTree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, xBase, yBase int, log2TrafoSize, log2CbSize int,
	trafoDepth, cbSize int, cbfCb, cbfCr bool, p *Params,
	lumaIntraModes [4]int, cuX0, cuY0 int) ([]TransformUnit, error) {

	log2MaxTrafoSize := p.Log2MaxTrafoSize
	log2MinTrafoSize := p.Log2MinTrafoSize
	maxTrafoDepth := p.MaxTransformHierarchyDepthIntra

	split := false
	if dec.Trace {
		fmt.Printf("  [TT] check split: log2TrSize=%d max=%d min=%d depth=%d/%d cond=%v,%v,%v,%v\n",
			log2TrafoSize, log2MaxTrafoSize, log2MinTrafoSize, trafoDepth, maxTrafoDepth,
			log2TrafoSize <= log2MaxTrafoSize, log2TrafoSize > log2MinTrafoSize,
			trafoDepth < maxTrafoDepth, log2TrafoSize > 2)
	}
	if log2TrafoSize <= log2MaxTrafoSize &&
		log2TrafoSize > log2MinTrafoSize &&
		trafoDepth < maxTrafoDepth &&
		log2TrafoSize > 2 {
		splitFlag := dec.DecodeDecision(&ctx[context.CtxSplitTransformFlag+int(5-log2TrafoSize)])
		split = splitFlag == 1
	} else if log2TrafoSize > log2MaxTrafoSize {
		split = true
	}

	// cbf_cb and cbf_cr: only signaled when log2TrafoSize > 2 for 4:2:0
	// (chroma block must be at least 4x4, which requires luma >= 8x8)
	if log2TrafoSize > 2 {
		if trafoDepth == 0 || cbfCb {
			cbfCbFlag := dec.DecodeDecision(&ctx[context.CtxCbfCb+trafoDepth])
			cbfCb = cbfCbFlag == 1
		}
		if trafoDepth == 0 || cbfCr {
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
				cbfCb, cbfCr, p, lumaIntraModes, cuX0, cuY0)
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

	// Parse transform_skip_flag for 4x4 TUs when enabled
	if p.TransformSkipEnabled && log2TrafoSize == 2 {
		if tu.CbfLuma {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag])
			tu.TransformSkipLuma = tsFlag == 1
		}
	}

	// Decode residual coefficients
	trSize := 1 << log2TrafoSize

	// Compute luma scanIdx based on intra mode
	// HEVC spec: for 4x4 luma TUs, mode 6-14 → horizontal, mode 22-30 → vertical
	lumaScanIdx := 0
	if log2TrafoSize == 2 {
		// Determine which PU this TU belongs to
		puIdx := 0
		halfCb := cbSize / 2
		if x0 >= cuX0+halfCb {
			puIdx += 1
		}
		if y0 >= cuY0+halfCb {
			puIdx += 2
		}
		lumaMode := lumaIntraModes[puIdx]
		if lumaMode >= 6 && lumaMode <= 14 {
			lumaScanIdx = 1
		} else if lumaMode >= 22 && lumaMode <= 30 {
			lumaScanIdx = 2
		}
	}

	if tu.CbfLuma {
		coeffs, err := decodeResidualCoding(dec, ctx, log2TrafoSize, true, lumaScanIdx, p.SignDataHidingEnabled)
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

	// For 4:2:0, chroma residuals at log2TrafoSize==2 are only present
	// for the TU at the base position (top-left of the 8x8 group)
	parseChroma := log2TrafoSize > 2 || (x0 == xBase && y0 == yBase)

	// Parse chroma transform_skip_flag for 4x4 chroma TUs
	if p.TransformSkipEnabled && parseChroma && chromaLog2TrSize == 2 {
		if tu.CbfCb {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag+1])
			tu.TransformSkipCb = tsFlag == 1
		}
		if tu.CbfCr {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag+1])
			tu.TransformSkipCr = tsFlag == 1
		}
	}

	if parseChroma && tu.CbfCb {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false, 0, p.SignDataHidingEnabled)
		if err != nil {
			return nil, fmt.Errorf("cb residual: %w", err)
		}
		tu.CbCoeffs = coeffs
	} else {
		tu.CbCoeffs = make([]int32, chromaTrSize*chromaTrSize)
	}

	if parseChroma && tu.CbfCr {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false, 0, p.SignDataHidingEnabled)
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
	log2TrafoSize int, isLuma bool, scanIdx int, signDataHiding bool) ([]int32, error) {

	trSize := 1 << log2TrafoSize

	// HEVC spec 7.3.8.11: both prefixes first, then both suffixes
	lastSigCoeffXPrefix := decodeLastSigCoeffPrefix(dec, ctx, log2TrafoSize, isLuma, true)
	lastSigCoeffYPrefix := decodeLastSigCoeffPrefix(dec, ctx, log2TrafoSize, isLuma, false)
	lastSigCoeffX := decodeLastSigCoeffSuffix(dec, lastSigCoeffXPrefix)
	lastSigCoeffY := decodeLastSigCoeffSuffix(dec, lastSigCoeffYPrefix)
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

	// Scan order depends on scanIdx: 0=diagonal, 1=horizontal, 2=vertical
	sbScanOrder := scanOrder(numSbX, numSbY, scanIdx)
	coeffScanOrder := scanOrder(1<<log2SbSize, 1<<log2SbSize, scanIdx)

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

	// Track which sub-blocks are coded for context derivation
	codedSbFlags := make([][]bool, numSbY)
	for j := range codedSbFlags {
		codedSbFlags[j] = make([]bool, numSbX)
	}

	// Decode sub-blocks in reverse scan order
	// lastC1 tracks the c1 state from the previous sub-block's greater1 decoding.
	// c1=0 means previous sub-block had a coefficient with greater1_flag=1.
	// Initialized to 1 (no previous sub-block).
	lastC1 := 1
	ctxSet := 0

	for i := lastSubBlock; i >= 0; i-- {
		sbX := sbScanOrder[i][0]
		sbY := sbScanOrder[i][1]

		// coded_sub_block_flag
		codedSubBlock := true
		if i > 0 && i < lastSubBlock {
			// Context derivation per HEVC spec 9.3.4.2.4
			// Check right neighbor (sbX+1, sbY) and below neighbor (sbX, sbY+1)
			csbfRight := 0
			csbfBelow := 0
			if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
				csbfRight = 1
			}
			if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
				csbfBelow = 1
			}
			ctxInc := min(csbfRight+csbfBelow, 1)

			sbCtx := context.CtxCodedSubBlockFlag
			if !isLuma {
				sbCtx += 2
			}
			flag := dec.DecodeDecision(&ctx[sbCtx+ctxInc])
			codedSubBlock = flag == 1
		} else if i == lastSubBlock {
			codedSubBlock = true
		}

		codedSbFlags[sbY][sbX] = codedSubBlock

		if !codedSubBlock {
			continue
		}

		// Decode significant coefficient flags within sub-block
		numCoeffs := 1 << (2 * log2SbSize) // 16 for 4x4 sub-blocks
		sigFlags := make([]bool, numCoeffs)
		firstScanPos := numCoeffs - 1
		lastScanPosInSb := -1

		// Compute prevCsbf for sig_coeff_flag context derivation
		// prevCsbf = csbfRight + 2*csbfBelow
		prevCsbf := 0
		if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
			prevCsbf += 1
		}
		if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
			prevCsbf += 2
		}

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
				cx := coeffScanOrder[n][0]
				cy := coeffScanOrder[n][1]
				sigCtx := getSigCtxInc(cx, cy, log2TrafoSize, isLuma, sbX, sbY, scanIdx, prevCsbf)
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
		greater1Decoded := make([]bool, numCoeffs) // tracks if greater1 was decoded
		firstGreater1Pos := -1
		numGreater1InSb := 0

		if isLuma && i > 0 {
			ctxSet = 2
		} else {
			ctxSet = 0
		}
		if lastC1 == 0 {
			ctxSet++
		}

		// c1 tracks which of the 4 contexts within the set to use.
		// Reset to 1 at the start of each sub-block.
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
				greater1Decoded[n] = true
				numGreater1InSb++
				if greater1Flags[n] {
					if firstGreater1Pos < 0 {
						firstGreater1Pos = n
					}
					c1 = 0
				} else if c1 > 0 && c1 < 3 {
					c1++
				}
			}
		}

		// Update lastC1 for next sub-block's ctxSet derivation
		lastC1 = c1

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
		signHidden := signDataHiding && (lastScanPosInSb-firstScanPos) > 3
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if sigFlags[n] {
				if signHidden && n == firstScanPos {
					// Sign will be inferred from parity
					continue
				}
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
			if greater1Decoded[n] && greater1Flags[n] {
				baseLevel = 2
			}
			if n == firstGreater1Pos && greater2Flag {
				baseLevel = 3
			}

			// Decode coeff_abs_level_remaining per HEVC spec:
			// coeff_has_max_base_level is initially 1 for each sig coeff,
			// set to 0 when greater1_flag==0, and overridden with greater2_flag
			// at firstGreater1Pos.
			absRemaining := int32(0)
			needRemaining := !greater1Decoded[n] || // bypassed (>=8 sig coeffs)
				(greater1Decoded[n] && greater1Flags[n] && n != firstGreater1Pos) || // greater1=1, not first
				(n == firstGreater1Pos && greater2Flag) // first greater1 with greater2=1
			if needRemaining {
				absRemaining = decodeAbsLevelRemaining(dec, cRiceParam)
			}

			absLevel := baseLevel + absRemaining
			absLevels[n] = absLevel

			// Update Rice parameter
			if absLevel > int32(3*(1<<cRiceParam)) && cRiceParam < 4 {
				cRiceParam++
			}
		}

		// Sign data hiding: infer sign of firstScanPos from parity
		if signHidden {
			sumAbs := int32(0)
			for n := firstScanPos; n <= lastScanPosInSb; n++ {
				sumAbs += absLevels[n]
			}
			if sumAbs%2 != 0 {
				// Odd sum → sign is negative
				signFlags[firstScanPos] = true
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

// decodeLastSigCoeffPrefix decodes last_sig_coeff_x_prefix or last_sig_coeff_y_prefix.
// Returns the raw prefix value. Suffix is decoded separately per HEVC spec 7.3.8.11.
func decodeLastSigCoeffPrefix(dec *cabac.Decoder, ctx []cabac.CtxState,
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

	return prefix
}

// decodeLastSigCoeffSuffix decodes the suffix for last_sig_coeff_x or last_sig_coeff_y.
func decodeLastSigCoeffSuffix(dec *cabac.Decoder, prefix int) int {
	if prefix > 3 {
		suffixLen := (prefix >> 1) - 1
		suffix := int(dec.ReadBypassU(suffixLen))
		return ((2 + (prefix & 1)) << suffixLen) + suffix
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
// Based on HEVC spec 9.3.4.2.6.
// prevCsbf = csbfRight + 2*csbfBelow (coded_sub_block_flag of right and below neighbors).
func getSigCtxInc(cx, cy, log2TrafoSize int, isLuma bool, sbX, sbY, scanIdx, prevCsbf int) int {
	if log2TrafoSize == 2 {
		// 4x4 transform: use Table 9-39 context mapping
		ctxIdxMaps := [3][16]int{
			{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8, 8}, // diagonal
			{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8, 8}, // horizontal
			{0, 2, 6, 7, 1, 3, 6, 7, 4, 4, 8, 8, 5, 5, 8, 8}, // vertical
		}
		blkPos := cy*4 + cx
		if isLuma {
			return ctxIdxMaps[scanIdx][blkPos]
		}
		return ctxIdxMaps[scanIdx][blkPos] + 27
	}

	// For larger blocks (8x8+), per HEVC spec 9.3.4.2.6
	xP := cx // position within 4x4 sub-block
	yP := cy

	// DC position of DC sub-block
	if xP+yP == 0 && sbX == 0 && sbY == 0 {
		sigCtx := 0
		if isLuma {
			return sigCtx
		}
		return sigCtx + 27
	}

	// Compute base sigCtx from prevCsbf and position within sub-block
	var sigCtx int
	switch prevCsbf {
	case 0:
		if xP+yP >= 3 {
			sigCtx = 0
		} else if xP+yP > 0 {
			sigCtx = 1
		} else {
			sigCtx = 2
		}
	case 1: // right neighbor coded
		if yP == 0 {
			sigCtx = 2
		} else if yP == 1 {
			sigCtx = 1
		} else {
			sigCtx = 0
		}
	case 2: // below neighbor coded
		if xP == 0 {
			sigCtx = 2
		} else if xP == 1 {
			sigCtx = 1
		} else {
			sigCtx = 0
		}
	default: // prevCsbf == 3, both neighbors coded
		sigCtx = 2
	}

	if isLuma {
		if sbX+sbY > 0 {
			sigCtx += 3
		}
		numSbX := (1 << log2TrafoSize) >> 2
		if numSbX == 2 { // 8x8 TU
			if scanIdx == 0 {
				sigCtx += 9
			} else {
				sigCtx += 15
			}
		} else { // 16x16 or 32x32 TU
			sigCtx += 21
		}
		return sigCtx
	}

	// Chroma
	numSbX := (1 << log2TrafoSize) >> 2
	if numSbX == 2 { // 8x8
		sigCtx += 9
	} else {
		sigCtx += 12
	}
	return sigCtx + 27
}

// diagonalScanOrder generates the HEVC diagonal scan order for an NxN block.
// HEVC spec Table 6-5: diagonal scan, within each diagonal goes up-right (x++, y--).
func diagonalScanOrder(width, height int) [][2]int {
	var order [][2]int
	for s := 0; s < width+height-1; s++ {
		y := min(s, height-1)
		x := s - y
		for x < width && y >= 0 {
			order = append(order, [2]int{x, y})
			x++
			y--
		}
	}
	return order
}

// horizontalScanOrder generates a row-major scan order for an NxN block.
func horizontalScanOrder(width, height int) [][2]int {
	order := make([][2]int, 0, width*height)
	for y := range height {
		for x := range width {
			order = append(order, [2]int{x, y})
		}
	}
	return order
}

// verticalScanOrder generates a column-major scan order for an NxN block.
func verticalScanOrder(width, height int) [][2]int {
	order := make([][2]int, 0, width*height)
	for x := range width {
		for y := range height {
			order = append(order, [2]int{x, y})
		}
	}
	return order
}

// scanOrder returns the scan order for the given scanIdx.
// scanIdx: 0=diagonal, 1=horizontal, 2=vertical.
func scanOrder(width, height, scanIdx int) [][2]int {
	switch scanIdx {
	case 1:
		return horizontalScanOrder(width, height)
	case 2:
		return verticalScanOrder(width, height)
	default:
		return diagonalScanOrder(width, height)
	}
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

// cuDepthMap tracks CU split depths at minimum CB granularity.
type cuDepthMap struct {
	depths   []int // flat array, indexed as [y/minCb * widthMinCb + x/minCb]
	minCbSize int
	widthMinCb int
}

func newCuDepthMap(picW, picH, minCbSize int) *cuDepthMap {
	w := (picW + minCbSize - 1) / minCbSize
	h := (picH + minCbSize - 1) / minCbSize
	depths := make([]int, w*h)
	for i := range depths {
		depths[i] = -1 // unavailable
	}
	return &cuDepthMap{depths: depths, minCbSize: minCbSize, widthMinCb: w}
}

// set stores the depth for all minCb blocks covered by the CU at (x0,y0) of given size.
func (m *cuDepthMap) set(x0, y0, cuSize, depth int) {
	for y := y0 / m.minCbSize; y < (y0+cuSize)/m.minCbSize; y++ {
		for x := x0 / m.minCbSize; x < (x0+cuSize)/m.minCbSize; x++ {
			m.depths[y*m.widthMinCb+x] = depth
		}
	}
}

// get returns the CU depth at pixel position (x,y), or -1 if unavailable.
func (m *cuDepthMap) get(x, y int) int {
	bx, by := x/m.minCbSize, y/m.minCbSize
	if bx < 0 || by < 0 || bx >= m.widthMinCb || by*m.widthMinCb+bx >= len(m.depths) {
		return -1
	}
	return m.depths[by*m.widthMinCb+bx]
}

// intraModeMap tracks decoded intra luma prediction modes at 4x4 PU granularity.
type intraModeMap struct {
	modes []int // flat array, indexed as [y/4 * width4 + x/4]
	width4 int  // picture width in 4-sample units
}

func newIntraModeMap(picW, picH int) *intraModeMap {
	w := (picW + 3) / 4
	h := (picH + 3) / 4
	modes := make([]int, w*h)
	for i := range modes {
		modes[i] = -1 // unavailable
	}
	return &intraModeMap{modes: modes, width4: w}
}

// set stores a mode for all 4x4 blocks within the PU at (x0, y0) of given size.
func (m *intraModeMap) set(x0, y0, puSize, mode int) {
	for y := y0 / 4; y < (y0+puSize)/4; y++ {
		for x := x0 / 4; x < (x0+puSize)/4; x++ {
			m.modes[y*m.width4+x] = mode
		}
	}
}

// get returns the intra mode at pixel position (x, y), or -1 if unavailable.
func (m *intraModeMap) get(x, y int) int {
	bx, by := x/4, y/4
	if bx < 0 || by < 0 || bx >= m.width4 || by*m.width4+bx >= len(m.modes) {
		return -1
	}
	return m.modes[by*m.width4+bx]
}

// deriveMPM builds the 3-element MPM list per HEVC spec 8.4.2 (Table 8-3).
func deriveMPM(candA, candB int) [3]int {
	// -1 means unavailable → default to DC (mode 1)
	if candA < 0 {
		candA = 1
	}
	if candB < 0 {
		candB = 1
	}

	if candA == candB {
		if candA < 2 {
			return [3]int{0, 1, 26} // Planar, DC, Angular26
		}
		return [3]int{
			candA,
			2 + ((candA - 2 + 29) % 32), // candA - 1 in angular range
			2 + ((candA - 2 + 1) % 32),  // candA + 1 in angular range
		}
	}

	// candA != candB
	var third int
	if candA != 0 && candB != 0 {
		third = 0 // Planar
	} else if candA != 1 && candB != 1 {
		third = 1 // DC
	} else {
		third = 26 // Angular 26
	}
	return [3]int{candA, candB, third}
}
