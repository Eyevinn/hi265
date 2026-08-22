package decoder

import (
	"os"
	"strings"
	"testing"

	"github.com/Eyevinn/hi265/pkg/frame"
)

func testGolden(t *testing.T, bitstreamPath, goldenPath string, width, height int) {
	t.Helper()
	testGoldenFrames(t, bitstreamPath, goldenPath, width, height, 1)
}

func testGoldenFrames(t *testing.T, bitstreamPath, goldenPath string, width, height, numFrames int) {
	t.Helper()

	bitstreamData, err := os.ReadFile(bitstreamPath)
	if err != nil {
		t.Fatalf("read bitstream: %v", err)
	}

	goldenData, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	dec := New()
	frames, err := dec.DecodeAnnexB(bitstreamData)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(frames) != numFrames {
		t.Fatalf("expected %d frames, got %d", numFrames, len(frames))
	}

	// Concatenate all frames' YUV data
	var decoded []byte
	for _, f := range frames {
		if f.Width != width || f.Height != height {
			t.Fatalf("expected %dx%d, got %dx%d", width, height, f.Width, f.Height)
		}
		decoded = append(decoded, f.YUV420Bytes()...)
	}

	if len(decoded) != len(goldenData) {
		t.Fatalf("decoded size %d != golden size %d", len(decoded), len(goldenData))
	}

	frameSize := width*height + 2*(width/2)*(height/2)
	for fi := range numFrames {
		compareFrame(t, decoded[fi*frameSize:(fi+1)*frameSize],
			goldenData[fi*frameSize:(fi+1)*frameSize], width, height, fi)
	}
}

func compareFrame(t *testing.T, decoded, golden []byte, width, height, frameIdx int) {
	t.Helper()
	_ = frame.Frame{} // ensure import is used

	lumaSize := width * height
	chromaSize := (width / 2) * (height / 2)
	mismatches := 0
	for i := range decoded {
		if decoded[i] != golden[i] {
			if mismatches < 10 {
				plane := "Y"
				pos := i
				if i >= lumaSize+chromaSize {
					plane = "Cr"
					pos = i - lumaSize - chromaSize
				} else if i >= lumaSize {
					plane = "Cb"
					pos = i - lumaSize
				}
				t.Errorf("frame %d: mismatch at %s[%d]: got 0x%02x, want 0x%02x",
					frameIdx, plane, pos, decoded[i], golden[i])
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		for i := range decoded {
			if decoded[i] != golden[i] {
				plane := "Y"
				pos := i
				if i >= lumaSize+chromaSize {
					plane = "Cr"
					pos = i - lumaSize - chromaSize
				} else if i >= lumaSize {
					plane = "Cb"
					pos = i - lumaSize
				}
				x := pos % width
				y := pos / width
				if plane != "Y" {
					x = pos % (width / 2)
					y = pos / (width / 2)
				}
				t.Logf("frame %d: mismatch %s[%d] (x=%d,y=%d): got 0x%02x, want 0x%02x, diff=%d",
					frameIdx, plane, pos, x, y, decoded[i], golden[i], int(decoded[i])-int(golden[i]))
			}
		}
		t.Fatalf("frame %d: total mismatches: %d out of %d bytes", frameIdx, mismatches, len(decoded))
	}
}

func TestDecodeBlack16x16(t *testing.T) {
	testGolden(t, "../../testdata/black_16x16.265", "../../testdata/golden/black_16x16.yuv", 16, 16)
}

func TestDecodeGray16x16(t *testing.T) {
	testGolden(t, "../../testdata/gray_16x16.265", "../../testdata/golden/gray_16x16.yuv", 16, 16)
}

func TestDecodeBlack32x32(t *testing.T) {
	testGolden(t, "../../testdata/black_32x32.265", "../../testdata/golden/black_32x32.yuv", 32, 32)
}

func TestDecodeRed16x16(t *testing.T) {
	testGolden(t, "../../testdata/red_16x16.265", "../../testdata/golden/red_16x16.yuv", 16, 16)
}

func TestDecodeColorGrid32x32(t *testing.T) {
	testGolden(t, "../../testdata/colorgrid_32x32.265", "../../testdata/golden/colorgrid_32x32.yuv", 32, 32)
}

func TestDecodeChecker16x16(t *testing.T) {
	testGolden(t, "../../testdata/checker_16x16.265", "../../testdata/golden/checker_16x16.yuv", 16, 16)
}

func TestDecodeGradient64x64(t *testing.T) {
	testGolden(t, "../../testdata/gradient_64x64.265", "../../testdata/golden/gradient_64x64.yuv", 64, 64)
}

func TestDecodeGray128x64(t *testing.T) {
	testGolden(t, "../../testdata/gray_128x64.265", "../../testdata/golden/gray_128x64.yuv", 128, 64)
}

func TestDecodeSinCos128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_128x64.265", "../../testdata/golden/sincos_128x64.yuv", 128, 64)
}

func TestDecodeGrayDeblock128x64(t *testing.T) {
	testGolden(t, "../../testdata/gray_deblock_128x64.265", "../../testdata/golden/gray_deblock_128x64.yuv", 128, 64)
}

func TestDecodeSinCosDeblock128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_deblock_128x64.265",
		"../../testdata/golden/sincos_deblock_128x64.yuv", 128, 64)
}

func TestDecodeSinCosSignHide128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_signhide_128x64.265",
		"../../testdata/golden/sincos_signhide_128x64.yuv", 128, 64)
}

func TestDecodeSinCosTSkip128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_tskip_128x64.265", "../../testdata/golden/sincos_tskip_128x64.yuv", 128, 64)
}

func TestDecodeSinCosSao128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_sao_128x64.265", "../../testdata/golden/sincos_sao_128x64.yuv", 128, 64)
}

func TestDecodeSinCosCRF32_128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_crf32_128x64.265", "../../testdata/golden/sincos_crf32_128x64.yuv", 128, 64)
}

func TestDecodeGray2Frames128x64(t *testing.T) {
	testGoldenFrames(t, "../../testdata/gray_2frames_128x64.265",
		"../../testdata/golden/gray_2frames_128x64.yuv", 128, 64, 2)
}

// Tiled pictures, one slice segment per tile — the shape kvazaar's
// "--tiles WxH --slices tiles" produces and the one `hevc-retiler` stitches.
// Both loop filters are off in these vectors so that tile geometry, tile scan
// order and neighbour availability are the only things under test; filtering
// across a tile boundary is a separate matter (docs/tiles-decoding.md, T5).

// Two tile columns over two frames, which also covers closing one picture when
// the next picture's first slice segment arrives.
func TestDecodeTiles2x1TwoFrames128x64(t *testing.T) {
	testGoldenFrames(t, "../../testdata/tiles_2x1_2frames_128x64.265",
		"../../testdata/golden/tiles_2x1_2frames_128x64.yuv", 128, 64, 2)
}

// A 2x2 grid, so the picture has both a vertical and a horizontal tile
// boundary and tile scan differs from raster scan in both directions.
func TestDecodeTiles2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_128x128.265",
		"../../testdata/golden/tiles_2x2_128x128.yuv", 128, 128)
}

// Uniform spacing that is not an equal division: 3 CTBs in 2 tile columns is
// one CTB then two, per the spec 6.5.1 formula. A decoder that divided equally
// would put the tile boundary in the wrong place.
func TestDecodeTilesNonEqual192x64(t *testing.T) {
	testGolden(t, "../../testdata/tiles_nonequal_192x64.265",
		"../../testdata/golden/tiles_nonequal_192x64.yuv", 192, 64)
}

// The same 2x2 grid with one loop filter on at a time. These are the vectors
// that pin the boundary rules of spec 8.7.2 and 8.7.3.2: with
// loop_filter_across_tiles_enabled_flag equal to 0, neither filter may reach
// across a tile edge, so a decoder that filtered through it would differ from
// FFmpeg along the seams and nowhere else.
func TestDecodeTilesDeblock2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_deblock_128x128.265",
		"../../testdata/golden/tiles_2x2_deblock_128x128.yuv", 128, 128)
}

func TestDecodeTilesSao2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_sao_128x128.265",
		"../../testdata/golden/tiles_2x2_sao_128x128.yuv", 128, 128)
}

// Several tiles in one slice segment, reached through entry point offsets —
// kvazaar's default when tiles are enabled, and what most encoders emit. Each
// tile is its own CABAC substream: the contexts are re-initialised, QP
// prediction restarts and no earlier tile is available for prediction.
func TestDecodeTilesMultiPerSegment2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_2x2_128x128.yuv", 128, 128)
}

// The same shape with both loop filters on. One slice covers every tile here,
// so slice_loop_filter_across_slices_enabled_flag cannot protect a seam — this
// is the vector that pins the tile rule on its own.
func TestDecodeTilesMultiPerSegmentFilters2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_filters_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_filters_2x2_128x128.yuv", 128, 128)
}

// A delta-QP map makes cu_qp_delta live, which is what makes the per-tile reset
// of qPY_PREV (spec 8.6.1) observable: without it the predicted QP walks in from
// the previous tile and every coefficient dequantises against the wrong step.
func TestDecodeTilesMultiPerSegmentQP2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_qp_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_qp_2x2_128x128.yuv", 128, 128)
}

// A slice is one independent slice segment plus any number of dependent ones
// (spec 7.4.7.1). A dependent segment carries almost no header: it continues its
// predecessor's CABAC contexts, QP prediction and neighbour maps, and a boundary
// between two segments of one slice is not a prediction or filtering boundary at
// all. "kvazaar --slices wpp" puts each CTB row in a dependent segment.
func TestDecodeDependentSegmentsWpp128x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_dep_wpp_128x128.265",
		"../../testdata/golden/slices_dep_wpp_128x128.yuv", 128, 128)
}

// The same stream with deblocking on, which pins the filtering half: treating a
// dependent segment boundary as a slice boundary leaves the row seam unfiltered.
func TestDecodeDependentSegmentsDeblock128x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_dep_wpp_deblock_128x128.265",
		"../../testdata/golden/slices_dep_wpp_deblock_128x128.yuv", 128, 128)
}

// Two *independent* slices with wavefront parallel processing, from x265
// --wpp --slices 2. Each slice indexes its entry point offsets from its own
// first CTB row, and the second slice must not inherit the first's wavefront
// snapshot: the row above it belongs to another slice, so spec 6.4.1 makes it
// unavailable and the contexts start from their initial values.
func TestDecodeTwoSlicesWpp256x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_wpp_2slices_256x128.265",
		"../../testdata/golden/slices_wpp_2slices_256x128.yuv", 256, 128)
}

// A P picture with real motion vectors must be refused rather than
// approximated. Only zero-motion skip CUs are reconstructed, so an inter CU that
// carries a motion vector, a merge candidate or a reference index has no
// prediction to build from — and returning the picture anyway would hand the
// caller something that looks like a decode and is not one.
func TestDecodeInterMotionIsRefused(t *testing.T) {
	data, err := os.ReadFile("../../testdata/pframe_motion_128x64.265")
	if err != nil {
		t.Fatal(err)
	}
	frames, err := New().DecodeAnnexB(data)
	if err == nil {
		t.Fatalf("expected the inter picture to be refused, got %d frames", len(frames))
	}
	if !strings.Contains(err.Error(), "motion compensation is not implemented") {
		t.Errorf("the error should name the limitation, got: %v", err)
	}
}
