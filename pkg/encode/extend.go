package encode

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

// StreamState describes the last coded picture of an existing bitstream, which
// is what an appended picture has to continue from.
type StreamState struct {
	// POC is the pic_order_cnt_lsb of the last coded slice. HEVC counts
	// pictures, so the next picture normally uses POC+1 — unlike AVC, where
	// frame_num and pic_order_cnt_lsb advance on different strides.
	POC int
	// NaluType is the NAL unit type of that slice, e.g. hevc.NALU_IDR_W_RADL
	// or hevc.NALU_TRAIL_R.
	NaluType hevc.NaluType
	// SPS and PPS are the active parameter sets, reused verbatim for appended
	// slices so the result splices without a parameter-set change.
	SPS *hevc.SPS
	PPS *hevc.PPS
}

// LastFrameState returns the state of the last coded picture in an Annex-B
// bitstream: its POC, its NAL unit type, and the parameter sets in force.
//
// Use it to pick continuation values when appending pictures with
// EncodePSkipSliceFromSPSPPS. For the common case of appending one or more
// empty frames, AppendEmptyFrames wraps the whole flow in a single call.
//
// Returns an error if the stream carries no SPS, no PPS, or no coded slice.
func LastFrameState(annexB []byte) (*StreamState, error) {
	nalus := avc.ExtractNalusFromByteStream(annexB)
	spsMap := make(map[uint32]*hevc.SPS)
	ppsMap := make(map[uint32]*hevc.PPS)

	var (
		lastSlice    *hevc.SliceHeader
		lastSliceNal hevc.NaluType
		lastPPS      *hevc.PPS
		found        bool
	)
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])
		switch naluType {
		case hevc.NALU_SPS:
			sps, err := hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				return nil, fmt.Errorf("LastFrameState: parse SPS: %w", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			pps, err := hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				return nil, fmt.Errorf("LastFrameState: parse PPS: %w", err)
			}
			ppsMap[pps.PicParameterSetID] = pps
		default:
			if !isVCLNaluType(naluType) {
				continue
			}
			sh, err := hevc.ParseSliceHeader(nalu, spsMap, ppsMap)
			if err != nil {
				return nil, fmt.Errorf("LastFrameState: parse slice header: %w", err)
			}
			lastSlice = sh
			lastSliceNal = naluType
			lastPPS = ppsMap[uint32(sh.PicParameterSetId)]
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("LastFrameState: no coded slice found")
	}
	if lastPPS == nil {
		return nil, fmt.Errorf("LastFrameState: PPS %d not found", lastSlice.PicParameterSetId)
	}
	sps := spsMap[uint32(lastPPS.SeqParameterSetID)]
	if sps == nil {
		return nil, fmt.Errorf("LastFrameState: SPS %d not found", lastPPS.SeqParameterSetID)
	}

	poc := int(lastSlice.PicOrderCntLsb)
	if lastSliceNal == hevc.NALU_IDR_W_RADL || lastSliceNal == hevc.NALU_IDR_N_LP {
		// An IDR header carries no pic_order_cnt_lsb; its POC is 0 by definition.
		poc = 0
	}
	return &StreamState{POC: poc, NaluType: lastSliceNal, SPS: sps, PPS: lastPPS}, nil
}

// AppendEmptyFrames extends an Annex-B bitstream with count empty (P-skip)
// pictures that continue the source's POC, one step per picture. Every appended
// picture copies its predecessor unchanged, so the result plays as a freeze on
// the source's last picture.
//
// The source's SPS and PPS are reused as-is and not re-emitted, so the appended
// slices splice into the stream without a parameter-set change — something a
// normal encoder run cannot do.
func AppendEmptyFrames(annexB []byte, count int) ([]byte, error) {
	if count <= 0 {
		return nil, fmt.Errorf("AppendEmptyFrames: count must be positive, got %d", count)
	}
	state, err := LastFrameState(annexB)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(annexB)
	for i := 1; i <= count; i++ {
		slice, err := EncodePSkipSliceFromSPSPPS(state.SPS, state.PPS, state.POC+i)
		if err != nil {
			return nil, fmt.Errorf("AppendEmptyFrames: encode picture %d: %w", i, err)
		}
		buf.Write(slice)
	}
	return buf.Bytes(), nil
}

// isVCLNaluType reports whether a NAL unit type carries a coded slice segment.
// HEVC VCL types are 0..31; 32 and above are non-VCL (VPS, SPS, PPS, SEI, ...).
func isVCLNaluType(t hevc.NaluType) bool {
	return t <= 31
}
