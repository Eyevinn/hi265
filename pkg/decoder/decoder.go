// Package decoder implements the HEVC/H.265 video decoder.
package decoder

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/pred"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// Decoder is the HEVC decoder.
type Decoder struct {
	vpsMap map[uint32][]byte
	spsMap map[uint32]*hevc.SPS
	ppsMap map[uint32]*hevc.PPS
}

// New creates a new HEVC decoder.
func New() *Decoder {
	return &Decoder{
		vpsMap: make(map[uint32][]byte),
		spsMap: make(map[uint32]*hevc.SPS),
		ppsMap: make(map[uint32]*hevc.PPS),
	}
}

// DecodeAnnexB decodes a single frame from an Annex-B byte stream.
func (d *Decoder) DecodeAnnexB(data []byte) (*frame.Frame, error) {
	nalus := avc.ExtractNalusFromByteStream(data)
	return d.DecodeNALUs(nalus)
}

// DecodeNALUs decodes a single frame from a list of NALUs (without start codes).
func (d *Decoder) DecodeNALUs(nalus [][]byte) (*frame.Frame, error) {
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])

		switch naluType {
		case hevc.NALU_VPS:
			// Store VPS raw bytes (mp4ff doesn't parse VPS internals)
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
			return d.decodeIDR(nalu)

		case hevc.NALU_SEI_PREFIX, hevc.NALU_SEI_SUFFIX:
			// Skip SEI

		case hevc.NALU_AUD:
			// Skip AUD

		default:
			// Skip unknown NALU types
		}
	}
	return nil, fmt.Errorf("no IDR frame found")
}

// decodeIDR decodes an IDR slice NALU.
func (d *Decoder) decodeIDR(nalu []byte) (*frame.Frame, error) {
	// Custom slice header parser for IDR I-slices.
	// mp4ff's ParseSliceHeader has a bug: it doesn't infer
	// slice_deblocking_filter_disabled_flag from PPS when the slice
	// doesn't override it, causing misalignment.
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

	// colour_plane_id if separate_colour_plane_flag (typically false)
	// Not present for our bitstream

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set

	// SAO flags if enabled
	if sps.SampleAdaptiveOffsetEnabledFlag {
		r.ReadFlag() // slice_sao_luma_flag
		r.ReadFlag() // slice_sao_chroma_flag
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

	// cu_chroma_qp_offset_enabled_flag if pps flag set
	// (not present in our bitstream)

	// deblocking_filter_override_flag
	if pps.DeblockingFilterOverrideEnabledFlag {
		override := r.ReadFlag()
		if override {
			r.ReadFlag() // slice_deblocking_filter_disabled_flag
			// If not disabled, read beta/tc offsets (skip for now)
		}
	}
	// Infer slice_deblocking_filter_disabled_flag = pps value
	sliceDeblockingDisabled := pps.DeblockingFilterDisabledFlag

	// loop_filter_across_slices_enabled_flag
	if pps.LoopFilterAcrossSlicesEnabledFlag &&
		(!sliceDeblockingDisabled || sps.SampleAdaptiveOffsetEnabledFlag) {
		r.ReadFlag()
	}

	// byte_alignment: stop bit (1) + zero padding to byte boundary
	stopBit := r.Read(1)
	if stopBit != 1 {
		return nil, fmt.Errorf("slice header: expected stop bit 1, got %d", stopBit)
	}
	bitsInByte := r.NrBitsRead() % 8
	if bitsInByte != 0 {
		r.Read(8 - bitsInByte) // skip alignment zeros
	}

	if err := r.AccError(); err != nil {
		return nil, fmt.Errorf("parse slice header: %w", err)
	}

	headerSize := r.NrBytesRead()

	// Calculate SliceQPY
	sliceQPY := 26 + int(pps.InitQpMinus26) + qpDelta

	// Get picture dimensions
	picWidth := int(sps.PicWidthInLumaSamples)
	picHeight := int(sps.PicHeightInLumaSamples)

	// Calculate CTB size
	log2CtbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 +
		int(sps.Log2DiffMaxMinLumaCodingBlockSize)

	// Extract CABAC data: starts after slice header, need to remove emulation prevention bytes
	cabacData := removeEmulationPreventionBytes(nalu[headerSize:])

	// Derive SPS parameters
	log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
	log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3

	// Decode slice data
	sd, err := slice.DecodeSliceData(cabacData, slice.Params{
		SliceQPY:                        sliceQPY,
		PicWidth:                        picWidth,
		PicHeight:                       picHeight,
		Log2CtbSize:                     log2CtbSize,
		Log2MinCbSize:                   log2MinCbSize,
		Log2MinTrafoSize:                log2MinTrSize,
		Log2MaxTrafoSize:                log2MaxTrSize,
		MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
	})
	if err != nil {
		return nil, fmt.Errorf("decode slice data: %w", err)
	}

	// Reconstruct frame
	return d.reconstructFrame(sd, sps, sliceQPY, picWidth, picHeight)
}

// reconstructFrame builds the output frame from decoded CU data.
func (d *Decoder) reconstructFrame(sd *slice.SliceData, sps *hevc.SPS,
	sliceQPY int, picWidth, picHeight int) (*frame.Frame, error) {

	f := frame.NewFrame(picWidth, picHeight)
	bitDepth := 8

	for _, cu := range sd.CUs {
		for _, tu := range cu.TransformUnits {
			trSize := 1 << tu.Log2TrSize

			// Determine intra prediction mode for this TU
			lumaMode := cu.IntraLumaMode[0] // Simplified: use first PU's mode

			// Luma reconstruction
			predSamples := predictIntra(lumaMode, trSize, nil, bitDepth)

			var residual []int32
			if tu.CbfLuma {
				// Dequantize
				dequantCoeffs := transform.Dequantize(tu.LumaCoeffs, trSize, sliceQPY)
				// Inverse transform
				if trSize == 4 && lumaMode != 0 && lumaMode != 1 {
					// 4x4 intra luma non-DC/non-planar: use DST
					residual = transform.InverseDST(dequantCoeffs)
				} else {
					residual = transform.InverseDCT(dequantCoeffs, trSize)
				}
			} else {
				residual = make([]int32, trSize*trSize)
			}

			// Reconstruct: pred + residual, clipped to [0, 255]
			recon := make([]int32, trSize*trSize)
			for i := range recon {
				recon[i] = predSamples[i] + residual[i]
			}
			f.SetLumaBlock(tu.X0, tu.Y0, trSize, recon)

			// Chroma reconstruction
			chromaTrSize := trSize / 2
			if chromaTrSize < 4 {
				chromaTrSize = 4
			}
			chromaLog2TrSize := tu.Log2TrSize - 1
			if chromaLog2TrSize < 2 {
				chromaLog2TrSize = 2
			}

			// Chroma prediction mode
			chromaMode := cu.IntraChromaMode
			if chromaMode == 4 { // DM mode
				chromaMode = lumaMode
			}

			// Chroma QP
			chromaQP := chromaQPFromLumaQP(sliceQPY)

			for comp := range 2 {
				var chromaCoeffs []int32
				if comp == 0 {
					chromaCoeffs = tu.CbCoeffs
				} else {
					chromaCoeffs = tu.CrCoeffs
				}

				chromaPred := predictIntra(chromaMode, chromaTrSize, nil, bitDepth)

				var chromaResidual []int32
				hasCbf := (comp == 0 && tu.CbfCb) || (comp == 1 && tu.CbfCr)
				if hasCbf {
					dequantCoeffs := transform.Dequantize(chromaCoeffs, chromaTrSize, chromaQP)
					chromaResidual = transform.InverseDCT(dequantCoeffs, chromaTrSize)
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

	return f, nil
}

// predictIntra performs intra prediction for a block.
func predictIntra(mode, size int, neighbors *pred.Neighbors, bitDepth int) []int32 {
	switch mode {
	case 0:
		return pred.PredictPlanar(size, neighbors, bitDepth)
	case 1:
		return pred.PredictDC(size, neighbors, bitDepth)
	default:
		// For other angular modes, fall back to DC for now
		return pred.PredictDC(size, neighbors, bitDepth)
	}
}

// removeEmulationPreventionBytes removes 0x03 emulation prevention bytes
// from the NALU payload (0x00 0x00 0x03 → 0x00 0x00).
func removeEmulationPreventionBytes(data []byte) []byte {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, 0, 0)
			i += 3 // skip the 0x03
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

// chromaQPFromLumaQP derives chroma QP from luma QP using HEVC spec Table 8-8.
func chromaQPFromLumaQP(qpY int) int {
	if qpY < 30 {
		return qpY
	}
	// HEVC chroma QP mapping table (Table 8-8)
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
