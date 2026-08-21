package encode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDecodeRealContent decodes x265 output on natural content and requires a
// bit-exact match with FFmpeg. Where the generator conformance tests cover what
// hi265 itself produces — flat colours, a handful of intra modes, one transform
// size — these cover what a real encoder emits: every intra mode, NxN
// partitions, transform trees down to 4x4, SAO, deblocking, sign data hiding,
// wavefront parallel processing and per-quantization-group QP.
//
// Every case here failed before the 0.10 work: several desynchronised the
// arithmetic decoder outright, and the rate-controlled ones panicked on a
// negative QP.
func TestDecodeRealContent(t *testing.T) {
	ffmpeg := ffmpegBin(t)
	x265 := x265Bin(t)

	const w, h = 192, 128

	contents := []string{"testsrc2", "smptebars", "gradients"}
	rates := []struct {
		name string
		args []string
	}{
		// Constant QP leaves cu_qp_delta off; rate control turns it on, and
		// x265 then defaults to a quantization group smaller than the CTU.
		{"qp30", []string{"--qp", "30"}},
		{"qp40", []string{"--qp", "40"}},
		{"crf28", []string{"--crf", "28"}},
		{"bitrate", []string{"--bitrate", "400"}},
	}
	geometries := []struct {
		name string
		args []string
	}{
		{"ctu16_cu8", []string{"--ctu", "16", "--min-cu-size", "8"}},
		{"ctu32_cu8", []string{"--ctu", "32", "--min-cu-size", "8"}},
		{"ctu64_cu16", []string{"--ctu", "64", "--min-cu-size", "16"}},
		{"ctu64_tu32", []string{"--ctu", "64", "--min-cu-size", "8", "--max-tu-size", "32"}},
	}

	for _, content := range contents {
		src := lavfiSource(t, ffmpeg, content, w, h)
		for _, rate := range rates {
			for _, geom := range geometries {
				name := fmt.Sprintf("%s_%s_%s", content, rate.name, geom.name)
				t.Run(name, func(t *testing.T) {
					args := append(append([]string{}, rate.args...), geom.args...)
					stream := encodeRealWithX265(t, x265, src, w, h, args)

					ff := decodeWithFFmpeg(t, ffmpeg, stream)
					own := decodeWithHi265(t, stream, w, h, 1)
					if rep := compareYUV(t, own, ff, w, h, 1); !rep.exact() {
						t.Errorf("decode differs from FFmpeg: %s", rep)
					}
				})
			}
		}
	}
}

// lavfiSource renders one frame of an FFmpeg test pattern as raw yuv420p.
func lavfiSource(t *testing.T, ffmpeg, pattern string, w, h int) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), pattern+".yuv")
	cmd := exec.Command(ffmpeg, "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("%s=size=%dx%d:rate=1:duration=1", pattern, w, h),
		"-pix_fmt", "yuv420p", "-f", "rawvideo", "-y", out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not render %s (%v): %s", pattern, err, combined)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s source: %v", pattern, err)
	}
	return data
}

// encodeRealWithX265 encodes one frame with x265's own defaults for everything
// the caller does not override, so the streams exercise what real output looks
// like rather than a reduced feature set.
func encodeRealWithX265(t *testing.T, x265 string, src []byte, w, h int, extra []string) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "src.yuv")
	out := filepath.Join(dir, "out.265")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatalf("write x265 input: %v", err)
	}

	args := []string{
		"--input", in,
		"--input-res", fmt.Sprintf("%dx%d", w, h),
		"--fps", "25", "--frames", "1",
		"--no-info", "--output", out,
	}
	args = append(args, extra...)

	if combined, err := exec.Command(x265, args...).CombinedOutput(); err != nil {
		t.Skipf("x265 failed (%v): %s", err, combined)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read x265 output: %v", err)
	}
	return data
}
