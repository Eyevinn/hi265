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

	segs, err := tileSegments(sps, pps)
	if err != nil {
		return nil, err
	}
	p := grayIDRSliceParams(sps, pps)

	// A tiled picture is one slice segment per tile, so a tiled parameter set
	// yields several NALUs. Concatenated they are still one Annex-B stream, and
	// one access unit.
	var buf bytes.Buffer
	for _, sg := range segs {
		p.seg = sg
		WriteNALU(&buf, naluIDRWRadl, encodeGrayIDRSlice(p))
	}
	return buf.Bytes(), nil
}

// EncodeGrayCRASliceFromSPSPPS encodes a gray CRA (Clean Random Access) slice
// compatible with external SPS/PPS at the given picture order count.
// It is the Gradual Decoder Refresh primitive: unlike an IDR, a CRA does not
// reset the picture order count, so a CRA carrying the right
// slice_pic_order_cnt_lsb splices a refresh point into a running stream without
// breaking POC continuity of the pictures that follow.
//
// Like the gray IDR, the content is uniform mid-gray on all planes and works for
// any chroma format (4:2:0, 4:2:2, 4:4:4) and bit depth (8, 10, 12), since DC
// prediction on a uniform surface always yields zero residual.
// Returns Annex-B framed CRA_NUT NALU.
func EncodeGrayCRASliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) ([]byte, error) {
	if err := validateGraySPSPPS(sps, pps); err != nil {
		return nil, err
	}

	segs, err := tileSegments(sps, pps)
	if err != nil {
		return nil, err
	}
	p := grayIDRSliceParams(sps, pps)
	rps := craRefPicSetParams(sps, poc)
	p.refPicSet = &rps

	var buf bytes.Buffer
	for _, sg := range segs {
		p.seg = sg
		WriteNALU(&buf, naluCRA, encodeGrayIDRSlice(p))
	}
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

	// refPicSet is nil for an IDR slice and non-nil for a CRA slice, which
	// carries the POC and reference picture set fields an IDR header omits.
	refPicSet *pocRefPicSetParams

	// seg says which slice segment of the picture this is, and which CTBs it
	// covers. Untiled pictures have exactly one.
	seg segment

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

func validateGraySPSPPS(sps *hevc.SPS, pps *hevc.PPS) error {
	// Tiles are supported, as one independent slice segment per tile, and so is
	// wavefront parallel processing, as one substream per CTB row of a single
	// segment. The two together are not: no HEVC profile permits the combination,
	// and the decoder refuses it as well.
	if pps.EntropyCodingSyncEnabledFlag && pps.TilesEnabledFlag {
		return fmt.Errorf("tiles combined with wavefront parallel processing is not supported")
	}
	// A gray picture codes no coefficients, so no chroma QP of any kind is used
	// and the chroma QP offsets — picture, slice or per-CU — cannot affect it, nor
	// can the scaling lists. Bit depth and chroma format cannot either: DC
	// prediction with no residual is mid-grey at any depth, which is the whole
	// point of this writer.
	//
	// What does reach it is syntax in the coding unit itself. Both of these are
	// bins that would have to be written for every CU, or the decoder reads one
	// that is not there (spec 7.3.8.5).
	if pps.TransquantBypassEnabledFlag {
		return fmt.Errorf("transquant_bypass_enabled_flag is not supported")
	}
	if sps.PCMEnabledFlag {
		return fmt.Errorf("pcm_enabled_flag is not supported")
	}
	return nil
}

// encodeGrayIDRSlice generates the complete IRAP slice RBSP for a gray frame.
// It writes an IDR header, or a CRA header when p.refPicSet is set.
func encodeGrayIDRSlice(p graySliceParams) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(p.seg.firstFlag()) // first_slice_segment_in_pic_flag
	w.WriteBit(0)                 // no_output_of_prior_pics_flag = 0 (all IRAP)
	w.WriteUE(p.ppsID)            // slice_pic_parameter_set_id
	p.seg.writeAddress(w)         // dependent flag and slice_segment_address

	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}

	w.WriteUE(2) // slice_type = 2 (I-slice)

	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set.
	// CRA: POC lsb, reference picture sets and temporal MVP flag are present.
	if p.refPicSet != nil {
		writePOCAndRefPicSets(w, *p.refPicSet)
	}

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

	// The slice data has to exist before the header can describe it: its
	// substream lengths are the entry point offsets.
	subs := encodeGraySliceData(p)
	p.seg.writeEntryPointOffsets(w, subs)

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	return appendSubstreams(w.Bytes(), subs)
}

// encodeGraySliceData encodes the CABAC slice data for a gray IDR frame, as the
// list of substreams it is made of.
// Every CU uses DC prediction (mode 1) with zero residual.
// For a uniform gray surface, DC prediction produces the exact value:
// - First CU: no neighbors → reference samples default to 1<<(bitDepth-1) = mid-gray
// - Subsequent CUs: neighbors are all mid-gray → DC pred = mid-gray
// Therefore cbf_luma = cbf_cb = cbf_cr = 0 for all CUs.
//
// Each CTU is encoded as the largest possible CU (no quadtree splitting) when it
// fits entirely within the picture. Boundary CTUs that extend past the picture edge
// use implicit splits down to sub-CUs that fit. This produces ~14x smaller bitstreams
// than splitting every CTU to minCB, since one large CU needs only one set of intra
// mode + cbf bins instead of 64 sets for a 64x64 CTU split to 8x8.
func encodeGraySliceData(p graySliceParams) [][]byte {
	// Only this segment's CTBs, in raster order within its tile. Every CU is DC
	// predicted with zero residual, so a neighbour in another tile being
	// unavailable changes nothing: DC with no neighbours is the mid-grey the
	// picture is made of anyway.
	//
	// That is also why no cu_qp_delta is written whatever the PPS says: every
	// transform unit has cbf_luma, cbf_cb and cbf_cr clear, and spec 7.3.8.10
	// codes the element only for a unit that carries a coefficient.
	return p.seg.encodeCtbs(p.ctuSize, context.SliceTypeI, p.qp,
		func(enc *cabac.Encoder, models []cabac.CtxState, x, y int) {
			encodeGrayCUTree(enc, models, x, y, p.ctuSize, p.log2CtuSize, p)
		})
}

// encodeGrayCUTree recursively processes the coding quadtree.
// Follows HEVC spec 7.3.8.4 coding_quadtree().
//
// For a uniform gray frame, we use the largest possible CU at each position:
// - If the CU fits in the picture: encode it directly (split_cu_flag = 0)
// - If it extends past the picture: implicit split, recurse into quadrants
//
// Context for split_cu_flag: ctxInc = condL + condA where condL/condA = 1 when
// the left/above neighbor CU depth > current depth. Since we always use the largest
// CU that fits, neighbor depths are never greater than the current depth, so ctxInc = 0.
func encodeGrayCUTree(enc *cabac.Encoder, models []cabac.CtxState,
	x0, y0, cuSize, log2CuSize int, p graySliceParams) {

	fitsInPicture := (x0+cuSize <= p.width) && (y0+cuSize <= p.height)

	if fitsInPicture {
		if log2CuSize > p.log2MinCbSize {
			// Code split_cu_flag = 0 (don't split — use largest possible CU)
			enc.EncodeDecision(0, &models[context.CtxSplitCuFlag])
		}
		encodeGrayCU(enc, models, log2CuSize, p)
		return
	}

	if log2CuSize == p.log2MinCbSize {
		// At minCbSize and doesn't fit — shouldn't happen for valid SPS
		encodeGrayCU(enc, models, log2CuSize, p)
		return
	}

	// CU extends beyond picture → implicit split (no flag coded)
	half := cuSize / 2
	for _, off := range [][2]int{{0, 0}, {half, 0}, {0, half}, {half, half}} {
		cx := x0 + off[0]
		cy := y0 + off[1]
		if cx < p.width && cy < p.height {
			encodeGrayCUTree(enc, models, cx, cy, half, log2CuSize-1, p)
		}
	}
}

// encodeGrayCU encodes a single CU for a gray frame at any size.
// DC prediction mode (1), zero residual for luma and both chroma components.
func encodeGrayCU(enc *cabac.Encoder, models []cabac.CtxState,
	log2CuSize int, p graySliceParams) {

	// part_mode: only coded when log2CbSize == MinCbLog2SizeY (spec 7.3.8.5).
	// For I-slices, PART_NxN is only allowed at minCB, so at larger sizes
	// part_mode is implicitly PART_2Nx2N.
	if log2CuSize == p.log2MinCbSize {
		enc.EncodeDecision(1, &models[context.CtxPartMode]) // part_mode = 2Nx2N
	}

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
