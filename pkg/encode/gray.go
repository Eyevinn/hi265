package encode

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/mp4ff/hevc"
)

// EncodeGrayIDRSliceFromSPSPPS encodes a gray IDR slice compatible with external SPS/PPS.
// A "gray" frame has uniform mid-gray value on all planes (128 for 8-bit, 512 for 10-bit).
// DC prediction produces exact gray for every CU, so residual is always zero.
// This makes the encoding independent of chroma format and bit depth.
// Returns Annex-B framed IDR_W_RADL NALU.
func EncodeGrayIDRSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS) ([]byte, error) {
	if err := validateGraySPSPPS(sps, pps); err != nil {
		return nil, err
	}

	p := grayIDRSliceParams(sps, pps)
	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeGrayIDRSlice(p))
	return buf.Bytes(), nil
}

// graySliceParams holds parameters for encoding a gray IDR slice.
type graySliceParams struct {
	width  int
	height int
	qp     int

	// Coding block sizes
	log2MinCbSize int // log2(minCbSize)
	log2CtuSize   int // log2(CTU size)
	ctuSize       int
	maxDepth      int // log2CtuSize - log2MinCbSize (number of quadtree levels)

	// Transform tree parameters
	log2MinTrafoSize                int
	log2MaxTrafoSize                int
	maxTransformHierarchyDepthIntra int

	// Chroma format
	chromaArrayType int // 0=mono, 1=4:2:0, 2=4:2:2, 3=4:4:4

	// Slice header fields from PPS/SPS
	ppsID                           uint32
	numExtraSliceHeaderBits         uint8
	outputFlagPresent               bool
	saoEnabled                      bool
	deblockingFilterControlPresent  bool
	deblockingFilterOverrideEnabled bool
	deblockingFilterDisabled        bool
	loopFilterAcrossSlicesEnabled   bool
}

func grayIDRSliceParams(sps *hevc.SPS, pps *hevc.PPS) graySliceParams {
	qp := 26 + int(pps.InitQpMinus26)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
	log2CtuSize := log2MinCbSize + int(sps.Log2DiffMaxMinLumaCodingBlockSize)
	ctuSize := 1 << log2CtuSize
	log2MinTrafoSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrafoSize := log2MinTrafoSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)

	chromaArrayType := int(sps.ChromaFormatIDC)
	if sps.SeparateColourPlaneFlag {
		chromaArrayType = 0
	}

	return graySliceParams{
		width:         int(sps.PicWidthInLumaSamples),
		height:        int(sps.PicHeightInLumaSamples),
		qp:            qp,
		log2MinCbSize: log2MinCbSize,
		log2CtuSize:   log2CtuSize,
		ctuSize:       ctuSize,
		maxDepth:      log2CtuSize - log2MinCbSize,

		log2MinTrafoSize:                log2MinTrafoSize,
		log2MaxTrafoSize:                log2MaxTrafoSize,
		maxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),

		chromaArrayType: chromaArrayType,

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

func validateGraySPSPPS(_ *hevc.SPS, pps *hevc.PPS) error {
	if pps.TilesEnabledFlag {
		return fmt.Errorf("tiles not supported")
	}
	if pps.DependentSliceSegmentsEnabledFlag {
		return fmt.Errorf("dependent slice segments not supported")
	}
	return nil
}

// encodeGrayIDRSlice generates the complete IDR slice RBSP for a gray frame.
func encodeGrayIDRSlice(p graySliceParams) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(1)      // first_slice_segment_in_pic_flag = 1
	w.WriteBit(0)      // no_output_of_prior_pics_flag = 0 (IDR only)
	w.WriteUE(p.ppsID) // slice_pic_parameter_set_id

	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}

	w.WriteUE(2) // slice_type = 2 (I-slice)

	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set

	if p.saoEnabled {
		w.WriteBit(0) // slice_sao_luma_flag = 0
		w.WriteBit(0) // slice_sao_chroma_flag = 0
	}

	w.WriteSE(0) // slice_qp_delta = 0

	if p.deblockingFilterControlPresent {
		if p.deblockingFilterOverrideEnabled {
			w.WriteBit(0) // deblocking_filter_override_flag = 0
		}
	}

	deblockActive := !p.deblockingFilterDisabled
	if p.loopFilterAcrossSlicesEnabled && (p.saoEnabled || deblockActive) {
		w.WriteBit(1) // slice_loop_filter_across_slices_enabled_flag
	}

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	headerBytes := w.Bytes()

	// === Slice data (CABAC) ===
	cabacBytes := encodeGraySliceData(p)

	result := make([]byte, 0, len(headerBytes)+len(cabacBytes))
	result = append(result, headerBytes...)
	result = append(result, cabacBytes...)
	return result
}

// encodeGraySliceData encodes CABAC slice data for a gray IDR frame.
// Every CU uses DC prediction (mode 1) with zero residual.
// For a uniform gray surface, DC prediction produces the exact value:
// - First CU: no neighbors → reference samples default to 1<<(bitDepth-1) = mid-gray
// - Subsequent CUs: neighbors are all mid-gray → DC pred = mid-gray
// Therefore cbf_luma = cbf_cb = cbf_cr = 0 for all CUs.
func encodeGraySliceData(p graySliceParams) []byte {
	enc := cabac.NewEncoder()
	models := context.InitModels(context.SliceTypeI, p.qp)

	numCTUx := (p.width + p.ctuSize - 1) / p.ctuSize
	numCTUy := (p.height + p.ctuSize - 1) / p.ctuSize
	totalCTUs := numCTUx * numCTUy

	ctuIdx := 0
	for ctuY := 0; ctuY < p.height; ctuY += p.ctuSize {
		for ctuX := 0; ctuX < p.width; ctuX += p.ctuSize {
			encodeGrayCUTree(enc, models, ctuX, ctuY, p.ctuSize, p.log2CtuSize, p)

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

// encodeGrayCUTree recursively processes the coding quadtree.
// Follows HEVC spec 7.3.8.4 coding_quadtree().
func encodeGrayCUTree(enc *cabac.Encoder, models []cabac.CtxState,
	x0, y0, cuSize, log2CuSize int, p graySliceParams) {

	// HEVC spec: split_cu_flag is only coded when:
	// 1. CU fits entirely within the picture
	// 2. log2CbSize > MinCbLog2SizeY
	fitsInPicture := (x0+cuSize <= p.width) && (y0+cuSize <= p.height)

	if log2CuSize == p.log2MinCbSize {
		// At minCbSize: no further split possible, encode the CU
		encodeGrayCU(enc, models, x0, y0, log2CuSize, p)
		return
	}

	if fitsInPicture {
		// Code split_cu_flag = 1 (always split to minCbSize)
		// Context: ctxInc = condL + condA
		// Since all CUs are split to maxDepth, neighbor depth > current depth when available.
		ctxInc := 0
		if x0 > 0 {
			ctxInc++
		}
		if y0 > 0 {
			ctxInc++
		}
		enc.EncodeDecision(1, &models[context.CtxSplitCuFlag+ctxInc])
	}
	// else: CU extends beyond picture → split is implicit (inferred as 1)

	half := cuSize / 2
	// Z-order: top-left, top-right, bottom-left, bottom-right
	// Only code children that start within the picture.
	for _, off := range [][2]int{{0, 0}, {half, 0}, {0, half}, {half, half}} {
		cx := x0 + off[0]
		cy := y0 + off[1]
		if cx < p.width && cy < p.height {
			encodeGrayCUTree(enc, models, cx, cy, half, log2CuSize-1, p)
		}
	}
}

// encodeGrayCU encodes a single CU at minCbSize for a gray frame.
// DC prediction mode (1), zero residual for luma and both chroma components.
func encodeGrayCU(enc *cabac.Encoder, models []cabac.CtxState,
	x0, y0, log2CuSize int, p graySliceParams) {

	// part_mode = 2Nx2N (single bin = 1)
	enc.EncodeDecision(1, &models[context.CtxPartMode])

	// Intra luma mode: DC (mode 1)
	// MPM derivation: for a uniform DC grid, both neighbors are DC (or default to DC).
	// When leftMode==aboveMode==DC(1) and DC < 2: MPM = {0, 1, 26}
	// DC is at mpmIdx=1.
	enc.EncodeDecision(1, &models[context.CtxPrevIntraLumaPredFlag]) // prev_intra_luma_pred_flag = 1
	enc.EncodeBypass(1)                                              // mpm_idx = 1 (10)
	enc.EncodeBypass(0)

	// intra_chroma_pred_mode = DM mode (mode 4: use luma mode)
	enc.EncodeDecision(0, &models[context.CtxIntraChromaPredMode])

	// Transform tree with zero residual
	encodeGrayTransformTree(enc, models, log2CuSize, 0, true, true, p)
}

// encodeGrayTransformTree encodes the transform tree for a gray CU (all cbf=0).
// Follows HEVC spec 7.3.8.9 transform_tree().
//
// For ChromaArrayType=2 (4:2:2), each TU has two chroma blocks (top/bottom halves),
// so cbf_cb/cbf_cr are coded twice: once in the standard block and once in the
// 4:2:2-specific second block.
func encodeGrayTransformTree(enc *cabac.Encoder, models []cabac.CtxState,
	log2TrafoSize, trafoDepth int, parentCbfCb, parentCbfCr bool, p graySliceParams) {

	// IntraSplitFlag = 0 (we always use part_mode = 2Nx2N)
	maxTrafoDepth := p.maxTransformHierarchyDepthIntra

	// Determine if split_transform_flag needs to be coded (spec 7.3.8.9)
	split := false
	canCodeSplit := false

	if log2TrafoSize > p.log2MaxTrafoSize {
		// TU larger than max → forced split (no flag coded)
		split = true
	} else if log2TrafoSize <= p.log2MaxTrafoSize &&
		log2TrafoSize > p.log2MinTrafoSize &&
		trafoDepth < maxTrafoDepth &&
		log2TrafoSize > 2 {
		// Code split_transform_flag
		canCodeSplit = true
	}
	// Otherwise: at min TU size or at max depth → implied no split

	if canCodeSplit {
		// split_transform_flag = 0 (no split for zero residual)
		ctxIdx := context.CtxSplitTransformFlag + (5 - log2TrafoSize)
		enc.EncodeDecision(0, &models[ctxIdx])
	}

	// cbf_cb, cbf_cr (first set): coded when ChromaArrayType in {1,2} and
	// (log2TrafoSize > 2 or ChromaArrayType != 1)
	// For 4:2:0 (type 1): only when log2TrafoSize > 2
	// For 4:2:2 (type 2): always (ChromaArrayType != 1 is true)
	if p.chromaArrayType == 1 || p.chromaArrayType == 2 {
		if log2TrafoSize > 2 || p.chromaArrayType != 1 {
			if trafoDepth == 0 || parentCbfCb {
				enc.EncodeDecision(0, &models[context.CtxCbfCb+trafoDepth])
			}
			if trafoDepth == 0 || parentCbfCr {
				enc.EncodeDecision(0, &models[context.CtxCbfCr+trafoDepth])
			}
		}
	}

	// cbf_cb, cbf_cr (second set for 4:2:2): coded when ChromaArrayType==2 and
	// (!split || log2TrafoSize==3)
	// This handles the second chroma block in 4:2:2 (vertical doubling).
	if p.chromaArrayType == 2 && (!split || log2TrafoSize == 3) {
		if trafoDepth == 0 || parentCbfCb {
			enc.EncodeDecision(0, &models[context.CtxCbfCb+trafoDepth])
		}
		if trafoDepth == 0 || parentCbfCr {
			enc.EncodeDecision(0, &models[context.CtxCbfCr+trafoDepth])
		}
	}

	if split {
		// Recurse into 4 children (parent cbf is always 0 for gray)
		for range 4 {
			encodeGrayTransformTree(enc, models, log2TrafoSize-1, trafoDepth+1, false, false, p)
		}
		return
	}

	// Leaf TU: code cbf_luma = 0
	// For I-slice (MODE_INTRA), cbf_luma is always coded.
	// Context: !trafoDepth (1 when trafoDepth==0, 0 otherwise)
	cbfLumaCtx := 0
	if trafoDepth == 0 {
		cbfLumaCtx = 1
	}
	enc.EncodeDecision(0, &models[context.CtxCbfLuma+cbfLumaCtx])
}
