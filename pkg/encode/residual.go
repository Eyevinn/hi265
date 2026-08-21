package encode

import (
	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/transform"
)

// HM-compatible forward quantization scales (reciprocal of dequant scales).
// g_quantScales from HM: {26214, 23302, 20560, 18396, 16384, 14564}
var quantScales = [6]int{26214, 23302, 20560, 18396, 16384, 14564}

// forwardDCT performs the HEVC forward DCT on a block of residual samples.
// Uses HM-compatible shift values:
//
//	Column pass shift = log2N + bitDepth - 9 (for 8-bit: log2N - 1)
//	Row pass shift    = log2N + 6
func forwardDCT(residual []int32, size int) []int32 {
	coeffs := make([]int32, size*size)
	tmp := make([]int32, size*size)

	matrix := getDCTMatrix(size)
	if matrix == nil {
		return residual
	}

	log2N := 0
	for (1 << log2N) < size {
		log2N++
	}

	bitDepth := 8
	shift1 := log2N + bitDepth - 9 // column pass shift
	shift2 := log2N + 6            // row pass shift
	add1 := int32(1) << max(shift1-1, 0)
	add2 := int32(1) << (shift2 - 1)

	// Column pass (vertical)
	for c := range size {
		for i := range size {
			var sum int32
			for j := range size {
				sum += matrix[i][j] * residual[c+j*size]
			}
			if shift1 > 0 {
				tmp[c+i*size] = (sum + add1) >> shift1
			} else {
				tmp[c+i*size] = sum
			}
		}
	}

	// Row pass (horizontal)
	for y := range size {
		for i := range size {
			var sum int32
			for j := range size {
				sum += matrix[i][j] * tmp[y*size+j]
			}
			coeffs[y*size+i] = (sum + add2) >> shift2
		}
	}

	return coeffs
}

func getDCTMatrix(size int) [][]int32 {
	switch size {
	case 4:
		m := make([][]int32, 4)
		for i := range m {
			m[i] = transform.DCT4[i][:]
		}
		return m
	case 8:
		m := make([][]int32, 8)
		for i := range m {
			m[i] = transform.DCT8[i][:]
		}
		return m
	case 16:
		m := make([][]int32, 16)
		for i := range m {
			m[i] = transform.DCT16[i][:]
		}
		return m
	case 32:
		m := make([][]int32, 32)
		for i := range m {
			m[i] = transform.DCT32[i][:]
		}
		return m
	}
	return nil
}

// quantize performs HM-compatible forward quantization.
// Formula: level = sign(c) * ((abs(c) * quantScale + add) >> qBits)
// where qBits = QUANT_SHIFT + qp/6 + transformShift
//
//	QUANT_SHIFT = 14
//	transformShift = 15 - bitDepth - log2N = 7 - log2N (for 8-bit)
func quantize(coeffs []int32, size, qp int) []int32 {
	levels := make([]int32, len(coeffs))

	log2N := 0
	for (1 << log2N) < size {
		log2N++
	}

	qpPer := qp / 6
	qpRem := qp % 6
	qScale := quantScales[qpRem]

	transformShift := 15 - 8 - log2N // 7 - log2N for 8-bit
	qBits := 14 + qpPer + transformShift
	add := int64(171) << (qBits - 9) // I-slice dead zone: 171/512 ≈ 1/3

	for i, c := range coeffs {
		if c == 0 {
			continue
		}
		absC := int64(c)
		sign := int32(1)
		if absC < 0 {
			absC = -absC
			sign = -1
		}
		level := int32((absC*int64(qScale) + add) >> qBits)
		levels[i] = sign * level
	}
	return levels
}

// encodeResidualCoding encodes residual coefficients for one TU using CABAC.
func encodeResidualCoding(enc *cabac.Encoder, models []cabac.CtxState,
	levels []int32, log2TrafoSize int, isLuma bool, scanIdx int) {

	size := 1 << log2TrafoSize

	// Sub-block processing setup
	log2SbSize := 2
	if log2TrafoSize == 2 {
		log2SbSize = log2TrafoSize
	}
	numSbX := size >> log2SbSize
	numSbY := size >> log2SbSize
	sbSize := 1 << log2SbSize

	sbScanOrder := slice.ScanOrder(numSbX, numSbY, scanIdx)
	coeffScanOrder := slice.ScanOrder(sbSize, sbSize, scanIdx)

	// Find last significant coefficient using the HIERARCHICAL scan order
	// (sub-block scan × coefficient scan within sub-block). This must match
	// the decoder which processes sub-blocks 0..lastSubBlock in this order.
	lastX, lastY := -1, -1
	lastSubBlock := -1
	for i := len(sbScanOrder) - 1; i >= 0; i-- {
		sbX := sbScanOrder[i][0]
		sbY := sbScanOrder[i][1]
		sbOriginX := sbX * sbSize
		sbOriginY := sbY * sbSize
		for j := len(coeffScanOrder) - 1; j >= 0; j-- {
			cx := sbOriginX + coeffScanOrder[j][0]
			cy := sbOriginY + coeffScanOrder[j][1]
			if cx < size && cy < size && levels[cy*size+cx] != 0 {
				lastX = cx
				lastY = cy
				lastSubBlock = i
				goto foundLast
			}
		}
	}
foundLast:
	if lastX < 0 {
		return
	}
	// Encode last_sig_coeff_x_prefix/suffix and y_prefix/suffix.
	// Spec 7.3.8.11: for the vertical scan the coded x and y are swapped (the
	// decoder swaps them back after reading), so that one set of context models
	// serves both scan directions. Without the swap encoder and decoder stay
	// self-consistent but every conforming decoder disagrees.
	encLastX, encLastY := lastX, lastY
	if scanIdx == 2 {
		encLastX, encLastY = lastY, lastX
	}
	encodeLastSigCoeff(enc, models, encLastX, encLastY, log2TrafoSize, isLuma)

	// coded_sub_block_flag tracking
	codedSbFlags := make([][]bool, numSbY)
	for i := range codedSbFlags {
		codedSbFlags[i] = make([]bool, numSbX)
	}

	// lastC1 tracks the c1 state from the previous sub-block for ctxSet derivation.
	// Matches decoder: c1=0 means previous sub-block had greater1_flag=1.
	lastC1 := 1

	// Process sub-blocks in reverse scan order
	for i := lastSubBlock; i >= 0; i-- {
		sbX := sbScanOrder[i][0]
		sbY := sbScanOrder[i][1]
		sbOriginX := sbX * sbSize
		sbOriginY := sbY * sbSize

		// Determine if this sub-block has any significant coefficients
		hasSig := false
		for _, pos := range coeffScanOrder {
			cx := sbOriginX + pos[0]
			cy := sbOriginY + pos[1]
			if cx < size && cy < size && levels[cy*size+cx] != 0 {
				hasSig = true
				break
			}
		}

		// coded_sub_block_flag — match decoder exactly
		if i > 0 && i < lastSubBlock {
			csbfRight := 0
			csbfBelow := 0
			if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
				csbfRight = 1
			}
			if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
				csbfBelow = 1
			}
			ctxInc := min(csbfRight+csbfBelow, 1) // decoder uses min(sum, 1)

			sbCtx := context.CtxCodedSubBlockFlag
			if !isLuma {
				sbCtx += 2
			}
			val := uint8(0)
			if hasSig {
				val = 1
			}
			enc.EncodeDecision(val, &models[sbCtx+ctxInc])
			codedSbFlags[sbY][sbX] = hasSig
		} else {
			// First and last sub-blocks: implicitly coded
			if i == lastSubBlock {
				codedSbFlags[sbY][sbX] = true
			} else {
				// i == 0: always true in decoder
				codedSbFlags[sbY][sbX] = true
			}
		}

		if !codedSbFlags[sbY][sbX] {
			continue
		}

		// Find the scan position of the last significant coeff within this sub-block
		numCoeffs := 1 << (2 * log2SbSize)
		startPos := numCoeffs - 1
		lastScanPosInSb := -1

		sigFlags := make([]bool, numCoeffs)

		if i == lastSubBlock {
			// Mark the last significant position
			localX := lastX - sbOriginX
			localY := lastY - sbOriginY
			for j, pos := range coeffScanOrder {
				if pos[0] == localX && pos[1] == localY {
					sigFlags[j] = true
					lastScanPosInSb = j
					startPos = j - 1
					break
				}
			}
		}

		// prevCsbf for sig_coeff_flag context
		prevCsbf := 0
		if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
			prevCsbf |= 1
		}
		if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
			prevCsbf |= 2
		}

		// implicitNonZeroCoeff: for middle sub-blocks, position 0 may be implicit
		implicitNonZeroCoeff := (i > 0 && i < lastSubBlock)
		firstScanPos := numCoeffs - 1
		if i == lastSubBlock && lastScanPosInSb >= 0 {
			firstScanPos = lastScanPosInSb
		}

		for j := startPos; j >= 0; j-- {
			if j > 0 || !implicitNonZeroCoeff {
				cx := coeffScanOrder[j][0]
				cy := coeffScanOrder[j][1]
				px := sbOriginX + cx
				py := sbOriginY + cy
				sig := px < size && py < size && levels[py*size+px] != 0

				ctxInc := slice.GetSigCtxInc(cx, cy, log2TrafoSize, isLuma, sbX, sbY, scanIdx, prevCsbf)
				val := uint8(0)
				if sig {
					val = 1
				}
				enc.EncodeDecision(val, &models[context.CtxSigCoeffFlag+ctxInc])
				sigFlags[j] = sig
				if sig {
					if lastScanPosInSb < 0 {
						lastScanPosInSb = j
					}
					firstScanPos = j
					implicitNonZeroCoeff = false
				}
			} else {
				// DC of coded sub-block is implicitly significant
				sigFlags[0] = true
				firstScanPos = 0
				if lastScanPosInSb < 0 {
					lastScanPosInSb = 0
				}
			}
		}

		if lastScanPosInSb < 0 {
			continue
		}

		// ctxSet derivation — must match decoder exactly
		ctxSet := 0
		if isLuma && i > 0 {
			ctxSet = 2
		}
		if lastC1 == 0 {
			ctxSet++
		}

		chromaOffset := 0
		if !isLuma {
			chromaOffset = 16
		}

		// coeff_abs_level_greater1_flag (up to 8)
		c1 := 1
		firstGreater1ScanPos := -1
		numGreater1InSb := 0

		// Collect which scan positions are significant, iterate in reverse
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			if numGreater1InSb < 8 {
				cx := sbOriginX + coeffScanOrder[n][0]
				cy := sbOriginY + coeffScanOrder[n][1]
				absLevel := levels[cy*size+cx]
				if absLevel < 0 {
					absLevel = -absLevel
				}
				greater1 := uint8(0)
				if absLevel > 1 {
					greater1 = 1
				}
				ctxIdx := context.CtxCoeffAbsLevelGreater1 + ctxSet*4 + c1 + chromaOffset
				enc.EncodeDecision(greater1, &models[ctxIdx])
				numGreater1InSb++
				if greater1 == 1 {
					if firstGreater1ScanPos < 0 {
						firstGreater1ScanPos = n
					}
					c1 = 0
				} else if c1 > 0 && c1 < 3 {
					c1++
				}
			}
		}

		// Update lastC1 for next sub-block
		lastC1 = c1

		// coeff_abs_level_greater2_flag (at most 1, at firstGreater1ScanPos)
		greater2Flag := false
		if firstGreater1ScanPos >= 0 {
			cx := sbOriginX + coeffScanOrder[firstGreater1ScanPos][0]
			cy := sbOriginY + coeffScanOrder[firstGreater1ScanPos][1]
			absLevel := levels[cy*size+cx]
			if absLevel < 0 {
				absLevel = -absLevel
			}
			greater2 := uint8(0)
			if absLevel > 2 {
				greater2 = 1
				greater2Flag = true
			}
			greater2Ctx := ctxSet
			if !isLuma {
				greater2Ctx += 4
			}
			enc.EncodeDecision(greater2, &models[context.CtxCoeffAbsLevelGreater2+greater2Ctx])
		}

		// Sign flags (bypass) — same order as decoder: lastScanPosInSb down to firstScanPos
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			cx := sbOriginX + coeffScanOrder[n][0]
			cy := sbOriginY + coeffScanOrder[n][1]
			lev := levels[cy*size+cx]
			sign := uint8(0)
			if lev < 0 {
				sign = 1
			}
			enc.EncodeBypass(sign)
		}

		// coeff_abs_level_remaining (bypass, TR + EGk) — same order as decoder
		cRiceParam := 0
		numGreater1Seen := 0
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			cx := sbOriginX + coeffScanOrder[n][0]
			cy := sbOriginY + coeffScanOrder[n][1]
			absLevel := levels[cy*size+cx]
			if absLevel < 0 {
				absLevel = -absLevel
			}

			// Compute baseLevel matching decoder logic
			baseLevel := int32(1)
			greater1Decoded := numGreater1Seen < 8
			numGreater1Seen++

			if greater1Decoded && absLevel > 1 {
				baseLevel = 2
			}
			if n == firstGreater1ScanPos && greater2Flag {
				baseLevel = 3
			}

			// Determine if remaining needs to be coded — match decoder's needRemaining
			needRemaining := !greater1Decoded || // bypassed (>=8 sig coeffs)
				(greater1Decoded && absLevel > 1 && n != firstGreater1ScanPos) || // greater1=1, not first
				(n == firstGreater1ScanPos && greater2Flag) // first greater1 with greater2=1

			if needRemaining {
				remaining := int(absLevel - baseLevel)
				encodeCoeffAbsLevelRemaining(enc, remaining, cRiceParam)
				// Update Rice parameter
				if absLevel > int32(3*(1<<cRiceParam)) && cRiceParam < 4 {
					cRiceParam++
				}
			}
		}
	}
}

// encodeLastSigCoeff encodes last_sig_coeff_x_prefix/suffix and y_prefix/suffix.
// Per HEVC spec 7.3.8.11: both prefixes first, then both suffixes.
// This order must match the decoder which reads X prefix, Y prefix, X suffix, Y suffix.
func encodeLastSigCoeff(enc *cabac.Encoder, models []cabac.CtxState,
	lastX, lastY, log2TrafoSize int, isLuma bool) {

	xPrefix, xSuffixVal, xSuffixLen := lastPosToPrefix(lastX)
	yPrefix, ySuffixVal, ySuffixLen := lastPosToPrefix(lastY)

	// Encode both prefixes first (context-coded)
	encodeLastSigCoeffPrefix(enc, models, xPrefix, log2TrafoSize, isLuma, context.CtxLastSigCoeffXPrefix)
	encodeLastSigCoeffPrefix(enc, models, yPrefix, log2TrafoSize, isLuma, context.CtxLastSigCoeffYPrefix)

	// Then encode both suffixes (bypass-coded)
	if xSuffixLen > 0 {
		for k := xSuffixLen - 1; k >= 0; k-- {
			enc.EncodeBypass(uint8((xSuffixVal >> k) & 1))
		}
	}
	if ySuffixLen > 0 {
		for k := ySuffixLen - 1; k >= 0; k-- {
			enc.EncodeBypass(uint8((ySuffixVal >> k) & 1))
		}
	}
}

// lastPosToPrefix converts a last significant coefficient position to prefix + suffix.
// Matches the decoder's decodeLastSigCoeffSuffix inverse:
//
//	prefix 0..3: position = prefix (no suffix)
//	prefix > 3:  position = ((2 + (prefix & 1)) << suffixLen) + suffix
//	             where suffixLen = (prefix >> 1) - 1
func lastPosToPrefix(pos int) (prefix, suffixVal, suffixLen int) {
	if pos < 4 {
		return pos, 0, 0
	}
	// Binary search: find prefix such that the range covers pos
	// Range for prefix p (p>3): start = ((2 + (p&1)) << ((p>>1)-1)), size = 1 << ((p>>1)-1)
	prefix = 4
	for {
		sl := (prefix >> 1) - 1
		start := (2 + (prefix & 1)) << sl
		size := 1 << sl
		if pos >= start && pos < start+size {
			return prefix, pos - start, sl
		}
		prefix++
	}
}

// encodeLastSigCoeffPrefix encodes only the prefix part (context-coded truncated unary).
func encodeLastSigCoeffPrefix(enc *cabac.Encoder, models []cabac.CtxState,
	prefix, log2TrafoSize int, isLuma bool, ctxBase int) {

	// Context offset and shift — must match decoder's decodeLastSigCoeffPrefix exactly
	var ctxOffset, ctxShift int
	if isLuma {
		ctxOffset = 3*(log2TrafoSize-2) + ((log2TrafoSize - 1) >> 2)
		ctxShift = (log2TrafoSize + 1) >> 2
	} else {
		ctxOffset = 15
		ctxShift = log2TrafoSize - 2
		if ctxShift < 0 {
			ctxShift = 0
		}
	}

	// Encode prefix as truncated unary — match decoder's maxPrefix
	maxPrefix := (log2TrafoSize << 1) - 1
	for k := range maxPrefix {
		ctxInc := k >> ctxShift
		if k < prefix {
			enc.EncodeDecision(1, &models[ctxBase+ctxOffset+ctxInc])
		} else {
			enc.EncodeDecision(0, &models[ctxBase+ctxOffset+ctxInc])
			break
		}
	}
}

// encodeCoeffAbsLevelRemaining encodes coeff_abs_level_remaining using
// Truncated Rice + Exp-Golomb coding. Must match the decoder's decodeAbsLevelRemaining:
//
//	prefix < 3: value = prefix << cRiceParam + suffix (cRiceParam bits)
//	prefix >= 3: value = ((1<<(prefix-3))+2) << cRiceParam + suffix (prefix-3+cRiceParam bits)
func encodeCoeffAbsLevelRemaining(enc *cabac.Encoder, level, cRiceParam int) {
	cMax := 3 // TR threshold in prefix units
	threshold := cMax << cRiceParam

	if level < threshold {
		// Truncated Rice: unary prefix + cRiceParam suffix bits
		prefix := level >> cRiceParam
		for range prefix {
			enc.EncodeBypass(1)
		}
		enc.EncodeBypass(0)
		// suffix = cRiceParam low bits of level
		for k := cRiceParam - 1; k >= 0; k-- {
			enc.EncodeBypass(uint8((level >> k) & 1))
		}
	} else {
		// Exp-Golomb: find prefix p >= 3 such that value is in the range for p
		// Range for prefix p: base = ((1<<(p-3))+2) << cRiceParam, size = 1<<(p-3+cRiceParam)
		prefix := cMax
		for {
			base := ((1 << (prefix - cMax)) + cMax - 1) << cRiceParam
			size := 1 << (prefix - cMax + cRiceParam)
			if level < base+size {
				suffix := level - base
				suffixLen := prefix - cMax + cRiceParam
				// Write prefix ones + stop 0
				for range prefix {
					enc.EncodeBypass(1)
				}
				enc.EncodeBypass(0)
				// Write suffixLen suffix bits
				for k := suffixLen - 1; k >= 0; k-- {
					enc.EncodeBypass(uint8((suffix >> k) & 1))
				}
				return
			}
			prefix++
		}
	}
}
