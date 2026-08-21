package encode

import (
	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/pred"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/frame"
	"github.com/Eyevinn/mp4ff/hevc"
)

// idrSliceParams holds the parameters needed to write an IRAP (IDR or CRA)
// slice header when using external SPS/PPS.
type idrSliceParams struct {
	width                           int
	height                          int
	qp                              int
	ppsID                           uint32
	numExtraSliceHeaderBits         uint8
	outputFlagPresent               bool
	saoEnabled                      bool
	deblockingFilterControlPresent  bool
	deblockingFilterOverrideEnabled bool
	deblockingFilterDisabled        bool
	loopFilterAcrossSlicesEnabled   bool

	// refPicSet is nil for an IDR slice and non-nil for a CRA slice, which
	// carries the POC and reference picture set fields an IDR header omits.
	refPicSet *pocRefPicSetParams
}

// pocRefPicSetParams holds the slice header fields that every picture except an
// IDR carries right after pic_output_flag (spec 7.3.6.1): the picture order
// count LSB, the short-term and long-term reference picture sets, and the
// slice-level temporal MVP flag.
type pocRefPicSetParams struct {
	poc                    int
	log2MaxPicOrderCntLsb  int  // log2_max_pic_order_cnt_lsb_minus4 + 4
	numShortTermRefPicSets int  // sps.NumShortTermRefPicSets
	numLongTermRefPics     int  // sps.NumLongTermRefPics (num_long_term_ref_pics_sps)
	longTermRefPicsPresent bool // sps.LongTermRefPicsPresentFlag
	spsTemporalMvpEnabled  bool // sps.SpsTemporalMvpEnabledFlag

	// intraRefresh marks a picture that references nothing (a CRA): an empty
	// inline short-term RPS is written and temporal MVP is switched off.
	intraRefresh bool
}

// craRefPicSetParams derives the CRA-specific slice header fields from the SPS.
func craRefPicSetParams(sps *hevc.SPS, poc int) pocRefPicSetParams {
	return pocRefPicSetParams{
		poc:                    poc,
		log2MaxPicOrderCntLsb:  int(sps.Log2MaxPicOrderCntLsbMinus4) + 4,
		numShortTermRefPicSets: int(sps.NumShortTermRefPicSets),
		numLongTermRefPics:     int(sps.NumLongTermRefPics),
		longTermRefPicsPresent: sps.LongTermRefPicsPresentFlag,
		spsTemporalMvpEnabled:  sps.SpsTemporalMvpEnabledFlag,
		intraRefresh:           true,
	}
}

// writePOCAndRefPicSets writes slice_pic_order_cnt_lsb, the short-term and
// long-term reference picture sets and slice_temporal_mvp_enabled_flag
// (spec 7.3.6.1). Only POC LSBs are coded, so the caller's POC is masked to
// log2MaxPicOrderCntLsb bits; the decoder derives the MSBs from prevTid0Pic,
// which is what lets a CRA splice into a running stream without resetting POC.
func writePOCAndRefPicSets(w *BitWriter, p pocRefPicSetParams) {
	pocBits := p.log2MaxPicOrderCntLsb
	pocMask := (1 << pocBits) - 1
	w.WriteBits(uint32(p.poc&pocMask), pocBits)

	switch {
	case p.intraRefresh:
		// An intra refresh picture uses no reference pictures, so an empty RPS
		// is written inline rather than pointing at one of the SPS sets.
		w.WriteBit(0) // short_term_ref_pic_set_sps_flag = 0
		if p.numShortTermRefPicSets > 0 {
			// Inline RPS in a slice header has stRpsIdx = num_short_term_ref_pic_sets,
			// and inter_ref_pic_set_prediction_flag is coded when stRpsIdx > 0.
			w.WriteBit(0) // inter_ref_pic_set_prediction_flag = 0
		}
		w.WriteUE(0) // num_negative_pics = 0
		w.WriteUE(0) // num_positive_pics = 0

	case p.numShortTermRefPicSets > 0:
		// short_term_ref_pic_set_sps_flag = 1, use SPS index 0
		w.WriteBit(1)
		if p.numShortTermRefPicSets > 1 {
			// short_term_ref_pic_set_idx: u(ceil(log2(numShortTermRefPicSets)))
			bits := ceilLog2(p.numShortTermRefPicSets)
			w.WriteBits(0, bits) // index 0
		}

	default:
		// short_term_ref_pic_set_sps_flag = 0 (inline)
		// Inline STRPS: stRpsIdx=0, no inter_ref_pic_set_prediction_flag
		w.WriteBit(0)
		w.WriteUE(1)  // num_negative_pics = 1
		w.WriteUE(0)  // num_positive_pics = 0
		w.WriteUE(0)  // delta_poc_s0_minus1 = 0 (deltaPOC = -1)
		w.WriteBit(1) // used_by_curr_pic_s0_flag = 1
	}

	// Long-term reference pictures are never used.
	if p.longTermRefPicsPresent {
		if p.numLongTermRefPics > 0 {
			w.WriteUE(0) // num_long_term_sps = 0
		}
		w.WriteUE(0) // num_long_term_pics = 0
	}

	// slice_temporal_mvp_enabled_flag. Spec 7.4.7.1 requires it to be 0 when
	// the current picture is a CRA or BLA picture.
	if p.spsTemporalMvpEnabled {
		if p.intraRefresh {
			w.WriteBit(0)
		} else {
			w.WriteBit(1)
		}
	}
}

func idrSliceParamsFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS) idrSliceParams {
	qp := 26 + int(pps.InitQpMinus26) // slice_qp_delta = 0
	return idrSliceParams{
		width:                           int(sps.PicWidthInLumaSamples),
		height:                          int(sps.PicHeightInLumaSamples),
		qp:                              qp,
		ppsID:                           pps.PicParameterSetID,
		numExtraSliceHeaderBits:         pps.NumExtraSliceHeaderBits,
		outputFlagPresent:               pps.OutputFlagPresentFlag,
		saoEnabled:                      sps.SampleAdaptiveOffsetEnabledFlag,
		deblockingFilterControlPresent:  pps.DeblockingFilterControlPresentFlag,
		deblockingFilterOverrideEnabled: pps.DeblockingFilterOverrideEnabledFlag,
		deblockingFilterDisabled:        pps.DeblockingFilterDisabledFlag,
		loopFilterAcrossSlicesEnabled:   pps.LoopFilterAcrossSlicesEnabledFlag,
	}
}

// encodeIDRSliceWithParams generates an IRAP slice RBSP respecting all header fields
// from external SPS/PPS parameters. It writes an IDR header, or a CRA header when
// p.refPicSet is set.
func encodeIDRSliceWithParams(p idrSliceParams, y, cb, cr []uint8) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(1)      // first_slice_segment_in_pic_flag = 1
	w.WriteBit(0)      // no_output_of_prior_pics_flag = 0 (present for all IRAP)
	w.WriteUE(p.ppsID) // slice_pic_parameter_set_id

	// num_extra_slice_header_bits: skip N zero bits
	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}

	w.WriteUE(2) // slice_type = 2 (I-slice)

	// pic_output_flag (only if output_flag_present_flag)
	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set.
	// CRA: POC lsb, reference picture sets and temporal MVP flag are present.
	if p.refPicSet != nil {
		writePOCAndRefPicSets(w, *p.refPicSet)
	}

	// SAO flags
	if p.saoEnabled {
		w.WriteBit(0) // slice_sao_luma_flag = 0
		w.WriteBit(0) // slice_sao_chroma_flag = 0
	}

	// slice_qp_delta = 0
	w.WriteSE(0)

	// Deblocking filter syntax
	if p.deblockingFilterControlPresent {
		if p.deblockingFilterOverrideEnabled {
			w.WriteBit(0) // deblocking_filter_override_flag = 0
		}
		// Uses PPS default deblocking state (no per-slice override)
	}

	// slice_loop_filter_across_slices_enabled_flag
	deblockActive := !p.deblockingFilterDisabled
	if p.loopFilterAcrossSlicesEnabled && (p.saoEnabled || deblockActive) {
		w.WriteBit(1) // slice_loop_filter_across_slices_enabled_flag = PPS default (1)
	}

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	headerBytes := w.Bytes()

	// === Slice data (CABAC) ===
	// IDR encoding only supports 16x16 CUs (CTU = minCb = 16)
	cabacBytes := encodeIDRSliceData(p.width, p.height, p.qp, y, cb, cr)

	result := make([]byte, 0, len(headerBytes)+len(cabacBytes))
	result = append(result, headerBytes...)
	result = append(result, cabacBytes...)
	return result
}

// encodeIDRSlice generates the IDR slice RBSP (header + CABAC data).
func encodeIDRSlice(width, height, qp int, y, cb, cr []uint8) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(1) // first_slice_segment_in_pic_flag = 1
	w.WriteBit(0) // no_output_of_prior_pics_flag = 0 (IDR only)
	w.WriteUE(0)  // slice_pic_parameter_set_id = 0
	w.WriteUE(2)  // slice_type = 2 (I-slice)
	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set
	// SAO disabled in SPS → no SAO flags
	w.WriteSE(0) // slice_qp_delta = 0 (PPS init_qp_minus26 = qp-26)
	// Deblocking disabled in PPS → no deblock syntax
	// loop_filter_across_slices_enabled_flag: not present (PPS flag is 0 and deblock is disabled)

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	headerBytes := w.Bytes()

	// === Slice data (CABAC) ===
	cabacBytes := encodeIDRSliceData(width, height, qp, y, cb, cr)

	result := make([]byte, 0, len(headerBytes)+len(cabacBytes))
	result = append(result, headerBytes...)
	result = append(result, cabacBytes...)
	return result
}

// encodeIDRSliceData encodes the CABAC slice data for an IDR I-slice.
// ctuSize must be 16 (the only supported CU size for IDR encoding).
func encodeIDRSliceData(width, height, qp int, y, cb, cr []uint8) []byte {
	enc := cabac.NewEncoder()
	models := context.InitModels(context.SliceTypeI, qp)

	ctuSize := 16
	numCTUx := (width + ctuSize - 1) / ctuSize
	numCTUy := (height + ctuSize - 1) / ctuSize
	totalCTUs := numCTUx * numCTUy

	reconFrame := frame.NewFrame(width, height)
	lumaModesMap := make(map[[2]int]int)

	ctuIdx := 0
	for ctuY := 0; ctuY < height; ctuY += ctuSize {
		for ctuX := 0; ctuX < width; ctuX += ctuSize {
			encodeIDRCU(enc, models, ctuX, ctuY, ctuSize, width, height, qp,
				y, cb, cr, reconFrame, lumaModesMap)

			// end_of_slice_segment_flag
			ctuIdx++
			if ctuIdx == totalCTUs {
				enc.EncodeTerminate(1)
			} else {
				enc.EncodeTerminate(0)
			}
		}
	}

	return enc.Flush()
}

// encodeIDRCU encodes a single 16x16 intra CU.
func encodeIDRCU(enc *cabac.Encoder, models []cabac.CtxState,
	ctuX, ctuY, ctuSize, width, height, qp int,
	y, cb, cr []uint8, reconFrame *frame.Frame, lumaModesMap map[[2]int]int) {

	// No split_cu_flag (CTU = minCbSize = 16, implicitly no split)
	// No pred_mode_flag (I-slice, implicitly intra)

	// part_mode: decoded when log2CbSize == log2MinCbSize
	// bin=1 → 2Nx2N, bin=0 → NxN
	enc.EncodeDecision(1, &models[context.CtxPartMode])

	// Choose the best intra prediction mode for this flat-color block.
	// For flat 16x16 blocks, vertical (26) or horizontal (10) can produce
	// zero residual when the block color matches the top or left neighbor,
	// while DC (1) averages both neighbors and also introduces AC energy
	// through edge filtering at color boundaries.
	lumaMode := chooseBestLumaMode(reconFrame, ctuX, ctuY, ctuSize, width, y)

	mpm := deriveMPM(ctuX, ctuY, ctuSize, width, ceilLog2(ctuSize), lumaModesMap)

	// Encode intra luma mode
	mpmIdx := -1
	for i, m := range mpm {
		if m == lumaMode {
			mpmIdx = i
			break
		}
	}

	if mpmIdx >= 0 {
		// prev_intra_luma_pred_flag = 1
		enc.EncodeDecision(1, &models[context.CtxPrevIntraLumaPredFlag])
		// mpm_idx: TU bypass bins (0, 10, 11)
		switch mpmIdx {
		case 0:
			enc.EncodeBypass(0)
		case 1:
			enc.EncodeBypass(1)
			enc.EncodeBypass(0)
		case 2:
			enc.EncodeBypass(1)
			enc.EncodeBypass(1)
		}
	} else {
		// prev_intra_luma_pred_flag = 0
		enc.EncodeDecision(0, &models[context.CtxPrevIntraLumaPredFlag])
		// rem_intra_luma_pred_mode: 5 bypass bins
		rem := computeRemIntraLumaPredMode(lumaMode, mpm)
		for k := 4; k >= 0; k-- {
			enc.EncodeBypass(uint8((rem >> k) & 1))
		}
	}
	lumaModesMap[[2]int{ctuX / ctuSize, ctuY / ctuSize}] = lumaMode

	// intra_chroma_pred_mode = 4 (DM mode)
	// First bin = 0 means "use DM mode"
	enc.EncodeDecision(0, &models[context.CtxIntraChromaPredMode])

	// Transform tree (no split, TU = CU = 16x16)
	// cbf_cb at depth 0
	cbfCb := hasNonZeroChroma(ctuX, ctuY, ctuSize, width, cb, qp, lumaMode, reconFrame, 0)
	encBool(enc, &models[context.CtxCbfCb], cbfCb)

	// cbf_cr at depth 0
	cbfCr := hasNonZeroChroma(ctuX, ctuY, ctuSize, width, cr, qp, lumaMode, reconFrame, 1)
	encBool(enc, &models[context.CtxCbfCr], cbfCr)

	// Compute luma residual
	lumaResidual, lumaLevels, cbfLuma := computeLumaResidual(
		ctuX, ctuY, ctuSize, width, y, qp, lumaMode, reconFrame)

	// cbf_luma at depth 0 (context = !trafoDepth = 1, so ctxIdx = CtxCbfLuma + 1)
	encBool(enc, &models[context.CtxCbfLuma+1], cbfLuma)

	// Encode residual if any cbf is set
	if cbfLuma {
		encodeResidualCoding(enc, models, lumaLevels, 4, true, 0)
	}

	// Reconstruct luma
	reconstructLuma(reconFrame, ctuX, ctuY, ctuSize, width, height,
		lumaMode, lumaResidual, cbfLuma, lumaLevels, qp)

	// Encode and reconstruct chroma
	chromaTrSize := ctuSize / 2
	chromaMode := lumaMode // DM mode

	for comp := range 2 {
		var chromaSrc []uint8
		if comp == 0 {
			chromaSrc = cb
		} else {
			chromaSrc = cr
		}
		hasCbf := (comp == 0 && cbfCb) || (comp == 1 && cbfCr)

		if hasCbf {
			chromaLevels := computeChromaLevels(ctuX, ctuY, ctuSize, width,
				chromaSrc, qp, chromaMode, reconFrame, comp)
			encodeResidualCoding(enc, models, chromaLevels, 3, false, 0) // log2(8)=3
			reconstructChroma(reconFrame, comp, ctuX/2, ctuY/2, chromaTrSize,
				width/2, height/2, chromaMode, chromaLevels, qp)
		} else {
			reconstructChroma(reconFrame, comp, ctuX/2, ctuY/2, chromaTrSize,
				width/2, height/2, chromaMode, nil, qp)
		}
	}
}

func encBool(enc *cabac.Encoder, ctx *cabac.CtxState, val bool) {
	v := uint8(0)
	if val {
		v = 1
	}
	enc.EncodeDecision(v, ctx)
}

// deriveMPM computes the Most Probable Modes for the CU at (x0,y0).
//
// log2CtbSize is the CTB size of the picture. Intra mode prediction does not
// cross a horizontal CTB boundary: per spec 8.4.2 the above candidate is
// INTRA_DC whenever y0-1 falls outside the current CTB. Omitting that rule
// keeps encoder and decoder self-consistent but makes the bitstream decode
// differently in every conforming decoder.
func deriveMPM(x0, y0, cuSize, picWidth, log2CtbSize int, modeMap map[[2]int]int) [3]int {
	cuX := x0 / cuSize
	cuY := y0 / cuSize

	leftMode := -1
	if x0 > 0 {
		if m, ok := modeMap[[2]int{cuX - 1, cuY}]; ok {
			leftMode = m
		}
	}

	aboveMode := -1
	if y0 > 0 && y0-1 >= (y0>>log2CtbSize)<<log2CtbSize {
		if m, ok := modeMap[[2]int{cuX, cuY - 1}]; ok {
			aboveMode = m
		}
	}

	// HEVC spec 8.4.2
	if leftMode < 0 {
		leftMode = 1 // DC
	}
	if aboveMode < 0 {
		aboveMode = 1 // DC
	}

	var mpm [3]int
	if leftMode == aboveMode {
		if leftMode < 2 {
			mpm = [3]int{0, 1, 26}
		} else {
			mpm[0] = leftMode
			mpm[1] = 2 + ((leftMode + 29) % 32)
			mpm[2] = 2 + ((leftMode - 2 + 1) % 32)
		}
	} else {
		mpm[0] = leftMode
		mpm[1] = aboveMode
		if leftMode != 0 && aboveMode != 0 {
			mpm[2] = 0
		} else if leftMode != 1 && aboveMode != 1 {
			mpm[2] = 1
		} else {
			mpm[2] = 26
		}
	}
	return mpm
}

// computeRemIntraLumaPredMode computes rem_intra_luma_pred_mode from the
// actual mode and MPM list (mode is not in MPM).
func computeRemIntraLumaPredMode(mode int, mpm [3]int) int {
	// Sort MPM in ascending order
	sorted := sortMPM(mpm)
	rem := mode
	for _, m := range sorted {
		if rem >= m {
			rem--
		}
	}
	return rem
}

func sortMPM(mpm [3]int) [3]int {
	s := mpm
	if s[0] > s[1] {
		s[0], s[1] = s[1], s[0]
	}
	if s[1] > s[2] {
		s[1], s[2] = s[2], s[1]
	}
	if s[0] > s[1] {
		s[0], s[1] = s[1], s[0]
	}
	return s
}

// hasNonZeroChroma checks if the chroma residual has non-zero quantized coefficients.
func hasNonZeroChroma(ctuX, ctuY, ctuSize, picWidth int, chromaSrc []uint8,
	qp, lumaMode int, recon *frame.Frame, comp int) bool {

	chromaTrSize := ctuSize / 2
	chromaQP := chromaQPFromLumaQP(qp)
	cx := ctuX / 2
	cy := ctuY / 2

	// Get prediction
	chromaPred := predictChromaBlock(recon, comp, cx, cy, chromaTrSize, lumaMode)

	// Compute residual
	chromaW := picWidth / 2
	chromaH := picWidth / 2 // assumes square for now; picHeight/2 for non-square
	residual := make([]int32, chromaTrSize*chromaTrSize)
	for py := range chromaTrSize {
		for px := range chromaTrSize {
			sx := cx + px
			sy := cy + py
			if sx < chromaW && sy < chromaH {
				srcIdx := sy*chromaW + sx
				residual[py*chromaTrSize+px] = int32(chromaSrc[srcIdx]) - chromaPred[py*chromaTrSize+px]
			}
		}
	}

	// Forward transform + quantize
	coeffs := forwardDCT(residual, chromaTrSize)
	levels := quantize(coeffs, chromaTrSize, chromaQP)

	for _, l := range levels {
		if l != 0 {
			return true
		}
	}
	return false
}

// computeLumaResidual computes the luma residual, transforms, and quantizes.
// Returns the prediction, quantized levels, and whether cbf_luma is set.
func computeLumaResidual(ctuX, ctuY, ctuSize, picWidth int, lumaSrc []uint8,
	qp, lumaMode int, recon *frame.Frame) (prediction []int32, levels []int32, cbfLuma bool) {

	// Get intra prediction for this block
	prediction = predictLumaBlock(recon, ctuX, ctuY, ctuSize, lumaMode)

	// Compute residual
	residual := make([]int32, ctuSize*ctuSize)
	for py := range ctuSize {
		for px := range ctuSize {
			sx := ctuX + px
			sy := ctuY + py
			if sx < picWidth {
				residual[py*ctuSize+px] = int32(lumaSrc[sy*picWidth+sx]) - prediction[py*ctuSize+px]
			}
		}
	}

	// Forward transform + quantize
	coeffs := forwardDCT(residual, ctuSize)
	levels = quantize(coeffs, ctuSize, qp)

	for _, l := range levels {
		if l != 0 {
			cbfLuma = true
			break
		}
	}
	return prediction, levels, cbfLuma
}

// computeChromaLevels computes quantized chroma levels for encoding.
func computeChromaLevels(ctuX, ctuY, ctuSize, picWidth int, chromaSrc []uint8,
	qp, chromaMode int, recon *frame.Frame, comp int) []int32 {

	chromaTrSize := ctuSize / 2
	chromaQP := chromaQPFromLumaQP(qp)
	cx := ctuX / 2
	cy := ctuY / 2

	chromaPred := predictChromaBlock(recon, comp, cx, cy, chromaTrSize, chromaMode)

	chromaW := picWidth / 2
	residual := make([]int32, chromaTrSize*chromaTrSize)
	for py := range chromaTrSize {
		for px := range chromaTrSize {
			sx := cx + px
			sy := cy + py
			if sx < chromaW {
				residual[py*chromaTrSize+px] = int32(chromaSrc[sy*chromaW+sx]) - chromaPred[py*chromaTrSize+px]
			}
		}
	}

	coeffs := forwardDCT(residual, chromaTrSize)
	return quantize(coeffs, chromaTrSize, chromaQP)
}

// predictLumaBlock generates DC intra prediction for a luma block.
func predictLumaBlock(f *frame.Frame, x0, y0, size, mode int) []int32 {
	ctuSize := 16 // our fixed CTU size
	neighbors := buildRefSamples(x0, y0, size, f.Width, f.Height, ctuSize,
		func(x, y int) uint8 { return f.GetLumaPixel(x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x, y) },
	)
	pred.FilterRefSamples(neighbors, mode, size, true, false)
	return predictIntra(mode, size, neighbors, 8, true)
}

// predictChromaBlock generates intra prediction for a chroma block.
func predictChromaBlock(f *frame.Frame, comp, x0, y0, size, mode int) []int32 {
	ctuSize := 8 // chroma CTU size for 4:2:0
	neighbors := buildRefSamples(x0, y0, size, f.Width/2, f.Height/2, ctuSize,
		func(x, y int) uint8 { return f.GetChromaPixel(comp, x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x*2, y*2) },
	)
	pred.FilterRefSamples(neighbors, mode, size, false, false)
	return predictIntra(mode, size, neighbors, 8, false)
}

// predictIntra performs intra prediction for a block.
func predictIntra(mode, size int, neighbors *pred.Neighbors, bitDepth int, isLuma bool) []int32 {
	switch mode {
	case 0:
		return pred.PredictPlanar(size, neighbors, bitDepth)
	case 1:
		return pred.PredictDC(size, neighbors, bitDepth, isLuma)
	default:
		return pred.PredictAngular(mode, size, neighbors, bitDepth, isLuma)
	}
}

// chooseBestLumaMode evaluates candidate intra prediction modes and returns
// the one with the smallest residual. For flat-color 16x16 blocks, vertical (26)
// gives zero residual when the block matches the top neighbor, horizontal (10)
// when it matches the left neighbor, and DC (1) when both match or neither does.
func chooseBestLumaMode(recon *frame.Frame, ctuX, ctuY, ctuSize, picWidth int, lumaSrc []uint8) int {
	candidates := []int{1, 10, 26} // DC, horizontal, vertical
	bestMode := 1
	bestCost := int64(1 << 62)

	for _, mode := range candidates {
		prediction := predictLumaBlock(recon, ctuX, ctuY, ctuSize, mode)

		var cost int64
		for py := range ctuSize {
			for px := range ctuSize {
				sx := ctuX + px
				sy := ctuY + py
				if sx < picWidth {
					diff := int64(lumaSrc[sy*picWidth+sx]) - int64(prediction[py*ctuSize+px])
					if diff < 0 {
						diff = -diff
					}
					cost += diff
				}
			}
		}

		if cost < bestCost {
			bestCost = cost
			bestMode = mode
		}
		if cost == 0 {
			break
		}
	}

	return bestMode
}

// buildRefSamples extracts and substitutes reference samples for intra prediction.
// Mirrors the decoder's buildRefSamples.
func buildRefSamples(x0, y0, size, picW, picH, ctbSize int,
	getPixel func(x, y int) uint8, isDecoded func(x, y int) bool) *pred.Neighbors {
	numCtbX := (picW + ctbSize - 1) / ctbSize
	curCtbX := x0 / ctbSize
	curCtbY := y0 / ctbSize
	curAddr := curCtbY*numCtbX + curCtbX

	isAvailable := func(px, py int) bool {
		if px < 0 || py < 0 || px >= picW || py >= picH {
			return false
		}
		nCtbX := px / ctbSize
		nCtbY := py / ctbSize
		nAddr := nCtbY*numCtbX + nCtbX
		if nAddr < curAddr {
			return true
		}
		if nAddr == curAddr {
			return isDecoded(px, py)
		}
		return false
	}

	hasAny := isAvailable(x0-1, y0) || isAvailable(x0, y0-1)
	if !hasAny {
		return nil
	}

	totalSamples := 4*size + 1
	ref := make([]uint8, totalSamples)
	avail := make([]bool, totalSamples)

	for i := range 2 * size {
		y := y0 + 2*size - 1 - i
		if isAvailable(x0-1, y) {
			ref[i] = getPixel(x0-1, y)
			avail[i] = true
		}
	}

	tlIdx := 2 * size
	if isAvailable(x0-1, y0-1) {
		ref[tlIdx] = getPixel(x0-1, y0-1)
		avail[tlIdx] = true
	}

	for i := range 2 * size {
		x := x0 + i
		if isAvailable(x, y0-1) {
			ref[tlIdx+1+i] = getPixel(x, y0-1)
			avail[tlIdx+1+i] = true
		}
	}

	firstAvail := -1
	for i := range totalSamples {
		if avail[i] {
			firstAvail = i
			break
		}
	}
	if firstAvail < 0 {
		return nil
	}

	for i := range firstAvail {
		ref[i] = ref[firstAvail]
	}
	for i := firstAvail + 1; i < totalSamples; i++ {
		if !avail[i] {
			ref[i] = ref[i-1]
		}
	}

	n := &pred.Neighbors{
		TopAvail:  true,
		LeftAvail: true,
		Top:       make([]uint8, 2*size),
		Left:      make([]uint8, 2*size),
	}

	for i := range 2 * size {
		n.Left[i] = ref[2*size-1-i]
	}
	n.TopLeft = ref[tlIdx]
	copy(n.Top, ref[tlIdx+1:])

	return n
}

// reconstructLuma reconstructs luma pixels after encoding.
func reconstructLuma(f *frame.Frame, x0, y0, size, picW, picH, mode int,
	prediction []int32, cbfLuma bool, levels []int32, qp int) {

	var residual []int32
	if cbfLuma {
		dequantCoeffs := transform.Dequantize(levels, size, qp)
		residual = transform.InverseDCT(dequantCoeffs, size)
	} else {
		residual = make([]int32, size*size)
	}

	recon := make([]int32, size*size)
	for i := range recon {
		recon[i] = prediction[i] + residual[i]
	}
	f.SetLumaBlock(x0, y0, size, recon)
}

// reconstructChroma reconstructs chroma pixels after encoding.
func reconstructChroma(f *frame.Frame, comp, cx, cy, chromaTrSize, chromaW, chromaH,
	mode int, levels []int32, qp int) {

	chromaQP := chromaQPFromLumaQP(qp)
	chromaPred := predictChromaBlock(f, comp, cx, cy, chromaTrSize, mode)

	var residual []int32
	if levels != nil {
		dequantCoeffs := transform.Dequantize(levels, chromaTrSize, chromaQP)
		residual = transform.InverseDCT(dequantCoeffs, chromaTrSize)
	} else {
		residual = make([]int32, chromaTrSize*chromaTrSize)
	}

	recon := make([]int32, chromaTrSize*chromaTrSize)
	for i := range recon {
		recon[i] = chromaPred[i] + residual[i]
	}
	f.SetChromaBlock(comp, cx, cy, chromaTrSize, recon)
}

// pSkipSliceParams holds the parameters needed to write a P-skip slice header.
type pSkipSliceParams struct {
	width                             int
	height                            int
	qp                                int
	poc                               int
	ppsID                             uint32
	numExtraSliceHeaderBits           uint8
	outputFlagPresent                 bool
	log2MaxPicOrderCntLsb             int // log2_max_pic_order_cnt_lsb_minus4 + 4
	numShortTermRefPicSets            int
	numLongTermRefPics                int // num_long_term_ref_pics_sps
	longTermRefPicsPresent            bool
	spsTemporalMvpEnabled             bool
	saoEnabled                        bool
	cabacInitPresent                  bool
	sliceChromaQpOffsetsPresent       bool
	deblockingFilterControlPresent    bool
	deblockingFilterOverrideEnabled   bool
	deblockingFilterDisabled          bool
	loopFilterAcrossSlicesEnabled     bool
	log2MinCodingBlockSizeMinus3      int
	log2DiffMaxMinLumaCodingBlockSize int
}

// encodePSkipSlice generates a P-skip slice RBSP (header + CABAC data).
// Uses the default encoder parameters: CTU=16, minCb=16.
func encodePSkipSlice(width, height, qp, poc int) []byte {
	return encodePSkipSliceWithParams(pSkipSliceParams{
		width:                             width,
		height:                            height,
		qp:                                qp,
		poc:                               poc,
		log2MaxPicOrderCntLsb:             4,
		log2MinCodingBlockSizeMinus3:      1, // minCb=16
		log2DiffMaxMinLumaCodingBlockSize: 0, // CTU=16
	})
}

// encodePSkipSliceWithParams generates a P-skip slice RBSP respecting all header fields.
func encodePSkipSliceWithParams(p pSkipSliceParams) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(1)      // first_slice_segment_in_pic_flag = 1
	w.WriteUE(p.ppsID) // slice_pic_parameter_set_id
	// num_extra_slice_header_bits: skip N zero bits
	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}
	w.WriteUE(1) // slice_type = 1 (P-slice)

	// pic_output_flag (only if output_flag_present_flag)
	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// pic_order_cnt_lsb, reference picture sets, slice_temporal_mvp_enabled_flag
	writePOCAndRefPicSets(w, pocRefPicSetParams{
		poc:                    p.poc,
		log2MaxPicOrderCntLsb:  p.log2MaxPicOrderCntLsb,
		numShortTermRefPicSets: p.numShortTermRefPicSets,
		numLongTermRefPics:     p.numLongTermRefPics,
		longTermRefPicsPresent: p.longTermRefPicsPresent,
		spsTemporalMvpEnabled:  p.spsTemporalMvpEnabled,
	})

	// SAO flags
	if p.saoEnabled {
		w.WriteBit(0) // slice_sao_luma_flag = 0
		w.WriteBit(0) // slice_sao_chroma_flag = 0
	}

	// num_ref_idx_active_override_flag = 0
	w.WriteBit(0)

	// cabac_init_flag (only if cabac_init_present_flag)
	if p.cabacInitPresent {
		w.WriteBit(0) // cabac_init_flag = 0
	}

	// five_minus_max_num_merge_cand = 4 (maxMergeCand = 1)
	w.WriteUE(4)

	// slice_qp_delta = 0
	w.WriteSE(0)

	// chroma QP offsets (only if pps_slice_chroma_qp_offsets_present_flag)
	if p.sliceChromaQpOffsetsPresent {
		w.WriteSE(0) // slice_cb_qp_offset = 0
		w.WriteSE(0) // slice_cr_qp_offset = 0
	}

	// Deblocking filter syntax
	if p.deblockingFilterControlPresent {
		if p.deblockingFilterOverrideEnabled {
			w.WriteBit(0) // deblocking_filter_override_flag = 0
		}
		// Uses PPS default deblocking state (no per-slice override)
	}

	// slice_loop_filter_across_slices_enabled_flag
	// Present when PPS has loop_filter_across_slices_enabled and
	// (SAO or deblocking is active in this slice).
	// With SAO flags=0 and no deblock override, deblocking uses PPS default.
	deblockActive := !p.deblockingFilterDisabled
	if p.loopFilterAcrossSlicesEnabled && (p.saoEnabled || deblockActive) {
		w.WriteBit(1) // slice_loop_filter_across_slices_enabled_flag = PPS default (1)
	}

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	headerBytes := w.Bytes()

	// === Slice data (CABAC) ===
	// Derive CTU and min CB sizes from SPS parameters
	log2MinCbSize := p.log2MinCodingBlockSizeMinus3 + 3
	ctuLog2 := log2MinCbSize + p.log2DiffMaxMinLumaCodingBlockSize
	ctuSize := 1 << ctuLog2
	cabacBytes := encodePSkipSliceData(p.width, p.height, p.qp, ctuSize, log2MinCbSize)

	result := make([]byte, 0, len(headerBytes)+len(cabacBytes))
	result = append(result, headerBytes...)
	result = append(result, cabacBytes...)
	return result
}

// encodePSkipSliceData encodes the CABAC slice data for a P-skip slice.
// log2MinCbSize is the minimum coding block size (log2). When ctuSize > minCbSize,
// split_cu_flag=0 must be written at each quadtree level before cu_skip_flag.
func encodePSkipSliceData(width, height, qp, ctuSize, log2MinCbSize int) []byte {
	enc := cabac.NewEncoder()
	models := context.InitModels(context.SliceTypeP, qp)
	numCTUx := (width + ctuSize - 1) / ctuSize
	numCTUy := (height + ctuSize - 1) / ctuSize
	totalCTUs := numCTUx * numCTUy

	log2CtuSize := 0
	for s := ctuSize; s > 1; s >>= 1 {
		log2CtuSize++
	}

	ctuIdx := 0
	for ctuY := 0; ctuY < height; ctuY += ctuSize {
		for ctuX := 0; ctuX < width; ctuX += ctuSize {
			// Write split_cu_flag=0 when CTU > minCB to signal no quadtree split.
			// Context for split_cu_flag: ctxInc = condL + condA.
			// For all-skip with no splits, neighbor depths are always 0 (CTU level),
			// same as current depth, so condL=condA=0, ctxInc=0.
			if log2CtuSize > log2MinCbSize {
				enc.EncodeDecision(0, &models[context.CtxSplitCuFlag]) // split_cu_flag = 0
			}

			// cu_skip_flag = 1
			// Context: ctxInc from left + above skip CU availability
			ctxInc := 0
			if ctuX > 0 {
				ctxInc++ // left available and is skip
			}
			if ctuY > 0 {
				ctxInc++ // above available and is skip
			}
			enc.EncodeDecision(1, &models[context.CtxCuSkipFlag+ctxInc])

			// merge_idx = 0: when maxMergeCand=1, no bins coded

			// end_of_slice_segment_flag
			ctuIdx++
			if ctuIdx == totalCTUs {
				enc.EncodeTerminate(1)
			} else {
				enc.EncodeTerminate(0)
			}
		}
	}

	return enc.Flush()
}

// ceilLog2 returns ceil(log2(n)) for n >= 1.
func ceilLog2(n int) int {
	bits := 0
	v := n - 1
	for v > 0 {
		v >>= 1
		bits++
	}
	return bits
}

func chromaQPFromLumaQP(qpY int) int {
	if qpY < 30 {
		return qpY
	}
	table := []int{
		29, 30, 31, 32, 32, 33, 34, 34, 35, 35,
		36, 36, 37, 37, 38, 39, 40, 41, 42, 43,
		44, 45, 46,
	}
	if qpY-30 < len(table) {
		return table[qpY-30]
	}
	return qpY - 6
}
