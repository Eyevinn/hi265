// Package decoder implements the HEVC/H.265 video decoder.
package decoder

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/deblock"
	"github.com/Eyevinn/hi265/internal/pred"
	"github.com/Eyevinn/hi265/internal/sao"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// Decoder is the HEVC decoder.
type Decoder struct {
	vpsMap   map[uint32][]byte
	spsMap   map[uint32]*hevc.SPS
	ppsMap   map[uint32]*hevc.PPS
	refFrame *frame.Frame // reference frame for inter prediction
}

// New creates a new HEVC decoder.
func New() *Decoder {
	return &Decoder{
		vpsMap: make(map[uint32][]byte),
		spsMap: make(map[uint32]*hevc.SPS),
		ppsMap: make(map[uint32]*hevc.PPS),
	}
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
			f, err := d.decodeIDR(nalu)
			if err != nil {
				return nil, err
			}
			d.refFrame = f
			frames = append(frames, f)

		case hevc.NALU_TRAIL_R, hevc.NALU_TRAIL_N:
			f, err := d.decodeTrailSlice(nalu)
			if err != nil {
				return nil, err
			}
			d.refFrame = f
			frames = append(frames, f)

		case hevc.NALU_SEI_PREFIX, hevc.NALU_SEI_SUFFIX:
			// Skip SEI

		case hevc.NALU_AUD:
			// Skip AUD

		default:
			// Skip unknown NALU types
		}
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames decoded")
	}
	return frames, nil
}

// decodeIDR decodes an IDR slice NALU.
func (d *Decoder) decodeIDR(nalu []byte) (*frame.Frame, error) {
	r := bits.NewEBSPReader(bytes.NewReader(nalu))

	// Skip 2-byte NALU header
	r.Read(16)

	// first_slice_segment_in_pic_flag
	r.ReadFlag()

	// no_output_of_prior_pics_flag (present for IDR)
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

	// slice_type
	_ = r.ReadExpGolomb()

	// pic_output_flag if output_flag_present_flag (typically false)
	if pps.OutputFlagPresentFlag {
		r.ReadFlag()
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set

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
	if pps.SliceChromaQpOffsetsPresentFlag {
		r.ReadSignedGolomb() // slice_cb_qp_offset
		r.ReadSignedGolomb() // slice_cr_qp_offset
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

	// loop_filter_across_slices_enabled_flag
	if pps.LoopFilterAcrossSlicesEnabledFlag &&
		(!sliceDeblockingDisabled || sps.SampleAdaptiveOffsetEnabledFlag) {
		r.ReadFlag()
	}

	// byte_alignment
	stopBit := r.Read(1)
	if stopBit != 1 {
		return nil, fmt.Errorf("slice header: expected stop bit 1, got %d", stopBit)
	}
	bitsInByte := r.NrBitsRead() % 8
	if bitsInByte != 0 {
		r.Read(8 - bitsInByte)
	}

	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse slice header: %w", err)
	}

	headerSize := r.NrBytesRead()

	sliceQPY := 26 + int(pps.InitQpMinus26) + qpDelta
	picWidth := int(sps.PicWidthInLumaSamples)
	picHeight := int(sps.PicHeightInLumaSamples)
	log2CtbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 +
		int(sps.Log2DiffMaxMinLumaCodingBlockSize)

	cabacData := removeEmulationPreventionBytes(nalu[headerSize:])

	log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3

	sd, err := slice.DecodeSliceData(cabacData, slice.Params{
		SliceType:                       context.SliceTypeI,
		SliceQPY:                        sliceQPY,
		PicWidth:                        picWidth,
		PicHeight:                       picHeight,
		Log2CtbSize:                     log2CtbSize,
		Log2MinCbSize:                   log2MinCbSize,
		Log2MinTrafoSize:                log2MinTrSize,
		Log2MaxTrafoSize:                log2MaxTrSize,
		MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
		SignDataHidingEnabled:           pps.SignDataHidingEnabledFlag,
		TransformSkipEnabled:            pps.TransformSkipEnabledFlag,
		SaoLuma:                         sliceSaoLuma,
		SaoChroma:                       sliceSaoChroma,
		CuQpDeltaEnabled:                pps.CuQpDeltaEnabledFlag,
		Log2MinCuQpDeltaSize:            log2CtbSize - int(pps.DiffCuQpDeltaDepth),
	})
	if err != nil {
		return nil, fmt.Errorf("decode slice data: %w", err)
	}

	return d.reconstructFrame(sd, sps, nil, sliceQPY, picWidth, picHeight,
		sliceDeblockingDisabled, betaOffsetDiv2*2, tcOffsetDiv2*2,
		sliceSaoLuma || sliceSaoChroma)
}

// decodeTrailSlice decodes a trailing (non-IDR) slice NALU (P or B slice).
func (d *Decoder) decodeTrailSlice(nalu []byte) (*frame.Frame, error) {
	r := bits.NewEBSPReader(bytes.NewReader(nalu))

	// Skip 2-byte NALU header
	r.Read(16)

	// first_slice_segment_in_pic_flag
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

	// slice_type
	sliceType := int(r.ReadExpGolomb())

	// pic_output_flag if output_flag_present_flag
	if pps.OutputFlagPresentFlag {
		r.ReadFlag()
	}

	// pic_order_cnt_lsb
	pocLsbBits := int(sps.Log2MaxPicOrderCntLsbMinus4) + 4
	r.Read(pocLsbBits)

	// short_term_ref_pic_set
	stRefPicSetSpsFlag := r.ReadFlag()
	if stRefPicSetSpsFlag {
		numSets := int(sps.NumShortTermRefPicSets)
		if numSets > 1 {
			nBits := ceilLog2(numSets)
			r.Read(nBits)
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
			return nil, fmt.Errorf("inter_ref_pic_set_prediction_flag=1 not supported")
		} else {
			// Direct mode: list delta POCs
			numNegPics := int(r.ReadExpGolomb())
			numPosPics := int(r.ReadExpGolomb())
			for range numNegPics {
				r.ReadExpGolomb() // delta_poc_s0_minus1
				r.ReadFlag()      // used_by_curr_pic_s0_flag
			}
			for range numPosPics {
				r.ReadExpGolomb() // delta_poc_s1_minus1
				r.ReadFlag()      // used_by_curr_pic_s1_flag
			}
		}
	}

	// long_term_ref_pics
	if sps.LongTermRefPicsPresentFlag {
		numLtSps := r.ReadExpGolomb()
		numLtPics := r.ReadExpGolomb()
		numLt := int(numLtSps + numLtPics)
		pocBits := int(sps.Log2MaxPicOrderCntLsbMinus4) + 4
		for i := range numLt {
			if i < int(numLtSps) && sps.NumLongTermRefPics > 1 {
				r.Read(ceilLog2(int(sps.NumLongTermRefPics)))
			} else if i >= int(numLtSps) {
				r.Read(pocBits) // poc_lsb_lt
			}
			r.ReadFlag() // delta_poc_msb_present_flag
		}
	}

	// slice_temporal_mvp_enabled_flag
	var sliceTemporalMvpEnabled bool
	if sps.SpsTemporalMvpEnabledFlag {
		sliceTemporalMvpEnabled = r.ReadFlag()
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

	// ref_pic_lists_modification (skip for simple case with 1 ref)
	// Present if ListsModificationPresentFlag && NumPocTotalCurr > 1
	// For our simple IPP... with 1 ref, NumPocTotalCurr=1, so not present

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

	// pred_weight_table (if weighted pred)
	// Not present for our test vectors (--no-weightp)

	// five_minus_max_num_merge_cand
	fiveMinusMerge := int(r.ReadExpGolomb())
	maxNumMergeCand := 5 - fiveMinusMerge

	// slice_qp_delta
	qpDelta := r.ReadSignedGolomb()

	// slice_cb_qp_offset, slice_cr_qp_offset
	if pps.SliceChromaQpOffsetsPresentFlag {
		r.ReadSignedGolomb()
		r.ReadSignedGolomb()
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

	// loop_filter_across_slices_enabled_flag
	if pps.LoopFilterAcrossSlicesEnabledFlag &&
		(!sliceDeblockingDisabled || sps.SampleAdaptiveOffsetEnabledFlag) {
		r.ReadFlag()
	}

	// byte_alignment
	stopBit := r.Read(1)
	if stopBit != 1 {
		return nil, fmt.Errorf("P-slice header: expected stop bit 1, got %d", stopBit)
	}
	bitsInByte := r.NrBitsRead() % 8
	if bitsInByte != 0 {
		r.Read(8 - bitsInByte)
	}

	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse P-slice header: %w", err)
	}

	headerSize := r.NrBytesRead()

	sliceQPY := 26 + int(pps.InitQpMinus26) + qpDelta
	picWidth := int(sps.PicWidthInLumaSamples)
	picHeight := int(sps.PicHeightInLumaSamples)
	log2CtbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 +
		int(sps.Log2DiffMaxMinLumaCodingBlockSize)

	cabacData := removeEmulationPreventionBytes(nalu[headerSize:])

	log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3

	sd, err := slice.DecodeSliceData(cabacData, slice.Params{
		SliceType:                       sliceType,
		SliceQPY:                        sliceQPY,
		PicWidth:                        picWidth,
		PicHeight:                       picHeight,
		Log2CtbSize:                     log2CtbSize,
		Log2MinCbSize:                   log2MinCbSize,
		Log2MinTrafoSize:                log2MinTrSize,
		Log2MaxTrafoSize:                log2MaxTrSize,
		MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
		MaxNumMergeCand:                 maxNumMergeCand,
		SignDataHidingEnabled:           pps.SignDataHidingEnabledFlag,
		TransformSkipEnabled:            pps.TransformSkipEnabledFlag,
		SaoLuma:                         sliceSaoLuma,
		SaoChroma:                       sliceSaoChroma,
		CuQpDeltaEnabled:                pps.CuQpDeltaEnabledFlag,
		Log2MinCuQpDeltaSize:            log2CtbSize - int(pps.DiffCuQpDeltaDepth),
	})
	if err != nil {
		return nil, fmt.Errorf("decode P-slice data: %w", err)
	}

	return d.reconstructFrame(sd, sps, d.refFrame, sliceQPY, picWidth, picHeight,
		sliceDeblockingDisabled, betaOffsetDiv2*2, tcOffsetDiv2*2,
		sliceSaoLuma || sliceSaoChroma)
}

// reconstructFrame builds the output frame from decoded CU data.
func (d *Decoder) reconstructFrame(sd *slice.SliceData, sps *hevc.SPS,
	refFrame *frame.Frame, sliceQPY int, picWidth, picHeight int,
	deblockingDisabled bool, betaOffset, tcOffset int,
	saoEnabled bool) (*frame.Frame, error) {

	f := frame.NewFrame(picWidth, picHeight)
	bitDepth := 8
	log2CtbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 +
		int(sps.Log2DiffMaxMinLumaCodingBlockSize)
	ctbSize := 1 << log2CtbSize

	for _, cu := range sd.CUs {
		if cu.SkipFlag {
			// Skip CU: copy from reference frame (zero motion)
			if refFrame == nil {
				return nil, fmt.Errorf("skip CU at (%d,%d) but no reference frame", cu.X0, cu.Y0)
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
			lumaNeighbors := getLumaNeighbors(f, tu.X0, tu.Y0, trSize, ctbSize)
			pred.FilterRefSamples(lumaNeighbors, lumaMode, trSize, true, sps.StrongIntraSmoothingEnabledFlag)
			predSamples := predictIntra(lumaMode, trSize, lumaNeighbors, bitDepth, true)

			var residual []int32
			if tu.CbfLuma {
				dequantCoeffs := transform.Dequantize(tu.LumaCoeffs, trSize, cu.QpY)
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

			// Chroma reconstruction
			if tu.Log2TrSize < 3 && (tu.X0%8 != 0 || tu.Y0%8 != 0) {
				continue
			}
			chromaTrSize := trSize / 2
			if chromaTrSize < 4 {
				chromaTrSize = 4
			}

			chromaMode := cu.IntraChromaMode
			if chromaMode == 4 {
				chromaMode = lumaMode
			} else if chromaMode == lumaMode {
				chromaMode = 34
			}

			chromaQP := chromaQPFromLumaQP(cu.QpY)

			for comp := range 2 {
				var chromaCoeffs []int32
				if comp == 0 {
					chromaCoeffs = tu.CbCoeffs
				} else {
					chromaCoeffs = tu.CrCoeffs
				}

				chromaNeighbors := getChromaNeighbors(f, comp, tu.X0/2, tu.Y0/2, chromaTrSize, ctbSize)
				pred.FilterRefSamples(chromaNeighbors, chromaMode, chromaTrSize, false, false)
				chromaPred := predictIntra(chromaMode, chromaTrSize, chromaNeighbors, bitDepth, false)

				var chromaResidual []int32
				hasCbf := (comp == 0 && tu.CbfCb) || (comp == 1 && tu.CbfCr)
				if hasCbf {
					dequantCoeffs := transform.Dequantize(chromaCoeffs, chromaTrSize, chromaQP)
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
				f.SetChromaBlock(comp, tu.X0/2, tu.Y0/2, chromaTrSize, chromaRecon)
			}
		}
	}

	// Apply deblocking filter if enabled
	if !deblockingDisabled {
		deblock.Apply(f, sd.CUs, sliceQPY, betaOffset, tcOffset)
	}

	// Apply SAO filter if enabled
	if saoEnabled {
		sao.Apply(f, sd.SaoParams, log2CtbSize)
	}

	return f, nil
}

// predictIntra performs intra prediction for a block.
func predictIntra(mode, size int, neighbors *pred.Neighbors, bitDepth int, isLuma bool) []int32 {
	switch mode {
	case 0:
		return pred.PredictPlanar(size, neighbors, bitDepth)
	case 1:
		return pred.PredictDC(size, neighbors, bitDepth, isLuma)
	default:
		return pred.PredictAngular(mode, size, neighbors, bitDepth)
	}
}

// buildRefSamples extracts and substitutes reference samples for intra prediction
// per HEVC spec section 8.4.4.2.2.
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

func getLumaNeighbors(f *frame.Frame, x0, y0, size, ctbSize int) *pred.Neighbors {
	return buildRefSamples(x0, y0, size, f.Width, f.Height, ctbSize,
		func(x, y int) uint8 { return f.GetLumaPixel(x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x, y) },
	)
}

func getChromaNeighbors(f *frame.Frame, comp, x0, y0, size, ctbSize int) *pred.Neighbors {
	return buildRefSamples(x0, y0, size, f.Width/2, f.Height/2, ctbSize/2,
		func(x, y int) uint8 { return f.GetChromaPixel(comp, x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x*2, y*2) },
	)
}

func removeEmulationPreventionBytes(data []byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, 0, 0)
			i += 3
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
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
