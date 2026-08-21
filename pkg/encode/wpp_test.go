package encode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDecodeWPPStreams decodes x265 output with wavefront parallel processing
// left on, which is x265's default. Each CTU row is its own CABAC substream:
// the engine restarts at the row's entry point and its context state comes from
// a snapshot taken after the second CTU of the row above, not from the end of
// that row.
//
// Flat content keeps this a test of structure rather than of residual coding —
// busy content still diverges for an unrelated reason (roadmap 0.10), which
// would mask what this test is for.
func TestDecodeWPPStreams(t *testing.T) {
	ffmpeg := ffmpegBin(t)
	x265 := x265Bin(t)

	cases := []struct {
		name          string
		width, height int
		extra         []string
	}{
		// 22 CTU rows, so the row-to-row context sync runs many times.
		{"ctu16_many_rows", 640, 352, []string{"--ctu", "16", "--min-cu-size", "16"}},
		{"ctu32", 256, 192, []string{"--ctu", "32", "--min-cu-size", "16"}},
		{"ctu64", 256, 192, []string{"--ctu", "64", "--min-cu-size", "16"}},
		// One CTU per row: there is no second CTU to snapshot, so every row has
		// to fall back to a fresh context initialisation.
		{"single_ctu_column", 16, 128, []string{"--ctu", "16", "--min-cu-size", "16"}},
		// x265's own defaults: WPP plus SAO, sign data hiding, CTU 64 and 32x32
		// transforms, which is what real-world input actually looks like.
		{"x265_defaults", 256, 192, nil},
		{"x265_defaults_720p", 1280, 720, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stream := encodeWPPWithX265(t, x265, c.width, c.height, c.extra)

			ff := decodeWithFFmpeg(t, ffmpeg, stream)
			own := decodeWithHi265(t, stream, c.width, c.height, 1)
			if rep := compareYUV(t, own, ff, c.width, c.height, 1); !rep.exact() {
				t.Errorf("WPP stream decodes differently from FFmpeg: %s", rep)
			}
		})
	}
}

// encodeWPPWithX265 encodes flat content with WPP enabled (x265's default) and
// returns the Annex-B stream.
func encodeWPPWithX265(t *testing.T, x265 string, w, h int, extra []string) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "src.yuv")
	out := filepath.Join(dir, "out.265")

	// Flat mid-grey luma with distinct flat chroma planes.
	src := make([]byte, 0, w*h*3/2)
	for range w * h {
		src = append(src, 128)
	}
	for range w * h / 4 {
		src = append(src, 100)
	}
	for range w * h / 4 {
		src = append(src, 200)
	}
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatalf("write x265 input: %v", err)
	}

	args := []string{
		"--input", in,
		"--input-res", fmt.Sprintf("%dx%d", w, h),
		"--fps", "25", "--frames", "1", "--qp", "30",
		"--no-info", "--output", out,
	}
	args = append(args, extra...)

	cmd := exec.Command(x265, args...)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("x265 failed (%v): %s", err, combined)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read x265 output: %v", err)
	}
	return data
}
