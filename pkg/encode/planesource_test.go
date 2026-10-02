package encode

import (
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
)

// An odd number of 8x8 blocks covers a picture whose size is a multiple of 8 but
// not of 16. yuv.BuildFrameFromPlaneGrid rounds its frame up to whole 16x16
// blocks, so that frame is larger than what the blocks paint, and the strip in
// between is zero samples. What the blocks cover is accepted and repacked; the
// strip has to be refused rather than encoded as picture.
func TestPlaneSourceCountsCoverageInBlocks(t *testing.T) {
	pg := yuv.NewPlaneGrid(5, 3, 8) // 40x24 samples, in a 48x32 frame
	for row := range pg.Height {
		for col := range pg.Width {
			pg.Y[row][col] = uint8(16 + 10*col + row)
			pg.Cb[row][col] = uint8(100 + col)
			pg.Cr[row][col] = uint8(150 + row)
		}
	}

	const width, height = 40, 24
	y, cb, cr, err := planeSource(pg, width, height)
	if err != nil {
		t.Fatalf("planeSource at %dx%d: %v", width, height, err)
	}
	for row := range height {
		for col := range width {
			if got, want := y[row*width+col], pg.Y[row/8][col/8]; got != want {
				t.Fatalf("luma (%d,%d) is %d, want %d", col, row, got, want)
			}
		}
	}
	chromaW, chromaH := width/2, height/2
	for row := range chromaH {
		for col := range chromaW {
			if got, want := cb[row*chromaW+col], pg.Cb[row/4][col/4]; got != want {
				t.Fatalf("Cb (%d,%d) is %d, want %d", col, row, got, want)
			}
			if got, want := cr[row*chromaW+col], pg.Cr[row/4][col/4]; got != want {
				t.Fatalf("Cr (%d,%d) is %d, want %d", col, row, got, want)
			}
		}
	}

	for _, tc := range []struct{ width, height int }{
		{48, 24}, // the frame's width, 8 samples more than the blocks paint
		{40, 32}, // the frame's height
		{48, 32},
	} {
		if _, _, _, err := planeSource(pg, tc.width, tc.height); err == nil {
			t.Errorf("expected a %dx%d picture from 40x24 samples of blocks to be refused",
				tc.width, tc.height)
		}
	}
}

// PlaneGrid documents block sizes of 8 and 16 only. Before they were refused, a
// block size of 4 encoded a picture that was mostly zero samples, and 32 panicked
// inside hi264, writing past the frame it had allocated.
func TestGenerateIDRFromPlaneRejectsBlockSize(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16}
	for _, bs := range []int{4, 32} {
		pg := yuv.NewPlaneGrid(8, 8, bs) // covers the picture at either size
		_, err := GenerateIDRFromPlane(p, pg)
		if err == nil || !strings.Contains(err.Error(), "block size") {
			t.Errorf("block size %d: got error %v, want it refused for its block size", bs, err)
		}
	}
}
