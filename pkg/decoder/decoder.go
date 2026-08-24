// Package decoder implements the HEVC/H.265 video decoder.
package decoder

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/loopfilter"
	"github.com/Eyevinn/hi265/internal/pred"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/tiles"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// Decoder is the HEVC decoder.
type Decoder struct {
	vpsMap   map[uint32][]byte
	spsMap   map[uint32]*hevc.SPS
	ppsMap   map[uint32]*hevc.PPS
	refFrame *frame.Frame // reference frame for inter prediction
	pic      *picture     // picture being assembled from slice segments
}

// New creates a new HEVC decoder.
func New() *Decoder {
	return &Decoder{
		vpsMap: make(map[uint32][]byte),
		spsMap: make(map[uint32]*hevc.SPS),
		ppsMap: make(map[uint32]*hevc.PPS),
	}
}

// sliceSegment is one parsed slice segment header plus the CABAC payload that
// follows it. Picture assembly happens a segment at a time, so parsing a header
// and decoding what it describes are separate steps.
type sliceSegment struct {
	sps       *hevc.SPS
	pps       *hevc.PPS
	first     bool // first_slice_segment_in_pic_flag
	dependent bool // dependent_slice_segment_flag
	addrRS    int  // slice_segment_address, in raster scan
	params    slice.Params
	cabac     []byte
	intra     bool // an IRAP segment, which needs no reference frame

	// Loop filter parameters, which the spec allows to vary per slice.
	deblockingDisabled   bool
	betaOffset, tcOffset int
	saoEnabled           bool
	acrossSlices         bool // slice_loop_filter_across_slices_enabled_flag

	// chromaQPOffsets is pps_cb_qp_offset + slice_cb_qp_offset and the Cr pair,
	// which spec 8.6.1 adds to the luma QP before mapping it to a chroma QP.
	// Scaling a residual uses these; deblocking uses the picture-level pair on its
	// own (8.7.2.5.5), which picture keeps.
	chromaQPOffsets transform.ChromaQPOffsets
	// scaling is the quantization matrix set of spec 7.4.5, or nil when
	// scaling_list_enabled_flag is clear and every weight is a flat 16.
	scaling *transform.ScalingLists
}

// DecodeAnnexB decodes all frames from an Annex-B byte stream.
func (d *Decoder) DecodeAnnexB(data []byte) ([]*frame.Frame, error) {
	nalus := avc.ExtractNalusFromByteStream(data)
	return d.DecodeNALUs(nalus)
}

// DecodeNALUs decodes all frames from a list of NALUs (without start codes).
func (d *Decoder) DecodeNALUs(nalus [][]byte) ([]*frame.Frame, error) {
	var frames []*frame.Frame
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])

		switch naluType {
		case hevc.NALU_VPS:
			d.vpsMap[0] = nalu

		case hevc.NALU_SPS:
			sps, err := hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				return nil, fmt.Errorf("parse SPS: %w", err)
			}
			d.spsMap[uint32(sps.SpsID)] = sps

		case hevc.NALU_PPS:
			pps, err := hevc.ParsePPSNALUnit(nalu, d.spsMap)
			if err != nil {
				return nil, fmt.Errorf("parse PPS: %w", err)
			}
			d.ppsMap[pps.PicParameterSetID] = pps

		case hevc.NALU_IDR_N_LP, hevc.NALU_IDR_W_RADL:
			seg, err := d.parseIRAPSegment(nalu, false)
			if err != nil {
				return nil, err
			}
			if err := d.decodeSegment(seg, &frames); err != nil {
				return nil, err
			}

		case hevc.NALU_CRA:
			// A CRA is an intra picture like an IDR, but its header carries POC
			// and reference picture set fields. It becomes the new reference
			// frame for the P-skip pictures that follow.
			seg, err := d.parseIRAPSegment(nalu, true)
			if err != nil {
				return nil, err
			}
			if err := d.decodeSegment(seg, &frames); err != nil {
				return nil, err
			}

		case hevc.NALU_TRAIL_R, hevc.NALU_TRAIL_N:
			seg, err := d.parseTrailSegment(nalu)
			if err != nil {
				return nil, err
			}
			if err := d.decodeSegment(seg, &frames); err != nil {
				return nil, err
			}

		case hevc.NALU_SEI_PREFIX, hevc.NALU_SEI_SUFFIX:
			// Skip SEI

		case hevc.NALU_AUD:
			// Skip AUD

		default:
			// Skip unknown NALU types
		}
	}
	// The last picture of the stream has no following first-slice flag to close
	// it, so the end of the NAL list does.
	if err := d.finishPicture(&frames); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames decoded")
	}
	return frames, nil
}

// checkSupported refuses the coding tools this decoder does not implement, before
// a picture is built from them.
//
// The point is that every one of these fails silently otherwise, and several fail
// while still producing a picture that looks plausible. A per-CU flag nothing
// reads — cu_transquant_bypass_flag, pcm_flag — desynchronises the arithmetic
// decoder at the first CU that sets it, and the rest of the slice becomes noise
// that is nonetheless returned as a frame. A bit depth or chroma format nothing
// reads is worse: the samples are simply interpreted wrongly, and a 10-bit stream
// decoded as 8-bit comes out a few values off, which reads as a successful decode.
// A thumbnail that is subtly wrong is worse than no thumbnail.
func checkSupported(sps *hevc.SPS, pps *hevc.PPS) error {
	if d := 8 + int(sps.BitDepthLumaMinus8); d != 8 {
		return fmt.Errorf("bit depth %d is not supported, only 8", d)
	}
	if d := 8 + int(sps.BitDepthChromaMinus8); d != 8 {
		return fmt.Errorf("chroma bit depth %d is not supported, only 8", d)
	}
	if sps.ChromaFormatIDC != 1 {
		return fmt.Errorf("chroma_format_idc %d is not supported, only 1 (4:2:0)",
			sps.ChromaFormatIDC)
	}
	if sps.SeparateColourPlaneFlag {
		return fmt.Errorf("separate_colour_plane_flag is not supported")
	}
	if sps.PCMEnabledFlag {
		// pcm_flag is a per-CU bin, and a PCM CU carries raw samples after a byte
		// alignment that also restarts the arithmetic decoder.
		return fmt.Errorf("pcm_enabled_flag is not supported")
	}
	if pps.TransquantBypassEnabledFlag {
		// cu_transquant_bypass_flag is a per-CU bin, and a bypassed CU skips
		// scaling, the transform and both loop filters.
		return fmt.Errorf("transquant_bypass_enabled_flag is not supported")
	}
	if ext := pps.RangeExtension; ext != nil {
		if ext.ChromaQpOffsetListEnabledFlag {
			// cu_chroma_qp_offset_flag and cu_chroma_qp_offset_idx sit in the
			// transform unit (spec 7.3.8.10).
			return fmt.Errorf("chroma_qp_offset_list_enabled_flag is not supported")
		}
		if ext.CrossComponentPredictionEnabledFlag {
			return fmt.Errorf("cross_component_prediction_enabled_flag is not supported")
		}
	}
	if ext := sps.RangeExtension; ext != nil {
		// These four change how residual_coding is parsed or reconstructed, so a
		// stream using them desynchronises rather than merely losing quality.
		switch {
		case ext.PersistentRiceAdaptationEnabledFlag:
			return fmt.Errorf("persistent_rice_adaptation_enabled_flag is not supported")
		case ext.CabacBypassAlignmentEnabledFlag:
			return fmt.Errorf("cabac_bypass_alignment_enabled_flag is not supported")
		case ext.ImplicitRdpcmEnabledFlag:
			return fmt.Errorf("implicit_rdpcm_enabled_flag is not supported")
		case ext.ExplicitRdpcmEnabledFlag:
			return fmt.Errorf("explicit_rdpcm_enabled_flag is not supported")
		case ext.ExtendedPrecisionProcessingFlag:
			return fmt.Errorf("extended_precision_processing_flag is not supported")
		}
	}
	return nil
}

// scalingListsFor returns the quantization matrices a stream's parameter sets ask
// for (spec 7.4.5), or nil when scaling_list_enabled_flag is clear and every
// weight is the flat 16 that needs no matrix at all.
//
// Only the default matrices are supported. An explicit scaling_list_data() can
// carry any weights it likes, and reading them means parsing the SPS or PPS again
// here — mp4ff skips past that syntax without keeping the values. Switching the
// lists on and taking the defaults is what an encoder does when simply asked for
// scaling lists, and it is the case that used to decode wrongly.
func scalingListsFor(sps *hevc.SPS, pps *hevc.PPS) (*transform.ScalingLists, error) {
	if !sps.ScalingListEnabledFlag {
		return nil, nil
	}
	if sps.ScalingListDataPresentFlag || pps.ScalingListDataPresentFlag {
		return nil, fmt.Errorf(
			"explicit scaling_list_data is not supported, only the default scaling lists")
	}
	return transform.DefaultScalingLists(), nil
}

// decodeSegment decodes one slice segment into the picture it belongs to,
// starting a new picture when the segment says it is the first of one and
// publishing the previous picture at that point.
func (d *Decoder) decodeSegment(seg *sliceSegment, frames *[]*frame.Frame) error {
	// A per-CU chroma QP offset puts cu_chroma_qp_offset_flag and
	// cu_chroma_qp_offset_idx in the transform unit (spec 7.3.8.10). Nothing here
	// reads them, so the arithmetic decoder would run past bins that were coded
	// and produce a plausible-looking wrong picture. The picture and slice level
	// offsets, which need no extra syntax, are applied.
	if err := checkSupported(seg.sps, seg.pps); err != nil {
		return err
	}
	scaling, err := scalingListsFor(seg.sps, seg.pps)
	if err != nil {
		return err
	}
	seg.scaling = scaling
	if seg.first {
		if err := d.finishPicture(frames); err != nil {
			return err
		}
		if err := d.startPicture(seg); err != nil {
			return err
		}
	}
	pic := d.pic
	if pic == nil {
		return fmt.Errorf("slice segment at CTB %d arrived before the first segment of a picture", seg.addrRS)
	}

	if seg.dependent {
		// A dependent segment's header holds nothing but its address: the slice
		// type, QP, SAO flags and loop filter parameters are the independent
		// segment's, and so is the slice's place in bounds (spec 7.4.7.1). Its
		// neighbours in the earlier segments of this slice stay available, so the
		// availability map is deliberately not cleared here.
		if pic.slice == nil {
			return fmt.Errorf("dependent slice segment at CTB %d before any independent segment", seg.addrRS)
		}
		inherited := *pic.slice
		inherited.first = false
		inherited.dependent = true
		inherited.addrRS = seg.addrRS
		inherited.cabac = seg.cabac
		inherited.params.EntryPoints = seg.params.EntryPoints
		seg = &inherited
	} else {
		// A new slice: nothing decoded before it is available, and it takes its
		// own place in bounds. Slices are added in decoding order, which is what
		// lets the loop filters tell which side of a boundary came later.
		pic.startSegment()
		pic.slice = seg
		pic.sliceState = nil
		pic.sliceIdx = pic.bounds.AddSlice(loopfilter.Slice{
			DeblockingDisabled: seg.deblockingDisabled,
			BetaOffset:         seg.betaOffset,
			TcOffset:           seg.tcOffset,
			AcrossSlices:       seg.acrossSlices,
		})
	}

	seg.params.Grid = pic.grid
	seg.params.SegmentAddressRS = seg.addrRS
	seg.params.SaoParams = pic.saoParams
	seg.params.Dependent = seg.dependent
	seg.params.State = pic.sliceState

	sd, err := slice.DecodeSliceData(seg.cabac, seg.params)
	if err != nil {
		return fmt.Errorf("decode slice data: %w", err)
	}
	pic.sliceState = sd.State

	pic.bounds.ClaimSegment(seg.addrRS, sd.CtbsDecoded, pic.sliceIdx)
	if seg.saoEnabled {
		pic.saoEnabled = true
	}

	var refFrame *frame.Frame
	if !seg.intra {
		refFrame = d.refFrame
	}
	if err := reconstructSegment(pic.f, sd, seg.sps, refFrame, pic.grid,
		seg.chromaQPOffsets, seg.scaling); err != nil {
		return err
	}

	pic.cus = append(pic.cus, sd.CUs...)
	pic.ctbsDecoded += sd.CtbsDecoded
	return nil
}

// parsePredWeightTable parses pred_weight_table (spec 7.3.6.3) and reports
// whether any weight is actually signalled. When none is, every reference
// predicts unweighted and the table only cost bits.
//
// The per-reference flags carry a presence condition on the reference picture's
// layer and POC, which only bites for multi-layer streams and for a picture used
// as its own reference. Neither is supported, so the flags are always read.
func parsePredWeightTable(r *bits.EBSPReader, sliceType, numRefIdxL0, numRefIdxL1 int) bool {
	r.ReadExpGolomb()    // luma_log2_weight_denom
	r.ReadSignedGolomb() // delta_chroma_log2_weight_denom (4:2:0 always has chroma)
	lists := [2]int{numRefIdxL0, numRefIdxL1}
	if sliceType != context.SliceTypeB {
		lists[1] = 0
	}
	weighted := false
	for _, n := range lists {
		lumaFlags := make([]bool, n)
		chromaFlags := make([]bool, n)
		for i := range n {
			lumaFlags[i] = r.ReadFlag()
		}
		for i := range n {
			chromaFlags[i] = r.ReadFlag()
		}
		for i := range n {
			if lumaFlags[i] {
				weighted = true
				r.ReadSignedGolomb() // delta_luma_weight
				r.ReadSignedGolomb() // luma_offset
			}
			if chromaFlags[i] {
				weighted = true
				for range 2 {
					r.ReadSignedGolomb() // delta_chroma_weight
					r.ReadSignedGolomb() // delta_chroma_offset
				}
			}
		}
	}
	return weighted
}

// parseSegmentAddress reads the fields that only a slice segment other than the
// first of a picture carries (spec 7.3.6.1): the dependent flag and the segment
// address. A dependent segment's header stops there — everything else about the
// slice comes from the independent segment that started it.
func parseSegmentAddress(r *bits.EBSPReader, sps *hevc.SPS, pps *hevc.PPS, first bool) (
	addr int, dependent bool, err error) {

	if first {
		return 0, false, nil
	}
	if pps.DependentSliceSegmentsEnabledFlag {
		dependent = r.ReadFlag()
	}
	ctbsX, ctbsY := picSizeInCtbs(sps)
	numCtbs := ctbsX * ctbsY
	addr = int(r.Read(ceilLog2(numCtbs)))
	if addr <= 0 || addr >= numCtbs {
		return 0, false, fmt.Errorf("slice_segment_address %d in a picture of %d CTBs", addr, numCtbs)
	}
	return addr, dependent, nil
}

// parseDependentSegment finishes the header of a dependent slice segment, whose
// only remaining fields are the entry point offsets, any header extension and
// the byte alignment. The slice-level fields are the containing slice's.
func parseDependentSegment(r *bits.EBSPReader, nalu []byte, sps *hevc.SPS, pps *hevc.PPS,
	addrRS int) (*sliceSegment, error) {

	entryPoints, err := finishSliceHeader(r, sps, pps)
	if err != nil {
		return nil, err
	}
	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse dependent slice segment header: %w", err)
	}
	cabacData, droppedEPBs := removeEmulationPreventionBytesWithMap(nalu[r.NrBytesRead():])
	return &sliceSegment{
		sps:       sps,
		pps:       pps,
		dependent: true,
		addrRS:    addrRS,
		cabac:     cabacData,
		params:    slice.Params{EntryPoints: rebaseEntryPoints(entryPoints, droppedEPBs)},
	}, nil
}

// parsePOCAndRefPicSets parses the slice header fields that every picture except
// an IDR carries right after pic_output_flag (spec 7.3.6.1): pic_order_cnt_lsb,
// the short-term and long-term reference picture sets, and
// slice_temporal_mvp_enabled_flag. It returns the POC LSB and the slice-level
// temporal MVP flag.
func parsePOCAndRefPicSets(r *bits.EBSPReader, sps *hevc.SPS) (
	pocLsb int, temporalMvpEnabled bool, numPocTotalCurr int, err error) {

	pocLsbBits := int(sps.Log2MaxPicOrderCntLsbMinus4) + 4
	pocLsb = int(r.Read(pocLsbBits))

	// short_term_ref_pic_set
	stRefPicSetSpsFlag := r.ReadFlag()
	if stRefPicSetSpsFlag {
		numSets := int(sps.NumShortTermRefPicSets)
		idx := 0
		if numSets > 1 {
			idx = int(r.Read(ceilLog2(numSets)))
		}
		// NumPocTotalCurr counts the reference pictures this picture may use
		// (spec 7.4.7.2); it decides whether ref_pic_lists_modification is
		// present at all.
		if idx < len(sps.ShortTermRefPicSets) {
			set := sps.ShortTermRefPicSets[idx]
			for _, used := range set.UsedByCurrPicS0 {
				if used {
					numPocTotalCurr++
				}
			}
			for _, used := range set.UsedByCurrPicS1 {
				if used {
					numPocTotalCurr++
				}
			}
		} else {
			numPocTotalCurr = -1 // set not parsed: count unknown
		}
	} else {
		// Inline short_term_ref_pic_set (st_rps_idx = num_short_term_ref_pic_sets)
		stRpsIdx := int(sps.NumShortTermRefPicSets)
		interRpsPredFlag := false
		if stRpsIdx != 0 {
			interRpsPredFlag = r.ReadFlag()
		}
		if interRpsPredFlag {
			// Prediction mode: delta from a reference RPS
			r.ReadExpGolomb() // delta_idx_minus1
			r.ReadFlag()      // delta_rps_sign
			r.ReadExpGolomb() // abs_delta_rps_minus1
			// Need reference RPS to know num_delta_pocs - not supported yet
			return 0, false, 0, fmt.Errorf("inter_ref_pic_set_prediction_flag=1 not supported")
		}
		// Direct mode: list delta POCs
		numNegPics := int(r.ReadExpGolomb())
		numPosPics := int(r.ReadExpGolomb())
		for range numNegPics {
			r.ReadExpGolomb() // delta_poc_s0_minus1
			if r.ReadFlag() { // used_by_curr_pic_s0_flag
				numPocTotalCurr++
			}
		}
		for range numPosPics {
			r.ReadExpGolomb() // delta_poc_s1_minus1
			if r.ReadFlag() { // used_by_curr_pic_s1_flag
				numPocTotalCurr++
			}
		}
	}

	// long_term_ref_pics
	if sps.LongTermRefPicsPresentFlag {
		numLtSps := 0
		if sps.NumLongTermRefPics > 0 {
			numLtSps = int(r.ReadExpGolomb()) // num_long_term_sps
		}
		numLtPics := int(r.ReadExpGolomb()) // num_long_term_pics
		for i := range numLtSps + numLtPics {
			if i < numLtSps {
				if sps.NumLongTermRefPics > 1 {
					r.Read(ceilLog2(int(sps.NumLongTermRefPics))) // lt_idx_sps
				}
				// The used flag of an SPS long-term entry is not read back here,
				// so the count becomes unknown rather than wrong.
				numPocTotalCurr = -1
			} else {
				r.Read(pocLsbBits)                        // poc_lsb_lt
				if r.ReadFlag() && numPocTotalCurr >= 0 { // used_by_curr_pic_lt_flag
					numPocTotalCurr++
				}
			}
			if r.ReadFlag() { // delta_poc_msb_present_flag
				r.ReadExpGolomb() // delta_poc_msb_cycle_lt
			}
		}
	}

	// slice_temporal_mvp_enabled_flag
	if sps.SpsTemporalMvpEnabledFlag {
		temporalMvpEnabled = r.ReadFlag()
	}

	return pocLsb, temporalMvpEnabled, numPocTotalCurr, nil
}

// decodeIRAP decodes an intra random access point slice NALU: an IDR, or a CRA
// when isCRA is set. Both are decoded as intra pictures; the CRA header
// additionally carries slice_pic_order_cnt_lsb, the reference picture sets and
// slice_temporal_mvp_enabled_flag (spec 7.3.6.1).
// finishSliceHeader parses the tail of a slice segment header: the entry point
// offsets, any slice segment header extension, and byte_alignment. It returns
// the byte offsets, relative to the start of the slice data, at which each
// substream after the first begins.
//
// num_entry_point_offsets is present whenever tiles or wavefront parallel
// processing is enabled (spec 7.3.6.1). Skipping it left the reader a few bits
// short of the alignment, which showed up as a failed stop-bit check — and since
// x265 enables WPP by default, that was most real-world streams.
func finishSliceHeader(r *bits.EBSPReader, sps *hevc.SPS, pps *hevc.PPS) ([]int, error) {
	var entryPoints []int
	if pps.TilesEnabledFlag || pps.EntropyCodingSyncEnabledFlag {
		numEntryPointOffsets := int(r.ReadExpGolomb())
		if numEntryPointOffsets > 0 {
			offsetLen := int(r.ReadExpGolomb()) + 1
			if offsetLen < 1 || offsetLen > 32 {
				return nil, fmt.Errorf("slice header: offset_len_minus1+1 = %d out of range", offsetLen)
			}
			// The coded values are the sizes of consecutive substreams, so
			// accumulate them into absolute positions within the slice data.
			pos := 0
			entryPoints = make([]int, 0, numEntryPointOffsets)
			for range numEntryPointOffsets {
				pos += int(r.Read(offsetLen)) + 1
				entryPoints = append(entryPoints, pos)
			}
		}
	}

	if pps.SliceSegmentHeaderExtensionPresentFlag {
		extLen := int(r.ReadExpGolomb())
		for range extLen {
			r.Read(8)
		}
	}

	// byte_alignment
	stopBit := r.Read(1)
	if stopBit != 1 {
		return nil, fmt.Errorf("slice header: expected stop bit 1, got %d", stopBit)
	}
	if bitsInByte := r.NrBitsRead() % 8; bitsInByte != 0 {
		r.Read(8 - bitsInByte)
	}
	return entryPoints, nil
}

func (d *Decoder) parseIRAPSegment(nalu []byte, isCRA bool) (*sliceSegment, error) {
	r := bits.NewEBSPReader(bytes.NewReader(nalu))

	// Skip 2-byte NALU header
	r.Read(16)

	// first_slice_segment_in_pic_flag
	firstSlice := r.ReadFlag()

	// no_output_of_prior_pics_flag (present for all IRAP pictures)
	r.ReadFlag()

	// slice_pic_parameter_set_id
	ppsID := r.ReadExpGolomb()
	pps := d.ppsMap[uint32(ppsID)]
	if pps == nil {
		return nil, fmt.Errorf("PPS %d not found", ppsID)
	}
	sps := d.spsMap[pps.SeqParameterSetID]
	if sps == nil {
		return nil, fmt.Errorf("SPS %d not found", pps.SeqParameterSetID)
	}

	// dependent_slice_segment_flag and slice_segment_address
	segAddrRS, dependent, err := parseSegmentAddress(r, sps, pps, firstSlice)
	if err != nil {
		return nil, err
	}
	if dependent {
		return parseDependentSegment(r, nalu, sps, pps, segAddrRS)
	}

	// slice_reserved_flag[i]
	for range int(pps.NumExtraSliceHeaderBits) {
		r.ReadFlag()
	}

	// slice_type
	_ = r.ReadExpGolomb()

	// pic_output_flag if output_flag_present_flag (typically false)
	if pps.OutputFlagPresentFlag {
		r.ReadFlag()
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set.
	// CRA: POC lsb, reference picture sets and temporal MVP flag are present.
	if isCRA {
		if _, _, _, err := parsePOCAndRefPicSets(r, sps); err != nil {
			return nil, err
		}
	}

	// SAO flags if enabled
	var sliceSaoLuma, sliceSaoChroma bool
	if sps.SampleAdaptiveOffsetEnabledFlag {
		sliceSaoLuma = r.ReadFlag()
		sliceSaoChroma = r.ReadFlag()
	}

	// I-slice: no num_ref_idx, no pred_weight_table, no mvd_l1_zero_flag,
	// no cabac_init_flag, no collocated, no five_minus_max_num_merge_cand

	// slice_qp_delta
	qpDelta := r.ReadSignedGolomb()

	// slice_cb_qp_offset, slice_cr_qp_offset
	chromaQPOffsets := transform.ChromaQPOffsets{
		Cb: int(pps.CbQpOffset),
		Cr: int(pps.CrQpOffset),
	}
	if pps.SliceChromaQpOffsetsPresentFlag {
		chromaQPOffsets.Cb += int(r.ReadSignedGolomb())
		chromaQPOffsets.Cr += int(r.ReadSignedGolomb())
	}

	// Deblocking filter parameters: start with PPS defaults
	sliceDeblockingDisabled := pps.DeblockingFilterDisabledFlag
	betaOffsetDiv2 := int(pps.BetaOffsetDiv2)
	tcOffsetDiv2 := int(pps.TcOffsetDiv2)

	// deblocking_filter_override_flag
	if pps.DeblockingFilterOverrideEnabledFlag {
		override := r.ReadFlag()
		if override {
			sliceDeblockingDisabled = r.ReadFlag()
			if !sliceDeblockingDisabled {
				betaOffsetDiv2 = r.ReadSignedGolomb()
				tcOffsetDiv2 = r.ReadSignedGolomb()
			}
		}
	}

	// slice_loop_filter_across_slices_enabled_flag, which is inferred from the
	// PPS when absent (spec 7.4.7.1). Its presence turns on the *slice* SAO
	// flags, not the SPS one: a slice with SAO off and deblocking disabled does
	// not carry the bit even when the SPS enables SAO.
	sliceAcrossSlices := pps.LoopFilterAcrossSlicesEnabledFlag
	if pps.LoopFilterAcrossSlicesEnabledFlag &&
		(sliceSaoLuma || sliceSaoChroma || !sliceDeblockingDisabled) {
		sliceAcrossSlices = r.ReadFlag()
	}

	// Entry point offsets, slice header extension and byte alignment.
	entryPoints, epErr := finishSliceHeader(r, sps, pps)
	if epErr != nil {
		return nil, epErr
	}

	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse slice header: %w", err)
	}

	headerSize := r.NrBytesRead()

	log2Ctb := log2CtbSize(sps)
	cabacData, droppedEPBs := removeEmulationPreventionBytesWithMap(nalu[headerSize:])
	entryPoints = rebaseEntryPoints(entryPoints, droppedEPBs)

	log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)

	return &sliceSegment{
		sps:                sps,
		pps:                pps,
		first:              firstSlice,
		addrRS:             segAddrRS,
		cabac:              cabacData,
		intra:              true,
		deblockingDisabled: sliceDeblockingDisabled,
		betaOffset:         betaOffsetDiv2 * 2,
		tcOffset:           tcOffsetDiv2 * 2,
		saoEnabled:         sliceSaoLuma || sliceSaoChroma,
		acrossSlices:       sliceAcrossSlices,
		chromaQPOffsets:    chromaQPOffsets,
		params: slice.Params{
			SliceType:                       context.SliceTypeI,
			SliceQPY:                        26 + int(pps.InitQpMinus26) + qpDelta,
			PicWidth:                        int(sps.PicWidthInLumaSamples),
			PicHeight:                       int(sps.PicHeightInLumaSamples),
			Log2CtbSize:                     log2Ctb,
			Log2MinCbSize:                   int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3,
			Log2MinTrafoSize:                log2MinTrSize,
			Log2MaxTrafoSize:                log2MaxTrSize,
			MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
			SignDataHidingEnabled:           pps.SignDataHidingEnabledFlag,
			TransformSkipEnabled:            pps.TransformSkipEnabledFlag,
			SaoLuma:                         sliceSaoLuma,
			SaoChroma:                       sliceSaoChroma,
			CuQpDeltaEnabled:                pps.CuQpDeltaEnabledFlag,
			Log2MinCuQpDeltaSize:            log2Ctb - int(pps.DiffCuQpDeltaDepth),
			EntropyCodingSyncEnabled:        pps.EntropyCodingSyncEnabledFlag,
			EntryPoints:                     entryPoints,
		},
	}, nil
}

// parseTrailSegment parses the header of a trailing (non-IRAP) slice segment,
// which carries a P or B slice.
func (d *Decoder) parseTrailSegment(nalu []byte) (*sliceSegment, error) {
	r := bits.NewEBSPReader(bytes.NewReader(nalu))

	// Skip 2-byte NALU header
	r.Read(16)

	// first_slice_segment_in_pic_flag
	firstSlice := r.ReadFlag()

	// slice_pic_parameter_set_id
	ppsID := r.ReadExpGolomb()
	pps := d.ppsMap[uint32(ppsID)]
	if pps == nil {
		return nil, fmt.Errorf("PPS %d not found", ppsID)
	}
	sps := d.spsMap[pps.SeqParameterSetID]
	if sps == nil {
		return nil, fmt.Errorf("SPS %d not found", pps.SeqParameterSetID)
	}

	// dependent_slice_segment_flag and slice_segment_address
	segAddrRS, dependent, err := parseSegmentAddress(r, sps, pps, firstSlice)
	if err != nil {
		return nil, err
	}
	if dependent {
		return parseDependentSegment(r, nalu, sps, pps, segAddrRS)
	}

	// slice_reserved_flag[i]
	for range int(pps.NumExtraSliceHeaderBits) {
		r.ReadFlag()
	}

	// slice_type
	sliceType := int(r.ReadExpGolomb())

	// pic_output_flag if output_flag_present_flag
	if pps.OutputFlagPresentFlag {
		r.ReadFlag()
	}

	// pic_order_cnt_lsb, reference picture sets, slice_temporal_mvp_enabled_flag
	_, sliceTemporalMvpEnabled, numPocTotalCurr, err := parsePOCAndRefPicSets(r, sps)
	if err != nil {
		return nil, err
	}

	// SAO flags
	var sliceSaoLuma, sliceSaoChroma bool
	if sps.SampleAdaptiveOffsetEnabledFlag {
		sliceSaoLuma = r.ReadFlag()
		sliceSaoChroma = r.ReadFlag()
	}

	// For P/B slices:
	numRefIdxL0 := int(pps.NumRefIdxL0DefaultActiveMinus1) + 1

	// num_ref_idx_active_override_flag
	overrideFlag := r.ReadFlag()
	if overrideFlag {
		numRefIdxL0 = int(r.ReadExpGolomb()) + 1
		if sliceType == context.SliceTypeB {
			r.ReadExpGolomb() // num_ref_idx_l1_active_minus1
		}
	}

	// ref_pic_lists_modification, present when the PPS allows it and there is
	// more than one candidate reference to reorder (spec 7.3.6.1). With a single
	// reference there is nothing to reorder and the syntax is absent, which is
	// the only case handled: reading it would mean building the reference lists.
	if pps.ListsModificationPresentFlag && numPocTotalCurr != 1 {
		return nil, fmt.Errorf(
			"ref_pic_lists_modification is not supported (NumPocTotalCurr %d)", numPocTotalCurr)
	}

	// mvd_l1_zero_flag (B-slice only)
	if sliceType == context.SliceTypeB {
		r.ReadFlag()
	}

	// cabac_init_flag
	if pps.CabacInitPresentFlag {
		r.ReadFlag()
	}

	// collocated info (if temporal MVP enabled in slice)
	if sliceTemporalMvpEnabled {
		// collocated_from_l0_flag: only for B-slice
		if sliceType == context.SliceTypeB {
			r.ReadFlag() // collocated_from_l0_flag
		}
		// collocated_ref_idx: present if numRefIdx > 1
		if numRefIdxL0 > 1 {
			r.ReadExpGolomb() // collocated_ref_idx
		}
	}

	// pred_weight_table, which x265 emits by default: weighted_pred_flag is on
	// unless --no-weightp. Skipping it left the reader mid-header, and the entry
	// point offsets that follow then read as garbage.
	if (pps.WeightedPredFlag && sliceType == context.SliceTypeP) ||
		(pps.WeightedBipredFlag && sliceType == context.SliceTypeB) {
		numRefIdxL1 := 0
		if sliceType == context.SliceTypeB {
			numRefIdxL1 = int(pps.NumRefIdxL1DefaultActiveMinus1) + 1
		}
		if weighted := parsePredWeightTable(r, sliceType, numRefIdxL0, numRefIdxL1); weighted {
			// Default weights predict the plain reference sample, which a
			// zero-motion skip copy already produces. Real weights scale it, and
			// nothing here applies them.
			return nil, fmt.Errorf("weighted prediction with signalled weights is not supported")
		}
	}

	// five_minus_max_num_merge_cand
	fiveMinusMerge := int(r.ReadExpGolomb())
	maxNumMergeCand := 5 - fiveMinusMerge

	// slice_qp_delta
	qpDelta := r.ReadSignedGolomb()

	// slice_cb_qp_offset, slice_cr_qp_offset
	chromaQPOffsets := transform.ChromaQPOffsets{
		Cb: int(pps.CbQpOffset),
		Cr: int(pps.CrQpOffset),
	}
	if pps.SliceChromaQpOffsetsPresentFlag {
		chromaQPOffsets.Cb += int(r.ReadSignedGolomb())
		chromaQPOffsets.Cr += int(r.ReadSignedGolomb())
	}

	// Deblocking filter parameters
	sliceDeblockingDisabled := pps.DeblockingFilterDisabledFlag
	betaOffsetDiv2 := int(pps.BetaOffsetDiv2)
	tcOffsetDiv2 := int(pps.TcOffsetDiv2)

	if pps.DeblockingFilterOverrideEnabledFlag {
		override := r.ReadFlag()
		if override {
			sliceDeblockingDisabled = r.ReadFlag()
			if !sliceDeblockingDisabled {
				betaOffsetDiv2 = r.ReadSignedGolomb()
				tcOffsetDiv2 = r.ReadSignedGolomb()
			}
		}
	}

	// slice_loop_filter_across_slices_enabled_flag, inferred from the PPS when
	// absent (spec 7.4.7.1).
	sliceAcrossSlices := pps.LoopFilterAcrossSlicesEnabledFlag
	if pps.LoopFilterAcrossSlicesEnabledFlag &&
		(sliceSaoLuma || sliceSaoChroma || !sliceDeblockingDisabled) {
		sliceAcrossSlices = r.ReadFlag()
	}

	// Entry point offsets, slice header extension and byte alignment.
	entryPoints, epErr := finishSliceHeader(r, sps, pps)
	if epErr != nil {
		return nil, epErr
	}

	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse P-slice header: %w", err)
	}

	headerSize := r.NrBytesRead()

	log2Ctb := log2CtbSize(sps)
	cabacData, droppedEPBs := removeEmulationPreventionBytesWithMap(nalu[headerSize:])
	entryPoints = rebaseEntryPoints(entryPoints, droppedEPBs)

	log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)

	return &sliceSegment{
		sps:                sps,
		pps:                pps,
		first:              firstSlice,
		addrRS:             segAddrRS,
		cabac:              cabacData,
		deblockingDisabled: sliceDeblockingDisabled,
		betaOffset:         betaOffsetDiv2 * 2,
		tcOffset:           tcOffsetDiv2 * 2,
		saoEnabled:         sliceSaoLuma || sliceSaoChroma,
		acrossSlices:       sliceAcrossSlices,
		chromaQPOffsets:    chromaQPOffsets,
		params: slice.Params{
			SliceType:                       sliceType,
			SliceQPY:                        26 + int(pps.InitQpMinus26) + qpDelta,
			PicWidth:                        int(sps.PicWidthInLumaSamples),
			PicHeight:                       int(sps.PicHeightInLumaSamples),
			Log2CtbSize:                     log2Ctb,
			Log2MinCbSize:                   int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3,
			Log2MinTrafoSize:                log2MinTrSize,
			Log2MaxTrafoSize:                log2MaxTrSize,
			MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
			MaxNumMergeCand:                 maxNumMergeCand,
			SignDataHidingEnabled:           pps.SignDataHidingEnabledFlag,
			TransformSkipEnabled:            pps.TransformSkipEnabledFlag,
			SaoLuma:                         sliceSaoLuma,
			SaoChroma:                       sliceSaoChroma,
			CuQpDeltaEnabled:                pps.CuQpDeltaEnabledFlag,
			Log2MinCuQpDeltaSize:            log2Ctb - int(pps.DiffCuQpDeltaDepth),
			EntropyCodingSyncEnabled:        pps.EntropyCodingSyncEnabledFlag,
			EntryPoints:                     entryPoints,
		},
	}, nil
}

// reconstructSegment reconstructs one slice segment's CUs into the picture
// buffer. The loop filters are not applied here: they belong to the finished
// picture, which may be made of several segments.
func reconstructSegment(f *frame.Frame, sd *slice.SliceData, sps *hevc.SPS,
	refFrame *frame.Frame, grid *tiles.Grid,
	chromaQPOffsets transform.ChromaQPOffsets, scaling *transform.ScalingLists) error {

	bitDepth := 8
	picWidth, picHeight := f.Width, f.Height

	// CUs arrive in decoding order, so a tile boundary shows up as a change of
	// tile from one CU to the next. Crossing it makes everything decoded so far
	// unavailable for prediction (spec 6.4.1) — the same rule that applies at a
	// slice segment boundary, which startSegment handles.
	log2Ctb := log2CtbSize(sps)
	ctbOfLuma := func(x, y int) int { return (y>>log2Ctb)*grid.CtbsX + (x >> log2Ctb) }
	tile := -1

	for _, cu := range sd.CUs {
		if t := ctbOfLuma(cu.X0, cu.Y0); tile >= 0 && grid.TileIDOfRs(t) != tile {
			clearAvailability(f)
		}
		tile = grid.TileIDOfRs(ctbOfLuma(cu.X0, cu.Y0))

		if cu.SkipFlag {
			// Skip CU: copy from reference frame (zero motion)
			if refFrame == nil {
				return fmt.Errorf("skip CU at (%d,%d) but no reference frame", cu.X0, cu.Y0)
			}
			cuSize := 1 << cu.Log2CbSize
			// Copy luma
			for y := cu.Y0; y < cu.Y0+cuSize && y < picHeight; y++ {
				for x := cu.X0; x < cu.X0+cuSize && x < picWidth; x++ {
					f.Y[y*f.StrideY+x] = refFrame.Y[y*refFrame.StrideY+x]
				}
			}
			// Copy chroma (4:2:0)
			chromaCUSize := cuSize / 2
			cx0 := cu.X0 / 2
			cy0 := cu.Y0 / 2
			for y := cy0; y < cy0+chromaCUSize && y < picHeight/2; y++ {
				for x := cx0; x < cx0+chromaCUSize && x < picWidth/2; x++ {
					f.Cb[y*f.StrideC+x] = refFrame.Cb[y*refFrame.StrideC+x]
					f.Cr[y*f.StrideC+x] = refFrame.Cr[y*refFrame.StrideC+x]
				}
			}
			// Mark as decoded
			f.SetLumaDecoded(cu.X0, cu.Y0, cuSize)
			continue
		}

		for _, tu := range cu.TransformUnits {
			trSize := 1 << tu.Log2TrSize

			// Determine intra prediction mode for this TU
			lumaMode := cu.IntraLumaMode[0]
			if cu.PartMode == 1 { // NxN: each PU has its own mode
				halfCU := 1 << (cu.Log2CbSize - 1)
				puIdx := 0
				if tu.X0 >= cu.X0+halfCU {
					puIdx++
				}
				if tu.Y0 >= cu.Y0+halfCU {
					puIdx += 2
				}
				lumaMode = cu.IntraLumaMode[puIdx]
			}

			// Luma reconstruction
			lumaNeighbors := getLumaNeighbors(f, tu.X0, tu.Y0, trSize)
			pred.FilterRefSamples(lumaNeighbors, lumaMode, trSize, true, sps.StrongIntraSmoothingEnabledFlag)
			predSamples := predictIntra(lumaMode, trSize, lumaNeighbors, bitDepth, true)

			var residual []int32
			if tu.CbfLuma {
				// Every CU reaching this path is intra, so the scaling matrix is
				// the intra one for this component (spec Table 7-4).
				lumaM := scaling.Matrix(tu.Log2TrSize, transform.MatrixID(true, 0))
				dequantCoeffs := transform.Dequantize(tu.LumaCoeffs, trSize, cu.QpY, lumaM)
				if tu.TransformSkipLuma {
					residual = transform.TransformSkipShift(dequantCoeffs, tu.Log2TrSize, bitDepth)
				} else if trSize == 4 {
					residual = transform.InverseDST(dequantCoeffs)
				} else {
					residual = transform.InverseDCT(dequantCoeffs, trSize)
				}
			} else {
				residual = make([]int32, trSize*trSize)
			}

			recon := make([]int32, trSize*trSize)
			for i := range recon {
				recon[i] = predSamples[i] + residual[i]
			}
			f.SetLumaBlock(tu.X0, tu.Y0, trSize, recon)

			// Chroma reconstruction. With 4x4 luma TBs only one of the four
			// TUs in a group carries the chroma block, and it covers the whole
			// group, so it is reconstructed at ChromaX0/ChromaY0 rather than at
			// the TU's own position.
			if !tu.HasChroma {
				continue
			}
			chromaX0, chromaY0 := tu.ChromaX0, tu.ChromaY0
			chromaTrSize := trSize / 2
			if chromaTrSize < 4 {
				chromaTrSize = 4
			}

			// IntraPredModeC (spec 8.4.3) derives from IntraPredModeY at the
			// CU's top-left luma location, not from this transform block's PU.
			// With an NxN partition the four luma PUs share one chroma block, so
			// using the TU's own mode picks the wrong one for every quadrant but
			// the first — and produces modes the chroma syntax cannot even
			// express, such as 23 from a table of {0, 26, 10, 1}.
			cuLumaMode := cu.IntraLumaMode[0]
			chromaMode := cu.IntraChromaMode
			switch chromaMode {
			case 4:
				chromaMode = cuLumaMode
			case cuLumaMode:
				chromaMode = 34
			}

			for comp := range 2 {
				// Spec 8.6.1 adds the picture and slice offsets for this component
				// to the luma QP before Table 8-10 maps it, so Cb and Cr can scale
				// at different QPs within one transform unit.
				chromaQP := transform.ChromaQP(cu.QpY, chromaQPOffsets.For(comp))

				var chromaCoeffs []int32
				if comp == 0 {
					chromaCoeffs = tu.CbCoeffs
				} else {
					chromaCoeffs = tu.CrCoeffs
				}

				chromaNeighbors := getChromaNeighbors(f, comp, chromaX0/2, chromaY0/2, chromaTrSize)
				pred.FilterRefSamples(chromaNeighbors, chromaMode, chromaTrSize, false, false)
				chromaPred := predictIntra(chromaMode, chromaTrSize, chromaNeighbors, bitDepth, false)

				var chromaResidual []int32
				hasCbf := (comp == 0 && tu.CbfCb) || (comp == 1 && tu.CbfCr)
				if hasCbf {
					chromaLog2Size := tu.Log2TrSize - 1
					if chromaLog2Size < 2 {
						chromaLog2Size = 2
					}
					chromaM := scaling.Matrix(chromaLog2Size, transform.MatrixID(true, comp+1))
					dequantCoeffs := transform.Dequantize(chromaCoeffs, chromaTrSize, chromaQP, chromaM)
					tsChroma := (comp == 0 && tu.TransformSkipCb) || (comp == 1 && tu.TransformSkipCr)
					if tsChroma {
						chromaLog2 := tu.Log2TrSize - 1
						if chromaLog2 < 2 {
							chromaLog2 = 2
						}
						chromaResidual = transform.TransformSkipShift(dequantCoeffs, chromaLog2, bitDepth)
					} else {
						chromaResidual = transform.InverseDCT(dequantCoeffs, chromaTrSize)
					}
				} else {
					chromaResidual = make([]int32, chromaTrSize*chromaTrSize)
				}

				chromaRecon := make([]int32, chromaTrSize*chromaTrSize)
				for i := range chromaRecon {
					chromaRecon[i] = chromaPred[i] + chromaResidual[i]
				}
				f.SetChromaBlock(comp, chromaX0/2, chromaY0/2, chromaTrSize, chromaRecon)
			}
		}
	}

	return nil
}

// cropToConformanceWindow returns the visible part of a decoded picture. An
// encoder that pads the coded size up to a whole number of CTUs — x265 codes a
// 360-line source as 368 lines — signals the padding in the SPS conformance
// window, and a decoder that ignores it hands back the wrong picture size.
// Returns the input untouched when there is nothing to crop.
func cropToConformanceWindow(f *frame.Frame, sps *hevc.SPS) *frame.Frame {
	if !sps.ConformanceWindowFlag {
		return f
	}
	outW, outH := sps.ImageSize()
	width, height := int(outW), int(outH)
	if width == f.Width && height == f.Height {
		return f
	}
	if width <= 0 || height <= 0 || width > f.Width || height > f.Height {
		return f // nonsensical window: better the whole picture than nothing
	}

	// 4:2:0 offsets are in chroma units, so both planes crop on even boundaries.
	left := int(sps.ConformanceWindow.LeftOffset) * 2
	top := int(sps.ConformanceWindow.TopOffset) * 2

	out := frame.NewFrame(width, height)
	for y := range height {
		copy(out.Y[y*out.StrideY:y*out.StrideY+width], f.Y[(y+top)*f.StrideY+left:])
	}
	cw, ch := width/2, height/2
	for y := range ch {
		copy(out.Cb[y*out.StrideC:y*out.StrideC+cw], f.Cb[(y+top/2)*f.StrideC+left/2:])
		copy(out.Cr[y*out.StrideC:y*out.StrideC+cw], f.Cr[(y+top/2)*f.StrideC+left/2:])
	}
	return out
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

func getLumaNeighbors(f *frame.Frame, x0, y0, size int) *pred.Neighbors {
	return pred.BuildRefSamples(x0, y0, size, f.Width, f.Height,
		func(x, y int) uint8 { return f.GetLumaPixel(x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x, y) },
	)
}

func getChromaNeighbors(f *frame.Frame, comp, x0, y0, size int) *pred.Neighbors {
	return pred.BuildRefSamples(x0, y0, size, f.Width/2, f.Height/2,
		func(x, y int) uint8 { return f.GetChromaPixel(comp, x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x*2, y*2) },
	)
}

// removeEmulationPreventionBytesWithMap also reports the input positions of the
// bytes it dropped, so offsets expressed in raw NAL bytes can be translated to
// the cleaned buffer.
func removeEmulationPreventionBytesWithMap(data []byte) (out []byte, dropped []int) {
	out = make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, 0, 0)
			dropped = append(dropped, i+2)
			i += 3
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out, dropped
}

// rebaseEntryPoints translates entry point offsets from raw NAL byte positions
// into positions in the emulation-prevention-stripped slice data.
//
// Spec 7.4.7.1 counts emulation prevention bytes as part of the slice segment
// data for the purposes of these offsets, so on content that escapes a lot —
// flat colour, where long zero runs are common — the raw offsets run well past
// the end of the stripped buffer.
func rebaseEntryPoints(entryPoints, dropped []int) []int {
	if len(entryPoints) == 0 || len(dropped) == 0 {
		return entryPoints
	}
	out := make([]int, len(entryPoints))
	next := 0
	for i, ep := range entryPoints {
		for next < len(dropped) && dropped[next] < ep {
			next++
		}
		out[i] = ep - next
	}
	return out
}

// ceilLog2 returns ceil(log2(n)), minimum 1.
func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	bits := 0
	n--
	for n > 0 {
		bits++
		n >>= 1
	}
	return bits
}
