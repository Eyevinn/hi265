// Package loopfilter carries the picture structure that the deblocking and SAO
// filters need in order to stop in the right places.
//
// Both filters read samples across block boundaries, and both are forbidden
// from reaching across some of them: a tile boundary when
// loop_filter_across_tiles_enabled_flag is 0, and a slice boundary when the
// slice's own slice_loop_filter_across_slices_enabled_flag is 0 (spec 8.7.2 for
// deblocking, 8.7.3 for SAO). Tiles and slices are both made of whole CTBs, so
// answering "may these two samples be filtered together?" is a question about
// the two CTBs that contain them.
//
// The per-slice filter parameters live here too. The spec lets deblocking be
// disabled, and its beta and tC offsets be chosen, per slice — a tiled picture
// stitched from separately encoded streams routinely has different values in
// different tiles.
package loopfilter

import "github.com/Eyevinn/hi265/internal/tiles"

// Slice holds one slice's loop filter parameters.
type Slice struct {
	DeblockingDisabled bool
	BetaOffset         int  // slice_beta_offset_div2 * 2
	TcOffset           int  // slice_tc_offset_div2 * 2
	AcrossSlices       bool // slice_loop_filter_across_slices_enabled_flag
}

// Boundaries maps every CTB of one picture to its tile and its slice.
type Boundaries struct {
	grid        *tiles.Grid
	log2CtbSize int
	acrossTiles bool
	sliceOf     []int // per CTB in raster order, index into slices; -1 if unclaimed
	slices      []Slice
}

// New prepares the boundaries of a picture with the given tile grid.
// acrossTiles is the PPS loop_filter_across_tiles_enabled_flag.
func New(grid *tiles.Grid, log2CtbSize int, acrossTiles bool) *Boundaries {
	sliceOf := make([]int, grid.NumCtbs())
	for i := range sliceOf {
		sliceOf[i] = -1
	}
	return &Boundaries{
		grid:        grid,
		log2CtbSize: log2CtbSize,
		acrossTiles: acrossTiles,
		sliceOf:     sliceOf,
	}
}

// AddSlice records a slice's filter parameters and returns its index. Slices
// must be added in decoding order, which is what makes the index usable as an
// ordering: of two CTBs in different slices, the one with the larger index is
// the later of the two, and it is that slice's flag which governs the boundary
// between them (spec 8.7.2, 8.7.3.2).
func (b *Boundaries) AddSlice(s Slice) int {
	b.slices = append(b.slices, s)
	return len(b.slices) - 1
}

// ClaimSegment assigns a slice segment's CTBs to slice index idx. A segment
// covers a run of ctbCount consecutive tile scan addresses beginning at raster
// address addrRS.
func (b *Boundaries) ClaimSegment(addrRS, ctbCount, idx int) {
	first := b.grid.RsToTs(addrRS)
	for ts := first; ts < first+ctbCount && ts < b.grid.NumCtbs(); ts++ {
		b.sliceOf[b.grid.TsToRs(ts)] = idx
	}
}

// AnyDeblocking reports whether any slice of the picture has deblocking
// enabled, so a caller can skip the filter altogether.
func (b *Boundaries) AnyDeblocking() bool {
	for _, s := range b.slices {
		if !s.DeblockingDisabled {
			return true
		}
	}
	return false
}

// SliceAtLuma returns the filter parameters of the slice covering a luma
// sample. Positions outside the picture return the first slice's.
func (b *Boundaries) SliceAtLuma(x, y int) Slice {
	return b.sliceAt(b.ctbOfLuma(x, y))
}

// CanFilterLuma reports whether a filter may combine two luma samples.
func (b *Boundaries) CanFilterLuma(xA, yA, xB, yB int) bool {
	return b.canFilter(b.ctbOfLuma(xA, yA), b.ctbOfLuma(xB, yB))
}

// CanFilterChroma reports whether a filter may combine two chroma samples,
// whose positions are in chroma units (4:2:0).
func (b *Boundaries) CanFilterChroma(xA, yA, xB, yB int) bool {
	return b.canFilter(b.ctbOfLuma(2*xA, 2*yA), b.ctbOfLuma(2*xB, 2*yB))
}

func (b *Boundaries) ctbOfLuma(x, y int) int {
	cx, cy := x>>b.log2CtbSize, y>>b.log2CtbSize
	if cx < 0 {
		cx = 0
	}
	if cy < 0 {
		cy = 0
	}
	if cx >= b.grid.CtbsX {
		cx = b.grid.CtbsX - 1
	}
	if cy >= b.grid.CtbsY {
		cy = b.grid.CtbsY - 1
	}
	return cy*b.grid.CtbsX + cx
}

func (b *Boundaries) sliceAt(ctbRS int) Slice {
	if len(b.slices) == 0 {
		return Slice{}
	}
	idx := b.sliceOf[ctbRS]
	if idx < 0 {
		idx = 0
	}
	return b.slices[idx]
}

// canFilter applies the two boundary rules to a pair of CTBs.
func (b *Boundaries) canFilter(ctbA, ctbB int) bool {
	if ctbA == ctbB {
		return true
	}
	if !b.acrossTiles && !b.grid.SameTileRs(ctbA, ctbB) {
		return false
	}
	sa, sb := b.sliceOf[ctbA], b.sliceOf[ctbB]
	if sa == sb || sa < 0 || sb < 0 {
		return true
	}
	// The later slice's flag decides: for deblocking the boundary is the current
	// block's left or top edge, and for SAO the spec names the flag of whichever
	// of the two samples comes later in decoding order.
	later := sa
	if sb > later {
		later = sb
	}
	return b.slices[later].AcrossSlices
}
