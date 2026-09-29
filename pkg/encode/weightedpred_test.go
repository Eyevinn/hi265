package encode

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

// weightpVector is one IDR from x265 at its default preset, which enables
// weighted prediction (every preset but ultrafast does), so its PPS sets
// weighted_pred_flag. Deblocking is off because pkg/decoder does not yet
// reconstruct a deblocked P-skip picture bit-exactly, with or without weighted
// prediction; FFmpeg is the check on that case. Made with:
//
//	ffmpeg -f lavfi -i testsrc2=size=128x64:rate=25 -frames:v 1 -c:v libx265 \
//	  -x265-params no-info=1:no-deblock=1 -f hevc testdata/weightp_128x64.265
const weightpVector = "../../testdata/weightp_128x64.265"

// TestEncodePSkipFromSPSPPS_WeightedPred codes a P-skip against a PPS with
// weighted_pred_flag set, which obliges the slice header to carry a
// pred_weight_table. Without one the decoder reads five_minus_max_num_merge_cand
// from where the table should be and desynchronises. The P-skip must decode to
// the picture it references, in pkg/decoder and, where it is installed, FFmpeg.
func TestEncodePSkipFromSPSPPS_WeightedPred(t *testing.T) {
	idr, err := readTestFile(weightpVector)
	if err != nil {
		t.Fatalf("read %s: %v", weightpVector, err)
	}
	sps, pps := parseSPSPPS(t, idr)
	if !pps.WeightedPredFlag {
		t.Fatalf("%s does not set weighted_pred_flag, so this test would not cover it", weightpVector)
	}
	checkPSkipCopiesReference(t, idr, sps, pps)
}

// TestEncodePSkipFromSPSPPS_TwoDefaultRefs codes a P-skip against a PPS with two
// default active list-0 references and temporal MVP on in the SPS. The header
// then owes collocated_ref_idx (spec 7.3.6.1) ahead of pred_weight_table, and
// the table owes a luma and a chroma flag per reference. x265 writes one
// default reference whatever --ref says, while HM among others writes more, so
// the test rewrites that one PPS field of an x265 vector with weighted
// prediction and one without; an I slice never reads it.
func TestEncodePSkipFromSPSPPS_TwoDefaultRefs(t *testing.T) {
	for _, vector := range []string{weightpVector, "../../testdata/sincos_128x64.265"} {
		t.Run(filepath.Base(vector), func(t *testing.T) {
			src, err := readTestFile(vector)
			if err != nil {
				t.Fatalf("read %s: %v", vector, err)
			}
			idr := withNumRefIdxL0DefaultActive(t, src, 2)
			sps, pps := parseSPSPPS(t, idr)
			if pps.NumRefIdxL0DefaultActiveMinus1 != 1 || !sps.SpsTemporalMvpEnabledFlag {
				t.Fatalf("want 2 default references and temporal MVP, got %d and %v",
					pps.NumRefIdxL0DefaultActiveMinus1+1, sps.SpsTemporalMvpEnabledFlag)
			}
			checkPSkipCopiesReference(t, idr, sps, pps)
		})
	}
}

// checkPSkipCopiesReference appends a P-skip coded against sps and pps to idr,
// and checks that it decodes to the picture it references, in pkg/decoder and,
// where it is installed, FFmpeg.
func checkPSkipCopiesReference(t *testing.T, idr []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()
	pSkip, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
	}
	stream := append(append([]byte{}, idr...), pSkip...)

	w, h := int(sps.PicWidthInLumaSamples), int(sps.PicHeightInLumaSamples)
	frameSize := w*h + 2*(w/2)*(h/2)
	own := decodeWithHi265(t, stream, w, h, 2)
	if !bytes.Equal(own[:frameSize], own[frameSize:]) {
		t.Error("hi265: the P-skip does not decode to the picture it references")
	}

	ffmpeg := ffmpegBin(t)
	ff := decodeWithFFmpeg(t, ffmpeg, stream)
	if rep := compareYUV(t, own, ff, w, h, 2); !rep.exact() {
		t.Errorf("decode differs from FFmpeg: %s", rep)
	}
}

// withNumRefIdxL0DefaultActive returns annexB with num_ref_idx_l0_default_active_minus1
// in its PPS rewritten to n-1, and every other bit of the stream unchanged.
func withNumRefIdxL0DefaultActive(t *testing.T, annexB []byte, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, nalu := range avc.ExtractNalusFromByteStream(annexB) {
		out.Write([]byte{0, 0, 0, 1})
		if hevc.GetNaluType(nalu[0]) != hevc.NALU_PPS {
			out.Write(nalu)
			continue
		}
		rbsp := RemoveEmulationPrevention(nalu[2:])
		r := NewBitReader(rbsp)
		r.ReadUE()    // pps_pic_parameter_set_id
		r.ReadUE()    // pps_seq_parameter_set_id
		r.SkipBits(7) // dependent_slice_segments_enabled_flag .. cabac_init_present_flag
		start := r.Pos()
		r.ReadUE() // num_ref_idx_l0_default_active_minus1
		end := r.Pos()
		stop := 8*len(rbsp) - 1 // rbsp_stop_one_bit, the last bit set
		for rbsp[stop/8]>>(7-stop%8)&1 == 0 {
			stop--
		}

		w := NewBitWriter()
		src := NewBitReader(rbsp)
		for range start {
			w.WriteBit(uint8(src.ReadBit()))
		}
		w.WriteUE(uint32(n - 1))
		src.SkipBits(end - start)
		for range stop - end {
			w.WriteBit(uint8(src.ReadBit()))
		}
		w.WriteBit(1) // rbsp_stop_one_bit
		w.AlignToByte()
		out.Write(nalu[:2])
		out.Write(InsertEBSP(w.Bytes()))
	}
	return out.Bytes()
}

// TestEncodePSkipFromSPSPPS_WeightedPredDeblocked is the same check on stock
// x265 output with deblocking on, the shape a real feed has, decoded by FFmpeg.
func TestEncodePSkipFromSPSPPS_WeightedPredDeblocked(t *testing.T) {
	ffmpeg := ffmpegBin(t)
	x265 := x265Bin(t)
	const w, h = 128, 64
	idr := encodeRealWithX265(t, x265, lavfiSource(t, ffmpeg, "testsrc2", w, h), w, h, nil)
	sps, pps := parseSPSPPS(t, idr)
	if !pps.WeightedPredFlag {
		t.Fatal("x265's default output does not set weighted_pred_flag, so this test would not cover it")
	}

	pSkip, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
	}
	ff := decodeWithFFmpeg(t, ffmpeg, append(append([]byte{}, idr...), pSkip...))
	frameSize := w*h + 2*(w/2)*(h/2)
	if len(ff) != 2*frameSize {
		t.Fatalf("FFmpeg decoded %d bytes, want two %dx%d frames", len(ff), w, h)
	}
	if !bytes.Equal(ff[:frameSize], ff[frameSize:]) {
		t.Error("FFmpeg: the P-skip does not decode to the picture it references")
	}
}

func parseSPSPPS(t *testing.T, annexB []byte) (*hevc.SPS, *hevc.PPS) {
	t.Helper()
	spsMap := make(map[uint32]*hevc.SPS)
	var sps *hevc.SPS
	var pps *hevc.PPS
	var err error
	for _, nalu := range avc.ExtractNalusFromByteStream(annexB) {
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_SPS:
			if sps, err = hevc.ParseSPSNALUnit(nalu); err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			if pps, err = hevc.ParsePPSNALUnit(nalu, spsMap); err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		}
	}
	if sps == nil || pps == nil {
		t.Fatal("no SPS/PPS in the stream")
	}
	return sps, pps
}
