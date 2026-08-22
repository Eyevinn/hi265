package encode

import (
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
)

// A grid cell is one 16x16 CTU, so yuv.BuildFrame lays a grid out at
// grid.Width*16 samples per row. gridSource has to hand the slice writers those
// samples repacked at the picture's own width, because that is the stride they
// index the planes at — the top-left crop, sample for sample, of what the grid
// describes.
//
// Checking against the frame's own strided layout rather than against
// YUV420Bytes keeps this independent of how the crop is implemented.
func TestGridSourceRepacksToPictureWidth(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cells         int // grid width and height in CTUs
		width, height int
	}{
		// The picture is narrower than the grid: the rightmost CTU column is
		// partly outside it and every row has to move.
		{"narrower_than_grid", 2, 24, 24},
		{"narrower_and_shorter", 8, 120, 72},
		{"one_partial_ctu", 1, 8, 8},
		// Exactly the grid, which is the common case and needs no copy.
		{"exactly_the_grid", 8, 128, 128},
		// A short bottom CTB row only: the stride already matches, so only rows
		// are dropped.
		{"short_bottom_row", 8, 128, 72},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grid, colors, err := buildGrid(patTiles, 0, tc.cells, tc.cells)
			if err != nil {
				t.Fatal(err)
			}
			want, err := yuv.BuildFrame(grid, colors)
			if err != nil {
				t.Fatal(err)
			}

			y, cb, cr, err := gridSource(grid, colors, tc.width, tc.height)
			if err != nil {
				t.Fatalf("gridSource: %v", err)
			}
			if len(y) != tc.width*tc.height {
				t.Fatalf("luma is %d samples, want %d", len(y), tc.width*tc.height)
			}
			chromaW, chromaH := tc.width/2, tc.height/2
			if len(cb) != chromaW*chromaH || len(cr) != chromaW*chromaH {
				t.Fatalf("chroma is %d/%d samples, want %d", len(cb), len(cr), chromaW*chromaH)
			}

			for row := range tc.height {
				for col := range tc.width {
					if got, exp := y[row*tc.width+col], want.Y[row*want.StrideY+col]; got != exp {
						t.Fatalf("luma (%d,%d) is %d, want %d", col, row, got, exp)
					}
				}
			}
			for row := range chromaH {
				for col := range chromaW {
					if got, exp := cb[row*chromaW+col], want.Cb[row*want.StrideC+col]; got != exp {
						t.Fatalf("Cb (%d,%d) is %d, want %d", col, row, got, exp)
					}
					if got, exp := cr[row*chromaW+col], want.Cr[row*want.StrideC+col]; got != exp {
						t.Fatalf("Cr (%d,%d) is %d, want %d", col, row, got, exp)
					}
				}
			}
		})
	}
}

// A grid smaller than the picture cannot supply its samples. Reading past the
// buffer used to panic; it is an error the caller can act on now.
func TestGridSourceRejectsUndersizedGrid(t *testing.T) {
	grid, colors, err := buildGrid(patTiles, 0, 2, 2) // 32x32 samples
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ width, height int }{
		{48, 32}, // too wide
		{32, 48}, // too tall
		{48, 48},
	} {
		if _, _, _, err := gridSource(grid, colors, tc.width, tc.height); err == nil {
			t.Errorf("expected a %dx%d picture from a 32x32 grid to be refused", tc.width, tc.height)
		}
	}
}
