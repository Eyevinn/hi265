package tiles

import (
	"slices"
	"testing"
)

// A single tile means tile scan is raster scan, which the rest of the decoder
// relies on to use the tables unconditionally.
func TestSingleTileIsIdentity(t *testing.T) {
	g := Single(5, 3)
	if g.NumTiles() != 1 {
		t.Fatalf("NumTiles = %d, want 1", g.NumTiles())
	}
	for rs := range g.NumCtbs() {
		if got := g.RsToTs(rs); got != rs {
			t.Errorf("RsToTs(%d) = %d, want %d", rs, got, rs)
		}
		if got := g.TsToRs(rs); got != rs {
			t.Errorf("TsToRs(%d) = %d, want %d", rs, got, rs)
		}
		if got := g.TileIDOfRs(rs); got != 0 {
			t.Errorf("TileIDOfRs(%d) = %d, want 0", rs, got)
		}
	}
}

// The 2x2 grid over 4x4 CTBs that kvazaar --tiles 2x2 emits for 256x256 with a
// 64-luma CTB. The four slice segment addresses in that stream are 0, 2, 8, 10,
// which are exactly the raster addresses of the four tiles' first CTBs.
func TestTileScan2x2(t *testing.T) {
	g, err := New(4, 4, []int{2, 2}, []int{2, 2})
	if err != nil {
		t.Fatal(err)
	}
	// Tile scan visits each tile's CTBs in raster order within the tile.
	wantTsToRs := []int{
		0, 1, 4, 5, // tile 0
		2, 3, 6, 7, // tile 1
		8, 9, 12, 13, // tile 2
		10, 11, 14, 15, // tile 3
	}
	got := make([]int, g.NumCtbs())
	for ts := range g.NumCtbs() {
		got[ts] = g.TsToRs(ts)
	}
	if !slices.Equal(got, wantTsToRs) {
		t.Errorf("tile scan = %v, want %v", got, wantTsToRs)
	}
	// RsToTs must invert it.
	for ts, rs := range wantTsToRs {
		if g.RsToTs(rs) != ts {
			t.Errorf("RsToTs(%d) = %d, want %d", rs, g.RsToTs(rs), ts)
		}
	}
	for _, tc := range []struct{ rs, tile int }{{0, 0}, {5, 0}, {2, 1}, {7, 1}, {8, 2}, {13, 2}, {10, 3}, {15, 3}} {
		if got := g.TileIDOfRs(tc.rs); got != tc.tile {
			t.Errorf("TileIDOfRs(%d) = %d, want %d", tc.rs, got, tc.tile)
		}
	}
	if g.SameTileRs(1, 2) {
		t.Error("CTBs 1 and 2 straddle the tile column boundary but compare same-tile")
	}
	if !g.SameTileRs(1, 4) {
		t.Error("CTBs 1 and 4 are both in tile 0 but compare different-tile")
	}
}

// Uniform spacing is not equal division. 5 CTBs in 2 columns is 2 then 3, and a
// decoder that divided equally would put the boundary in the wrong place for
// every picture whose CTB count does not divide evenly.
func TestUniformSizesAreNotEqualDivision(t *testing.T) {
	for _, tc := range []struct {
		ctbs, n int
		want    []int
	}{
		{5, 2, []int{2, 3}},
		{4, 2, []int{2, 2}},
		{7, 3, []int{2, 2, 3}},
	} {
		if got := UniformSizes(tc.ctbs, tc.n); !slices.Equal(got, tc.want) {
			t.Errorf("UniformSizes(%d, %d) = %v, want %v", tc.ctbs, tc.n, got, tc.want)
		}
	}
}

func TestExplicitSizesDeriveTheLast(t *testing.T) {
	got, err := ExplicitSizes(5, []int{2})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{2, 3}; !slices.Equal(got, want) {
		t.Errorf("ExplicitSizes(5, [2]) = %v, want %v", got, want)
	}
	if _, err := ExplicitSizes(5, []int{3, 2}); err == nil {
		t.Error("signalled sizes filling the picture leave nothing for the last tile, want error")
	}
}

// A 2x1 grid on the non-equal split, since that is the fixture the doc calls for.
func TestTileScanNonEqualColumns(t *testing.T) {
	g, err := New(5, 2, UniformSizes(5, 2), []int{2})
	if err != nil {
		t.Fatal(err)
	}
	// Tile 0 is 2 CTBs wide, tile 1 is 3.
	wantTsToRs := []int{0, 1, 5, 6, 2, 3, 4, 7, 8, 9}
	got := make([]int, g.NumCtbs())
	for ts := range g.NumCtbs() {
		got[ts] = g.TsToRs(ts)
	}
	if !slices.Equal(got, wantTsToRs) {
		t.Errorf("tile scan = %v, want %v", got, wantTsToRs)
	}
}

func TestMismatchedSizesRejected(t *testing.T) {
	// More tiles than CTBs is non-conformant (spec 7.4.3.3 bounds the tile
	// counts by the picture size), and the uniform formula yields an empty
	// first tile for it, so New must refuse rather than build a broken table.
	if _, err := New(3, 1, UniformSizes(3, 4), []int{1}); err == nil {
		t.Error("4 tile columns across 3 CTBs, want error")
	}
	if _, err := New(4, 4, []int{2, 3}, []int{2, 2}); err == nil {
		t.Error("column widths summing to 5 in a 4-CTB-wide picture, want error")
	}
	if _, err := New(4, 4, []int{2, 2}, nil); err == nil {
		t.Error("no row heights, want error")
	}
}
