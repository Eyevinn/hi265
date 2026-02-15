package decoder

import (
	"os"
	"testing"
)

func TestDecodeBlack16x16(t *testing.T) {
	// Read test bitstream
	bitstreamData, err := os.ReadFile("../../testdata/black_16x16.265")
	if err != nil {
		t.Fatalf("read bitstream: %v", err)
	}

	// Read golden reference
	goldenData, err := os.ReadFile("../../testdata/golden/black_16x16.yuv")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	// Decode
	dec := New()
	f, err := dec.DecodeAnnexB(bitstreamData)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Check dimensions
	if f.Width != 16 || f.Height != 16 {
		t.Fatalf("expected 16x16, got %dx%d", f.Width, f.Height)
	}

	// Compare output
	decoded := f.YUV420Bytes()
	if len(decoded) != len(goldenData) {
		t.Fatalf("decoded size %d != golden size %d", len(decoded), len(goldenData))
	}

	mismatches := 0
	for i := range decoded {
		if decoded[i] != goldenData[i] {
			if mismatches < 10 {
				plane := "Y"
				pos := i
				if i >= 256 {
					if i >= 320 {
						plane = "Cr"
						pos = i - 320
					} else {
						plane = "Cb"
						pos = i - 256
					}
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
