package loopfilter

import (
	"testing"

	"github.com/Eyevinn/hi265/internal/tiles"
)

const log2Ctb = 6 // 64x64 CTBs

// grid2x2 builds a 128x128 picture (2x2 CTBs) cut into four 1-CTB tiles, with
// one slice per tile in tile scan order, and returns the boundaries.
func grid2x2(t *testing.T, acrossTiles bool, acrossSlices []bool) *Boundaries {
	t.Helper()
	g, err := tiles.New(2, 2, []int{1, 1}, []int{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	b := New(g, log2Ctb, acrossTiles)
	// Tile scan order over 1-CTB tiles is raster order here: CTB 0, 1, 2, 3.
	for i, across := range acrossSlices {
		idx := b.AddSlice(Slice{AcrossSlices: across})
		b.ClaimSegment(i, 1, idx)
	}
	return b
}

// Sample positions either side of the vertical tile boundary at x = 64, and
// either side of the horizontal one at y = 64.
const (
	leftX, rightX  = 63, 64
	aboveY, belowY = 63, 64
)

func TestSingleTileSingleSliceFiltersEverywhere(t *testing.T) {
	b := New(tiles.Single(2, 2), log2Ctb, false)
	idx := b.AddSlice(Slice{})
	for rs := range 4 {
		b.ClaimSegment(rs, 1, idx)
	}
	if !b.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("one tile, one slice: a CTB boundary inside it must be filterable")
	}
	if !b.CanFilterLuma(0, aboveY, 0, belowY) {
		t.Error("one tile, one slice: horizontal CTB boundary must be filterable")
	}
}

func TestTileBoundaryBlocksWhenAcrossTilesDisabled(t *testing.T) {
	// All slices permit filtering across slice boundaries, so only the tile rule
	// can block: this is the shape a tile-stitching tool produces.
	permissive := []bool{true, true, true, true}

	blocked := grid2x2(t, false, permissive)
	if blocked.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("loop_filter_across_tiles_enabled_flag = 0: vertical tile edge must not be filtered")
	}
	if blocked.CanFilterLuma(0, aboveY, 0, belowY) {
		t.Error("loop_filter_across_tiles_enabled_flag = 0: horizontal tile edge must not be filtered")
	}

	allowed := grid2x2(t, true, permissive)
	if !allowed.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("loop_filter_across_tiles_enabled_flag = 1: tile edge must be filtered")
	}
}

func TestSliceBoundaryBlocksWhenAcrossSlicesDisabled(t *testing.T) {
	// Tiles permit crossing, so only the slice rule can block.
	b := grid2x2(t, true, []bool{false, false, false, false})
	if b.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("slice_loop_filter_across_slices_enabled_flag = 0: slice edge must not be filtered")
	}
}

// Of two slices, it is the later one's flag that governs the boundary between
// them: for deblocking the edge is the later block's left or top boundary, and
// spec 8.7.3.2 names the same slice for SAO.
func TestLaterSliceDecides(t *testing.T) {
	// CTB 0 (slice 0) and CTB 1 (slice 1) share the vertical boundary at x=64.
	earlierPermits := grid2x2(t, true, []bool{true, false, true, true})
	if earlierPermits.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("the later slice forbids crossing, so the edge must not be filtered")
	}
	laterPermits := grid2x2(t, true, []bool{false, true, true, true})
	if !laterPermits.CanFilterLuma(leftX, 0, rightX, 0) {
		t.Error("the later slice permits crossing, so the earlier slice's flag must not matter")
	}
}

// Chroma positions are half-resolution, and must resolve to the same CTBs as
// the luma samples they cover.
func TestChromaUsesTheSameBoundaries(t *testing.T) {
	b := grid2x2(t, false, []bool{true, true, true, true})
	if b.CanFilterChroma(leftX/2, 0, rightX/2, 0) {
		t.Error("chroma across a tile edge must not be filtered either")
	}
	if !b.CanFilterChroma(0, 0, 1, 0) {
		t.Error("chroma inside one tile must be filterable")
	}
}

func TestSamePositionAndSameCtbAlwaysFilterable(t *testing.T) {
	b := grid2x2(t, false, []bool{false, false, false, false})
	if !b.CanFilterLuma(10, 10, 11, 10) {
		t.Error("two samples in the same CTB are never separated by a boundary")
	}
}

func TestSliceParamsAndAnyDeblocking(t *testing.T) {
	g, err := tiles.New(2, 1, []int{1, 1}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	b := New(g, log2Ctb, false)
	off := b.AddSlice(Slice{DeblockingDisabled: true, BetaOffset: 4, TcOffset: -2})
	on := b.AddSlice(Slice{BetaOffset: 6})
	b.ClaimSegment(0, 1, off)
	b.ClaimSegment(1, 1, on)

	if s := b.SliceAtLuma(0, 0); !s.DeblockingDisabled || s.BetaOffset != 4 || s.TcOffset != -2 {
		t.Errorf("first tile: got %+v", s)
	}
	if s := b.SliceAtLuma(64, 0); s.DeblockingDisabled || s.BetaOffset != 6 {
		t.Errorf("second tile: got %+v", s)
	}
	if !b.AnyDeblocking() {
		t.Error("one slice has deblocking enabled, so the filter must run")
	}

	allOff := New(g, log2Ctb, false)
	allOff.ClaimSegment(0, 2, allOff.AddSlice(Slice{DeblockingDisabled: true}))
	if allOff.AnyDeblocking() {
		t.Error("every slice disables deblocking, so the filter must be skipped")
	}
}
