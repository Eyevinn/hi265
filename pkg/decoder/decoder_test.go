package decoder

import (
	"os"
	"testing"
)

func testGolden(t *testing.T, bitstreamPath, goldenPath string, width, height int) {
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
	f, err := dec.DecodeAnnexB(bitstreamData)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if f.Width != width || f.Height != height {
		t.Fatalf("expected %dx%d, got %dx%d", width, height, f.Width, f.Height)
	}

	decoded := f.YUV420Bytes()
	if len(decoded) != len(goldenData) {
		t.Fatalf("decoded size %d != golden size %d", len(decoded), len(goldenData))
	}

	lumaSize := width * height
	chromaSize := (width / 2) * (height / 2)
	mismatches := 0
	for i := range decoded {
		if decoded[i] != goldenData[i] {
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
				t.Errorf("mismatch at %s[%d]: got 0x%02x, want 0x%02x", plane, pos, decoded[i], goldenData[i])
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		t.Fatalf("total mismatches: %d out of %d bytes", mismatches, len(decoded))
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
