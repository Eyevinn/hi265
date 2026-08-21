package encode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// quadrantPattern is a grid at 8x8 block resolution: each character covers an
// 8x8 luma block, so the four quadrants of every 16x16 CTU differ from each
// other and from their neighbours. Four colours, all with strong luma and
// chroma steps, so every 8x8 CU carries a real residual.
var quadrantPattern = []string{
	"ABCDABCD",
	"CDABCDAB",
	"BADCBADC",
	"DCBADCBA",
}

var quadrantColors = yuv.ColorMap{
	'A': yuv.Color{Y: 16, Cb: 128, Cr: 128},  // black
	'B': yuv.Color{Y: 235, Cb: 128, Cr: 128}, // white
	'C': yuv.Color{Y: 81, Cb: 90, Cr: 240},   // red
	'D': yuv.Color{Y: 145, Cb: 54, Cr: 34},   // green
}

// build8x8Planes renders a grid whose characters map to 8x8 blocks (the same
// resolution hi264's "@8x8" gridimg directive produces) into full YUV planes.
func build8x8Planes(t *testing.T, rows []string, colors yuv.ColorMap) (y, cb, cr []uint8, w, h int) {
	t.Helper()

	grid, err := yuv.ParseGrid(strings.Join(rows, ","))
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	pg, err := yuv.GridToPlaneGridBS(grid, colors, 8)
	if err != nil {
		t.Fatalf("GridToPlaneGridBS: %v", err)
	}
	f := yuv.BuildFrameFromPlaneGrid(pg)
	return f.Y, f.Cb, f.Cr, pg.PixelWidth(), pg.PixelHeight()
}

// encode8x8Stream builds a complete Annex-B stream (VPS+SPS+PPS+IDR, plus
// optional P-skip frames) for the given planes using 8x8 coding granularity.
func encode8x8Stream(t *testing.T, w, h, qp int, use8x8 bool, y, cb, cr []uint8, pSkips int) []byte {
	t.Helper()

	p := EncodeParams{Width: w, Height: h, QP: qp, Use8x8CU: use8x8}
	stream, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}

	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(w, h, qp, use8x8, y, cb, cr))
	stream = append(stream, buf.Bytes()...)

	for i := 1; i <= pSkips; i++ {
		ps, err := GeneratePSkip(p, i)
		if err != nil {
			t.Fatalf("GeneratePSkip: %v", err)
		}
		stream = append(stream, ps...)
	}
	return stream
}

// cuFFmpegDecode decodes an Annex-B stream to raw YUV420 planar bytes.
// The test is skipped when ffmpeg is not installed.
func cuFFmpegDecode(t *testing.T, annexB []byte) []byte {
	t.Helper()

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	dir := t.TempDir()
	in := filepath.Join(dir, "in.265")
	out := filepath.Join(dir, "out.yuv")
	if err := os.WriteFile(in, annexB, 0o644); err != nil {
		t.Fatalf("write bitstream: %v", err)
	}

	cmd := exec.Command(ffmpeg, "-y", "-v", "error", "-i", in,
		"-f", "rawvideo", "-pix_fmt", "yuv420p", out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read ffmpeg output: %v", err)
	}
	return data
}

// cuHi265Decode decodes an Annex-B stream with this package's own decoder.
func cuHi265Decode(t *testing.T, annexB []byte, wantFrames int) []byte {
	t.Helper()

	frames, err := decoder.New().DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != wantFrames {
		t.Fatalf("decoded %d frames, want %d", len(frames), wantFrames)
	}
	var out []byte
	for _, f := range frames {
		out = append(out, f.YUV420Bytes()...)
	}
	return out
}

// cuPlaneDiff reports the mean and maximum absolute difference between two
// equally sized buffers.
func cuPlaneDiff(t *testing.T, got, want []byte) (mean float64, maxDiff int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("size mismatch: got %d, want %d", len(got), len(want))
	}
	sum := 0
	for i := range got {
		d := abs(int(got[i]) - int(want[i]))
		sum += d
		if d > maxDiff {
			maxDiff = d
		}
	}
	return float64(sum) / float64(len(got)), maxDiff
}

func cuIntendedYUV(y, cb, cr []uint8) []byte {
	out := make([]byte, 0, len(y)+len(cb)+len(cr))
	out = append(out, y...)
	out = append(out, cb...)
	out = append(out, cr...)
	return out
}

// TestEncode8x8QuadrantsMatchFFmpeg is the conformance test for 8x8 coding
// granularity: four PART_2Nx2N 8x8 CUs per 16x16 CTU, each with its own intra
// mode, an 8x8 luma transform block and 4x4 chroma transform blocks. Both those
// sizes take the mode-dependent residual scan of spec 7.4.9.11 (including the
// last_sig_coeff x/y swap for the vertical scan), which encoder/decoder
// agreement alone cannot validate — only FFmpeg can.
//
// The pattern is also the sharpest available test of the MPM candidate-B CTB
// boundary rule (spec 8.4.2): with four 8x8 CUs inside one 16x16 CTB, the two
// top CUs must force candB = INTRA_DC because their above neighbour lies in the
// CTB above, while the two bottom CUs legitimately use the above CU's mode.
// Both sides of that branch are exercised in every CTB of this frame.
func TestEncode8x8QuadrantsMatchFFmpeg(t *testing.T) {
	y, cb, cr, w, h := build8x8Planes(t, quadrantPattern, quadrantColors)
	if w != 64 || h != 32 {
		t.Fatalf("unexpected frame size %dx%d", w, h)
	}

	// Tolerances against the intended pattern are pure quantization error, with
	// roughly a factor two of headroom over the measured values.
	//
	// QP 34 is deliberately absent: chromaQPFromLumaQP maps luma QP 34 to
	// chroma QP 32 in both the generator and the decoder while spec Table 8-10
	// says 33, so chroma differs from FFmpeg at that one QP (measured mean 1.6,
	// max 14). That is a pre-existing defect of the shared chroma QP table,
	// unrelated to coding granularity.
	cases := []struct {
		qp      int
		maxDiff int
		mean    float64
	}{
		{20, 6, 0.5},
		{26, 12, 1.5},
		{30, 14, 3.0},
		{40, 60, 7.0},
	}
	for _, c := range cases {
		t.Run("qp"+strconv.Itoa(c.qp), func(t *testing.T) {
			stream := encode8x8Stream(t, w, h, c.qp, true, y, cb, cr, 0)

			ff := cuFFmpegDecode(t, stream)
			own := cuHi265Decode(t, stream, 1)

			if !bytes.Equal(ff, own) {
				mean, maxDiff := cuPlaneDiff(t, own, ff)
				t.Fatalf("hi265dec and FFmpeg disagree: mean=%.3f max=%d", mean, maxDiff)
			}

			// Both decodes must also reproduce the intended pattern within
			// quantization error.
			mean, maxDiff := cuPlaneDiff(t, own, cuIntendedYUV(y, cb, cr))
			t.Logf("QP=%d vs intended pattern: mean=%.3f max=%d", c.qp, mean, maxDiff)
			if maxDiff > c.maxDiff {
				t.Errorf("max error %d vs intended pattern exceeds %d", maxDiff, c.maxDiff)
			}
			if mean > c.mean {
				t.Errorf("mean error %.3f vs intended pattern exceeds %.3f", mean, c.mean)
			}
		})
	}
}

// TestEncode8x8PSkipMatchesFFmpeg checks that P-skip frames still work when the
// SPS says minCbSize = 8: the P-slice writes split_cu_flag = 0 at depth 0, so
// each CTU stays one 16x16 skip CU that repeats the previous picture.
func TestEncode8x8PSkipMatchesFFmpeg(t *testing.T) {
	y, cb, cr, w, h := build8x8Planes(t, quadrantPattern, quadrantColors)
	stream := encode8x8Stream(t, w, h, 26, true, y, cb, cr, 2)

	ff := cuFFmpegDecode(t, stream)
	own := cuHi265Decode(t, stream, 3)

	if !bytes.Equal(ff, own) {
		mean, maxDiff := cuPlaneDiff(t, own, ff)
		t.Fatalf("hi265dec and FFmpeg disagree: mean=%.3f max=%d", mean, maxDiff)
	}

	frameSize := w*h + 2*(w/2)*(h/2)
	if len(own) != 3*frameSize {
		t.Fatalf("decoded %d bytes, want %d", len(own), 3*frameSize)
	}
	for i := 1; i < 3; i++ {
		if !bytes.Equal(own[:frameSize], own[i*frameSize:(i+1)*frameSize]) {
			t.Errorf("frame %d differs from the IDR frame it should repeat", i)
		}
	}
}

// TestEncode16x16MatchesFFmpeg guards the default path: the same content coded
// as one 16x16 CU per CTU must still decode identically in FFmpeg and here.
func TestEncode16x16MatchesFFmpeg(t *testing.T) {
	grid, err := yuv.ParseGrid("ABCD,CDAB")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	f, err := yuv.BuildFrame(grid, quadrantColors)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	w, h := grid.Width*16, grid.Height*16

	stream := encode8x8Stream(t, w, h, 26, false, f.Y, f.Cb, f.Cr, 1)

	ff := cuFFmpegDecode(t, stream)
	own := cuHi265Decode(t, stream, 2)
	if !bytes.Equal(ff, own) {
		mean, maxDiff := cuPlaneDiff(t, own, ff)
		t.Fatalf("hi265dec and FFmpeg disagree: mean=%.3f max=%d", mean, maxDiff)
	}

	frameSize := w*h + 2*(w/2)*(h/2)
	mean, maxDiff := cuPlaneDiff(t, own[:frameSize], cuIntendedYUV(f.Y, f.Cb, f.Cr))
	t.Logf("16x16 path vs intended pattern: mean=%.3f max=%d", mean, maxDiff)
	if maxDiff > 8 {
		t.Errorf("max error %d vs intended pattern is too large", maxDiff)
	}
}

// TestGenerate16x16BytesUnchanged pins the default (16x16) generator output, so
// that gating 8x8 granularity behind Use8x8CU cannot silently change the
// bitstreams every existing test depends on. The digest was taken from a tree
// carrying the intra boundary filter fix but none of the 8x8 code, and the 8x8
// work reproduces it byte for byte.
func TestGenerate16x16BytesUnchanged(t *testing.T) {
	p := EncodeParams{Width: 64, Height: 32, QP: 26}
	grid, err := yuv.ParseGrid("ABCD,BCDA")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	colors := yuv.ColorMap{
		'A': yuv.Color{Y: 235, Cb: 128, Cr: 128},
		'B': yuv.Color{Y: 16, Cb: 128, Cr: 128},
		'C': yuv.Color{Y: 81, Cb: 90, Cr: 240},
		'D': yuv.Color{Y: 145, Cb: 54, Cr: 34},
	}
	ps, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	pskip, err := GeneratePSkip(p, 1)
	if err != nil {
		t.Fatalf("GeneratePSkip: %v", err)
	}
	stream := append(append(append([]byte{}, ps...), idr...), pskip...)

	const want = "9eae464b1df166a463379fa92979adfadf0298fc683511389091c5e0d2c708c2"
	got := sha256.Sum256(stream)
	if hex.EncodeToString(got[:]) != want {
		t.Errorf("default 16x16 output changed:\n got %x (%d bytes)\nwant %s",
			got, len(stream), want)
	}
}

// TestSPSBlockSizes checks the coding and transform block sizes the SPS
// announces in both modes. MinTbLog2SizeY must stay strictly below
// MinCbLog2SizeY, which the 4x4 minimum transform block satisfies for both.
func TestSPSBlockSizes(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		use8x8                                 bool
		minCbMinus3, diffCb, minTbMinus2, diff int
	}{
		{"default", false, 1, 0, 0, 2}, // minCb=16, CTU=16, minTb=4, maxTb=16
		{"8x8", true, 0, 1, 0, 1},      // minCb=8,  CTU=16, minTb=4, maxTb=8
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := EncodeParams{Width: 32, Height: 32, QP: 26, Use8x8CU: tc.use8x8}
			ps, err := GenerateVPSSPSPPS(p)
			if err != nil {
				t.Fatalf("GenerateVPSSPSPPS: %v", err)
			}
			var sps *hevc.SPS
			for _, nalu := range avc.ExtractNalusFromByteStream(ps) {
				if len(nalu) >= 2 && hevc.GetNaluType(nalu[0]) == hevc.NALU_SPS {
					sps, err = hevc.ParseSPSNALUnit(nalu)
					if err != nil {
						t.Fatalf("ParseSPSNALUnit: %v", err)
					}
				}
			}
			if sps == nil {
				t.Fatal("no SPS in generated parameter sets")
			}
			if got := int(sps.Log2MinLumaCodingBlockSizeMinus3); got != tc.minCbMinus3 {
				t.Errorf("log2_min_luma_coding_block_size_minus3 = %d, want %d", got, tc.minCbMinus3)
			}
			if got := int(sps.Log2DiffMaxMinLumaCodingBlockSize); got != tc.diffCb {
				t.Errorf("log2_diff_max_min_luma_coding_block_size = %d, want %d", got, tc.diffCb)
			}
			if got := int(sps.Log2MinLumaTransformBlockSizeMinus2); got != tc.minTbMinus2 {
				t.Errorf("log2_min_luma_transform_block_size_minus2 = %d, want %d", got, tc.minTbMinus2)
			}
			if got := int(sps.Log2DiffMaxMinLumaTransformBlockSize); got != tc.diff {
				t.Errorf("log2_diff_max_min_luma_transform_block_size = %d, want %d", got, tc.diff)
			}
		})
	}
}

// TestEncodeIDRFromSPSPPS8x8 covers the external parameter set path: when the
// caller's SPS says minCbSize 8, the IDR slice must split every CTU, which
// idrSliceParamsFromSPSPPS derives from the SPS alone.
func TestEncodeIDRFromSPSPPS8x8(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26, Use8x8CU: true}
	ps, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}

	spsMap := make(map[uint32]*hevc.SPS)
	var sps *hevc.SPS
	var pps *hevc.PPS
	for _, nalu := range avc.ExtractNalusFromByteStream(ps) {
		if len(nalu) < 2 {
			continue
		}
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_SPS:
			sps, err = hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		}
	}
	if sps == nil || pps == nil {
		t.Fatal("failed to parse SPS/PPS from generated parameter sets")
	}

	grid, err := yuv.ParseGrid("AB,CD")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, quadrantColors)
	if err != nil {
		t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
	}
	stream := append(append([]byte{}, ps...), idr...)

	ff := cuFFmpegDecode(t, stream)
	own := cuHi265Decode(t, stream, 1)
	if !bytes.Equal(ff, own) {
		mean, maxDiff := cuPlaneDiff(t, own, ff)
		t.Fatalf("hi265dec and FFmpeg disagree: mean=%.3f max=%d", mean, maxDiff)
	}

	f, err := yuv.BuildFrame(grid, quadrantColors)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	mean, maxDiff := cuPlaneDiff(t, own, cuIntendedYUV(f.Y, f.Cb, f.Cr))
	t.Logf("external SPS/PPS 8x8 vs intended pattern: mean=%.3f max=%d", mean, maxDiff)
	if maxDiff > 12 {
		t.Errorf("max error %d vs intended pattern is too large", maxDiff)
	}
}
