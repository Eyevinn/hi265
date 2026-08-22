package encode

import (
	"bytes"
	"os"
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
