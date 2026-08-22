// Package tiles derives the tile geometry and scan conversion tables of HEVC
// spec 6.5.1.
//
// A tiled picture is not coded in raster order: the CTBs of the first tile come
// first, in raster order within that tile, then those of the next tile. That is
// tile scan order, and decoding a tiled picture needs three lookups — raster to
// tile scan, tile scan back to raster, and which tile a CTB belongs to. All
// three collapse to the identity for the single-tile case, which is why the
// rest of the decoder can use them unconditionally.
package tiles

import "fmt"

// Grid holds the scan conversion tables for one picture's tile partitioning.
// The zero value is not usable; build one with New or Single.
type Grid struct {
	// CtbsX, CtbsY are the picture dimensions in CTBs.
	CtbsX, CtbsY int
	// colBd, rowBd are the tile boundaries in CTBs, with a trailing entry equal
	// to the picture size, so tile column i spans [colBd[i], colBd[i+1]).
	colBd, rowBd []int
	rsToTs       []int // CtbAddrRsToTs
	tsToRs       []int // CtbAddrTsToRs
	tileID       []int // TileId, indexed by tile scan address
}

// New builds the tables for a picture of ctbsX x ctbsY CTBs partitioned into
// the given tile column widths and row heights, both in CTBs. The widths must
// sum to ctbsX and the heights to ctbsY.
func New(ctbsX, ctbsY int, colWidths, rowHeights []int) (*Grid, error) {
	if ctbsX <= 0 || ctbsY <= 0 {
		return nil, fmt.Errorf("tiles: picture of %dx%d CTBs", ctbsX, ctbsY)
	}
	colBd, err := boundaries(colWidths, ctbsX, "column widths")
	if err != nil {
		return nil, err
	}
	rowBd, err := boundaries(rowHeights, ctbsY, "row heights")
	if err != nil {
		return nil, err
	}

	numCtbs := ctbsX * ctbsY
	g := &Grid{
		CtbsX:  ctbsX,
		CtbsY:  ctbsY,
		colBd:  colBd,
		rowBd:  rowBd,
		rsToTs: make([]int, numCtbs),
		tsToRs: make([]int, numCtbs),
		tileID: make([]int, numCtbs),
	}

	// Spec 6.5.1: a CTB's tile scan address is the number of CTBs in the tiles
	// that precede its own, plus its raster offset inside that tile.
	for rs := range numCtbs {
		tbX, tbY := rs%ctbsX, rs/ctbsX
		tileX, tileY := tileOf(colBd, tbX), tileOf(rowBd, tbY)

		ts := 0
		for i := 0; i < tileX; i++ {
			ts += rowHeight(rowBd, tileY) * colWidth(colBd, i)
		}
		for j := 0; j < tileY; j++ {
			ts += ctbsX * rowHeight(rowBd, j)
		}
		ts += (tbY-rowBd[tileY])*colWidth(colBd, tileX) + tbX - colBd[tileX]

		g.rsToTs[rs] = ts
		g.tsToRs[ts] = rs
		g.tileID[ts] = tileY*(len(colBd)-1) + tileX
	}
	return g, nil
}

// Single returns the trivial one-tile grid, where tile scan is raster scan.
func Single(ctbsX, ctbsY int) *Grid {
	g, err := New(ctbsX, ctbsY, []int{ctbsX}, []int{ctbsY})
	if err != nil {
		// Only reachable for a non-positive picture size, which callers derive
		// from an already validated SPS.
		panic(err)
	}
	return g
}

// UniformSizes returns the spec 6.5.1 tile sizes for uniform_spacing_flag = 1:
// n tiles across ctbs CTBs. This is not equal division — 5 CTBs in 2 columns is
// 2 and 3, and which of the two comes first is what the formula decides.
func UniformSizes(ctbs, n int) []int {
	sizes := make([]int, n)
	for i := range n {
		sizes[i] = (i+1)*ctbs/n - i*ctbs/n
	}
	return sizes
}

// ExplicitSizes returns the tile sizes for uniform_spacing_flag = 0, where the
// bitstream carries all but the last size and the last is whatever is left.
func ExplicitSizes(ctbs int, signalled []int) ([]int, error) {
	sizes := make([]int, len(signalled)+1)
	rest := ctbs
	for i, v := range signalled {
		if v <= 0 {
			return nil, fmt.Errorf("tiles: signalled tile size %d at index %d", v, i)
		}
		sizes[i] = v
		rest -= v
	}
	if rest <= 0 {
		return nil, fmt.Errorf("tiles: signalled tile sizes exceed the %d CTBs available", ctbs)
	}
	sizes[len(signalled)] = rest
	return sizes, nil
}

// NumTiles returns the number of tiles in the picture.
func (g *Grid) NumTiles() int { return (len(g.colBd) - 1) * (len(g.rowBd) - 1) }

// NumCtbs returns the number of CTBs in the picture.
func (g *Grid) NumCtbs() int { return len(g.rsToTs) }

// RsToTs converts a raster scan CTB address to a tile scan address.
func (g *Grid) RsToTs(rs int) int { return g.rsToTs[rs] }

// TsToRs converts a tile scan CTB address to a raster scan address.
func (g *Grid) TsToRs(ts int) int { return g.tsToRs[ts] }

// TileIDOfRs returns the tile a raster scan CTB address belongs to.
func (g *Grid) TileIDOfRs(rs int) int { return g.tileID[g.rsToTs[rs]] }

// SameTileRs reports whether two raster scan CTB addresses lie in the same
// tile. Both must be inside the picture.
func (g *Grid) SameTileRs(a, b int) bool { return g.TileIDOfRs(a) == g.TileIDOfRs(b) }

// boundaries turns tile sizes into cumulative boundaries, validating the total.
func boundaries(sizes []int, total int, what string) ([]int, error) {
	if len(sizes) == 0 {
		return nil, fmt.Errorf("tiles: no %s", what)
	}
	bd := make([]int, len(sizes)+1)
	for i, s := range sizes {
		if s <= 0 {
			return nil, fmt.Errorf("tiles: %s: size %d at index %d", what, s, i)
		}
		bd[i+1] = bd[i] + s
	}
	if bd[len(sizes)] != total {
		return nil, fmt.Errorf("tiles: %s sum to %d, want %d", what, bd[len(sizes)], total)
	}
	return bd, nil
}

// tileOf returns the index of the tile row or column containing CTB position p.
func tileOf(bd []int, p int) int {
	idx := 0
	for i := 0; i < len(bd)-1; i++ {
		if p >= bd[i] {
			idx = i
		}
	}
	return idx
}

func colWidth(colBd []int, i int) int  { return colBd[i+1] - colBd[i] }
func rowHeight(rowBd []int, j int) int { return rowBd[j+1] - rowBd[j] }
