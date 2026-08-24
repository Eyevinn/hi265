package retile

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestVerifyTilesMatchStandaloneDecode is the substance of the whole tool: a
// tile's crop out of the stitched picture must equal that input decoded on its
// own, or the stitch changed the pixels. It runs on pkg/decoder in-process, so
// it needs no external binary.
func TestVerifyTilesMatchStandaloneDecode(t *testing.T) {
	tests := []struct {
		name       string
		rows, cols int
		inputs     []string
	}{
		{"2x1_vertical", 2, 1, []string{inA, inB}},
		{"2x2_grid", 2, 2, []string{inA, inB, inB, inA}},
		{"1x2_nonuniform", 1, 2, []string{inA, inC}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := stitchFiles(t, tc.rows, tc.cols, tc.inputs...)
			if err := Verify(res, DecoderHi265, nil); err != nil {
				t.Errorf("Verify: %v", err)
			}
		})
	}
}

// TestVerifyDetectsMismatch: a comparison that cannot fail proves nothing, so
// point one tile at the wrong source, of the right size, and require the
// failure. Only the pixels can catch this — every geometry check still passes.
func TestVerifyDetectsMismatch(t *testing.T) {
	res := stitchFiles(t, 2, 1, inA, inB)
	wrong, err := os.ReadFile(inA)
	if err != nil {
		t.Fatal(err)
	}
	res.Tiles[1].Data, res.Tiles[1].Name = wrong, inA
	err = Verify(res, DecoderHi265, nil)
	if err == nil || !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("error = %v, want MISMATCH", err)
	}
}

// TestVerifyRefusesWrongSizedSource: a source of another size cannot be
// compared at all, and that has to be an error rather than a skipped tile.
func TestVerifyRefusesWrongSizedSource(t *testing.T) {
	res := stitchFiles(t, 2, 1, inA, inB)
	wrong, err := os.ReadFile(inC) // 64x64 where the tile is 128x64
	if err != nil {
		t.Fatal(err)
	}
	res.Tiles[1].Data, res.Tiles[1].Name = wrong, inC
	if err := Verify(res, DecoderHi265, nil); err == nil ||
		!strings.Contains(err.Error(), "want 128x64") {
		t.Errorf("error = %v, want a size refusal", err)
	}
}

// TestVerifyRefusesInterInProcess pins the rule that matters most: a decoder
// that cannot reconstruct the picture must fail verification, not pass it.
func TestVerifyRefusesInterInProcess(t *testing.T) {
	res := stitchFiles(t, 2, 1, inPA, inPB)
	err := Verify(res, DecoderHi265, nil)
	if err == nil {
		t.Fatal("in-process verification passed a stitch of inter pictures it cannot decode")
	}
	if !strings.Contains(err.Error(), "zero-motion skip CUs") {
		t.Errorf("error = %v, want it to name the limitation", err)
	}
}

func TestVerifyUnknownDecoder(t *testing.T) {
	res := stitchFiles(t, 2, 1, inA, inB)
	err := Verify(res, "libde265", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown decoder") {
		t.Errorf("error = %v, want it to reject the decoder name", err)
	}
}

// ffmpegOrSkip skips when ffmpeg is absent, as the conformance tests do.
func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	if p := os.Getenv("HI265_FFMPEG"); p != "" {
		return
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
}

// TestVerifyWithFFmpeg is the cross-check: it would catch a bug shared by the
// stitcher and pkg/decoder, which agreeing with itself never can.
func TestVerifyWithFFmpeg(t *testing.T) {
	ffmpegOrSkip(t)
	res := stitchFiles(t, 2, 2, inA, inB, inB, inA)
	if err := Verify(res, DecoderFFmpeg, nil); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestVerifyAutoUsesFFmpegForInter: the MCTS inputs really are tileable, and
// auto has to reach for the decoder that can prove it.
func TestVerifyAutoUsesFFmpegForInter(t *testing.T) {
	ffmpegOrSkip(t)
	res := stitchFiles(t, 2, 1, inPA, inPB)
	if err := Verify(res, DecoderAuto, nil); err != nil {
		t.Errorf("Verify: %v", err)
	}
}
