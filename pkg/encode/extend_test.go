package encode

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// buildStream returns an Annex-B stream of VPS/SPS/PPS + one IDR of a
// two-colour grid, plus its dimensions.
func buildStream(t *testing.T) []byte {
	t.Helper()

	grid, err := yuv.ParseGrid("xy,yx")
	if err != nil {
		t.Fatalf("parse grid: %v", err)
	}
	colors := yuv.ColorMap{
		'x': yuv.Color{Y: 235, Cb: 128, Cr: 128},
		'y': yuv.Color{Y: 16, Cb: 128, Cr: 128},
	}
	p := EncodeParams{Width: grid.Width * 16, Height: grid.Height * 16, QP: 26}

	var buf bytes.Buffer
	ps, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	buf.Write(ps)
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	buf.Write(idr)
	return buf.Bytes()
}

func TestLastFrameState(t *testing.T) {
	stream := buildStream(t)

	state, err := LastFrameState(stream)
	if err != nil {
		t.Fatalf("LastFrameState: %v", err)
	}
	if state.NaluType != hevc.NALU_IDR_W_RADL {
		t.Errorf("NaluType = %d, want %d (IDR_W_RADL)", state.NaluType, hevc.NALU_IDR_W_RADL)
	}
	if state.POC != 0 {
		t.Errorf("POC = %d, want 0 for an IDR", state.POC)
	}
	if state.SPS == nil || state.PPS == nil {
		t.Fatal("SPS or PPS missing")
	}

	// After appending, the last picture must be the final P-skip.
	extended, err := AppendEmptyFrames(stream, 3)
	if err != nil {
		t.Fatalf("AppendEmptyFrames: %v", err)
	}
	state, err = LastFrameState(extended)
	if err != nil {
		t.Fatalf("LastFrameState after append: %v", err)
	}
	if state.NaluType != hevc.NALU_TRAIL_R {
		t.Errorf("NaluType = %d, want %d (TRAIL_R)", state.NaluType, hevc.NALU_TRAIL_R)
	}
	if state.POC != 3 {
		t.Errorf("POC = %d, want 3 (one step per appended picture)", state.POC)
	}
}

// TestLastFrameStateRefusesRPSPredictedFromMissingSet checks that a slice whose
// inline short-term RPS is predicted from a set the SPS does not have is
// refused. The SPS has one set, so the inline RPS has stRpsIdx 1 and
// delta_idx_minus1 must be 0 (7.4.8); 1 points before set 0. Up to v0.61.0,
// mp4ff's slice header parser then indexed the sets at 255 and panicked.
// v0.62.0 refuses delta_idx_minus1, and LastFrameState takes the slice header
// from that parser, so this pins that the version go.mod requires still does.
func TestLastFrameStateRefusesRPSPredictedFromMissingSet(t *testing.T) {
	var spsHex, ppsHex string
	for _, tc := range craHeaderCases {
		if tc.name == "422_10bit" { // the set with num_short_term_ref_pic_sets = 1
			spsHex, ppsHex = tc.sps, tc.pps
		}
	}
	sps, _ := hex.DecodeString(spsHex)
	pps, _ := hex.DecodeString(ppsHex)

	w := NewBitWriter()
	w.WriteBit(1)     // first_slice_segment_in_pic_flag
	w.WriteUE(0)      // slice_pic_parameter_set_id
	w.WriteUE(1)      // slice_type = P
	w.WriteBits(1, 4) // slice_pic_order_cnt_lsb
	w.WriteBit(0)     // short_term_ref_pic_set_sps_flag = 0: inline, stRpsIdx 1
	w.WriteBit(1)     // inter_ref_pic_set_prediction_flag
	w.WriteUE(1)      // delta_idx_minus1
	w.WriteBit(0)     // delta_rps_sign
	w.WriteUE(0)      // abs_delta_rps_minus1
	w.WriteBits(3, 2) // used_by_curr_pic_flag for each picture of set 0, and the one deltaRps away
	w.WriteBit(1)     // rbsp_stop_one_bit
	w.AlignToByte()

	var stream bytes.Buffer
	for _, nalu := range [][]byte{sps, pps} {
		stream.Write([]byte{0, 0, 0, 1})
		stream.Write(nalu)
	}
	WriteNALU(&stream, int(hevc.NALU_TRAIL_R), w.Bytes())

	_, err := LastFrameState(stream.Bytes())
	if err == nil {
		t.Fatal("expected the slice header to be refused")
	}
	if !strings.Contains(err.Error(), "delta_idx_minus1") {
		t.Errorf("the error should name delta_idx_minus1, got: %v", err)
	}
}

// TestAppendEmptyFramesFreezes checks that every appended picture decodes to
// exactly the source's last picture — the freeze that makes this useful for
// padding a segment out to a target duration.
func TestAppendEmptyFramesFreezes(t *testing.T) {
	stream := buildStream(t)

	const appended = 4
	extended, err := AppendEmptyFrames(stream, appended)
	if err != nil {
		t.Fatalf("AppendEmptyFrames: %v", err)
	}
	if len(extended) <= len(stream) {
		t.Fatal("extended stream is not longer than the source")
	}

	frames, err := decoder.New().DecodeAnnexB(extended)
	if err != nil {
		t.Fatalf("decode extended stream: %v", err)
	}
	if len(frames) != 1+appended {
		t.Fatalf("decoded %d frames, want %d", len(frames), 1+appended)
	}
	want := frames[0].YUV420Bytes()
	for i := 1; i < len(frames); i++ {
		if !bytes.Equal(frames[i].YUV420Bytes(), want) {
			t.Errorf("appended frame %d differs from the source's last picture", i)
		}
	}
}

// Extending a real encoder's stream, rather than one of ours. x265 --no-deblock
// leaves slice_deblocking_filter_disabled_flag out of the slice header, where it
// has to be inferred from the PPS (spec 7.4.7.1). A parser that misses that
// inference goes on to read a slice_loop_filter_across_slices_enabled_flag bit
// that was never coded and fails byte_alignment() — which is what mp4ff's
// ParseSliceHeader did before v0.56.0, making this whole path refuse every
// --no-deblock stream with "alignment bit is not equal to one". Our own
// generated streams do not have that shape, so nothing caught it.
func TestAppendEmptyFramesOnRealNoDeblockStream(t *testing.T) {
	stream, err := os.ReadFile("../../testdata/sincos_128x64.265")
	if err != nil {
		t.Fatal(err)
	}

	state, err := LastFrameState(stream)
	if err != nil {
		t.Fatalf("LastFrameState: %v", err)
	}
	if state.POC != 0 {
		t.Errorf("POC = %d, want 0 for the IDR this vector holds", state.POC)
	}

	const appended = 2
	extended, err := AppendEmptyFrames(stream, appended)
	if err != nil {
		t.Fatalf("AppendEmptyFrames: %v", err)
	}
	frames, err := decoder.New().DecodeAnnexB(extended)
	if err != nil {
		t.Fatalf("decode extended stream: %v", err)
	}
	if len(frames) != 1+appended {
		t.Fatalf("decoded %d frames, want %d", len(frames), 1+appended)
	}
	want := frames[0].YUV420Bytes()
	for i := 1; i < len(frames); i++ {
		if !bytes.Equal(frames[i].YUV420Bytes(), want) {
			t.Errorf("appended frame %d differs from the source picture", i)
		}
	}
}

func TestAppendEmptyFramesRejectsBadInput(t *testing.T) {
	stream := buildStream(t)

	if _, err := AppendEmptyFrames(stream, 0); err == nil {
		t.Error("expected an error for count = 0")
	}
	if _, err := AppendEmptyFrames([]byte{0, 0, 0, 1}, 1); err == nil {
		t.Error("expected an error for a stream with no coded slice")
	}
}

// Extending a wavefront stream, which is what x265 produces at its defaults and
// so the shape hi265-mp4-extend most often meets. Until WPP emission landed the
// appended P-skip was refused outright, so the whole tool was unusable on a
// default-settings encode — the case that motivated it.
//
// The fixture is a real x265 --wpp encode; the appended pictures reuse its PPS,
// so their slice data has to carry one substream per CTB row with entry point
// offsets to match, and every one must still freeze the source picture exactly.
func TestAppendEmptyFramesOnWppStream(t *testing.T) {
	stream, err := os.ReadFile("../../testdata/slices_wpp_2slices_256x128.265")
	if err != nil {
		t.Fatal(err)
	}
	state, err := LastFrameState(stream)
	if err != nil {
		t.Fatalf("LastFrameState: %v", err)
	}
	if !state.PPS.EntropyCodingSyncEnabledFlag {
		t.Fatal("fixture is supposed to have wavefront parallel processing enabled")
	}

	const appended = 3
	extended, err := AppendEmptyFrames(stream, appended)
	if err != nil {
		t.Fatalf("AppendEmptyFrames: %v", err)
	}
	frames, err := decoder.New().DecodeAnnexB(extended)
	if err != nil {
		t.Fatalf("decode extended stream: %v", err)
	}
	if len(frames) != 1+appended {
		t.Fatalf("decoded %d frames, want %d", len(frames), 1+appended)
	}
	want := frames[0].YUV420Bytes()
	for i := 1; i < len(frames); i++ {
		if !bytes.Equal(frames[i].YUV420Bytes(), want) {
			t.Errorf("appended frame %d differs from the source picture", i)
		}
	}
}
