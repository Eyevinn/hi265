package decoder

import (
	"os"
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
