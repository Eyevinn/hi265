package tiles

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/hevc"
)

// Log2CtbSize returns Log2CtbSizeY for a sequence parameter set.
func Log2CtbSize(sps *hevc.SPS) int {
	return int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 +
		int(sps.Log2DiffMaxMinLumaCodingBlockSize)
}

// PicSizeInCtbs returns the picture dimensions in CTBs.
func PicSizeInCtbs(sps *hevc.SPS) (ctbsX, ctbsY int) {
	ctbSize := 1 << Log2CtbSize(sps)
	ctbsX = (int(sps.PicWidthInLumaSamples) + ctbSize - 1) / ctbSize
	ctbsY = (int(sps.PicHeightInLumaSamples) + ctbSize - 1) / ctbSize
	return ctbsX, ctbsY
}

// FromPPS derives the tile scan tables of spec 6.5.1 from a picture's parameter
// sets. Without tiles the picture is one tile, where tile scan is raster scan,
// so callers need no special case for the untiled path.
func FromPPS(sps *hevc.SPS, pps *hevc.PPS) (*Grid, error) {
	ctbsX, ctbsY := PicSizeInCtbs(sps)
	if !pps.TilesEnabledFlag {
		return Single(ctbsX, ctbsY), nil
	}

	cols := int(pps.NumTileColumnsMinus1) + 1
	rows := int(pps.NumTileRowsMinus1) + 1

	var colWidths, rowHeights []int
	if pps.UniformSpacingFlag {
		colWidths = UniformSizes(ctbsX, cols)
		rowHeights = UniformSizes(ctbsY, rows)
	} else {
		var err error
		if colWidths, err = ExplicitSizes(ctbsX, plusOne(pps.ColumnWidthMinus1)); err != nil {
			return nil, fmt.Errorf("PPS tile columns: %w", err)
		}
		if rowHeights, err = ExplicitSizes(ctbsY, plusOne(pps.RowHeightMinus1)); err != nil {
			return nil, fmt.Errorf("PPS tile rows: %w", err)
		}
	}

	grid, err := New(ctbsX, ctbsY, colWidths, rowHeights)
	if err != nil {
		return nil, err
	}
	if grid.NumTiles() != cols*rows {
		return nil, fmt.Errorf("tile grid has %d tiles, PPS signals %dx%d",
			grid.NumTiles(), cols, rows)
	}
	return grid, nil
}

// plusOne converts the PPS's minus1-coded tile sizes to sizes in CTBs.
func plusOne(minus1 []uint) []int {
	out := make([]int, len(minus1))
	for i, v := range minus1 {
		out[i] = int(v) + 1
	}
	return out
}
