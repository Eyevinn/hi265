// Command hi265retile stitches several HEVC Annex-B videos into one tiled picture.
//
// Inputs are placed in a tile grid in row-major order. By default they are
// stacked vertically (Nx1); use -grid RxC for an R-row by C-column grid.
// All inputs must share coding parameters and CTB size, be CTB-aligned, have
// tiles/WPP disabled and no slice-segment-header extension, and be all-intra
// or motion-constrained (MCTS) for inter frames.
//
// Usage:
//
//	hi265retile -o merged.265 top.265 bottom.265            # vertical (2x1)
//	hi265retile -grid 2x2 -o merged.265 a.265 b.265 c.265 d.265
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Eyevinn/hi265/pkg/retile"
	"github.com/Eyevinn/mp4ff/hevc"
)

type stream struct {
	vps, sps, pps []byte
	spsParsed     *hevc.SPS
	ppsParsed     *hevc.PPS
	slices        [][]byte
	sliceHdrs     []*hevc.SliceHeader
	sliceTypes    []hevc.NaluType
}

func parseStream(path string) (*stream, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := &stream{}
	spsMap := map[uint32]*hevc.SPS{}
	ppsMap := map[uint32]*hevc.PPS{}
	for _, n := range retile.SplitAnnexB(data) {
		t := hevc.GetNaluType(n[0])
		switch {
		case t == hevc.NALU_VPS:
			if s.vps == nil {
				s.vps = n
			}
		case t == hevc.NALU_SPS:
			if s.sps == nil {
				s.sps = n
				sps, err := hevc.ParseSPSNALUnit(n)
				if err != nil {
					return nil, fmt.Errorf("%s: SPS: %w", path, err)
				}
				s.spsParsed = sps
				spsMap[uint32(sps.SpsID)] = sps
			}
		case t == hevc.NALU_PPS:
			if s.pps == nil {
				s.pps = n
				pps, err := hevc.ParsePPSNALUnit(n, spsMap)
				if err != nil {
					return nil, fmt.Errorf("%s: PPS: %w", path, err)
				}
				s.ppsParsed = pps
				ppsMap[pps.PicParameterSetID] = pps
			}
		case t < 32: // VCL slice
			sh, err := hevc.ParseSliceHeader(n, spsMap, ppsMap)
			if err != nil {
				return nil, fmt.Errorf("%s: slice %d: %w", path, len(s.slices), err)
			}
			s.slices = append(s.slices, n)
			s.sliceHdrs = append(s.sliceHdrs, sh)
			s.sliceTypes = append(s.sliceTypes, t)
		}
	}
	if s.sps == nil || s.pps == nil || len(s.slices) == 0 {
		return nil, fmt.Errorf("%s: missing SPS/PPS/slices", path)
	}
	if s.ppsParsed.TilesEnabledFlag || s.ppsParsed.EntropyCodingSyncEnabledFlag {
		return nil, fmt.Errorf("%s: input must have tiles and WPP disabled", path)
	}
	if s.ppsParsed.SliceSegmentHeaderExtensionPresentFlag {
		return nil, fmt.Errorf("%s: input must not use slice_segment_header_extension", path)
	}
	return s, nil
}

// levelTable maps general_level_idc to its MaxLumaPs (luma samples per picture).
var levelTable = []struct {
	idc       byte
	maxLumaPs int
}{
	{30, 36864}, {60, 122880}, {63, 245760}, {90, 552960}, {93, 983040},
	{120, 2228224}, {123, 2228224}, {150, 8912896}, {153, 8912896}, {156, 8912896},
	{180, 35651584}, {183, 35651584}, {186, 35651584},
}

func pickLevelIDC(samples int, atLeast byte) byte {
	chosen := levelTable[len(levelTable)-1].idc
	for _, l := range levelTable {
		if l.maxLumaPs >= samples {
			chosen = l.idc
			break
		}
	}
	if chosen < atLeast {
		chosen = atLeast
	}
	return chosen
}

func parseGrid(s string, nInputs int) (rows, cols int, err error) {
	if s == "" {
		return nInputs, 1, nil // default: vertical stack
	}
	parts := strings.SplitN(strings.ToLower(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad -grid %q, want RxC", s)
	}
	if rows, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("bad -grid rows: %w", err)
	}
	if cols, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("bad -grid cols: %w", err)
	}
	if rows*cols != nInputs {
		return 0, 0, fmt.Errorf("-grid %dx%d needs %d inputs, got %d", rows, cols, rows*cols, nInputs)
	}
	return rows, cols, nil
}

func main() {
	out := flag.String("o", "merged.265", "output Annex-B file")
	grid := flag.String("grid", "", "tile grid RxC (rows x cols), inputs row-major; default vertical Nx1")
	doVerify := flag.Bool("verify", false, "after writing, decode (ffmpeg) and compare each tile to its source")
	flag.Parse()
	inputs := flag.Args()
	if len(inputs) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hi265retile [-grid RxC] -o merged.265 in0.265 in1.265 [...]")
		os.Exit(1)
	}

	rows, cols, err := parseGrid(*grid, len(inputs))
	check(err)

	streams := make([]*stream, len(inputs))
	for i, in := range inputs {
		s, err := parseStream(in)
		check(err)
		streams[i] = s
	}
	at := func(r, c int) *stream { return streams[r*cols+c] }
	base := streams[0]
	ctb := retile.CtbSizeY(base.spsParsed)

	// Validate a consistent CTB size and a rectangular grid: every tile in a
	// column shares its width, every tile in a row shares its height.
	colWidths := make([]uint, cols)  // CTBs
	rowHeights := make([]uint, rows) // CTBs
	frames := len(base.slices)
	for r := range rows {
		for c := range cols {
			s := at(r, c)
			if retile.CtbSizeY(s.spsParsed) != ctb {
				check(fmt.Errorf("tile (%d,%d): CTB size mismatch", r, c))
			}
			w := uint(s.spsParsed.PicWidthInLumaSamples)
			h := uint(s.spsParsed.PicHeightInLumaSamples)
			if w%ctb != 0 || h%ctb != 0 {
				check(fmt.Errorf("tile (%d,%d): %dx%d not a multiple of CTB %d", r, c, w, h, ctb))
			}
			wc, hc := w/ctb, h/ctb
			if c == 0 {
				rowHeights[r] = hc
			} else if rowHeights[r] != hc {
				check(fmt.Errorf("tile (%d,%d): height %d != row height %d", r, c, h, rowHeights[r]*ctb))
			}
			if r == 0 {
				colWidths[c] = wc
			} else if colWidths[c] != wc {
				check(fmt.Errorf("tile (%d,%d): width %d != column width %d", r, c, w, colWidths[c]*ctb))
			}
			if len(s.slices) != frames {
				check(fmt.Errorf("tile (%d,%d): %d frames != %d", r, c, len(s.slices), frames))
			}
		}
	}

	var mergedWc, mergedHc uint
	for _, w := range colWidths {
		mergedWc += w
	}
	for _, h := range rowHeights {
		mergedHc += h
	}
	mergedW := int(mergedWc * ctb)
	mergedH := int(mergedHc * ctb)

	newLevel := pickLevelIDC(mergedW*mergedH, base.spsParsed.ProfileTierLevel.GeneralLevelIDC)
	oldLevel := base.spsParsed.ProfileTierLevel.GeneralLevelIDC
	mergedSPSnal, err := retile.RewriteSPS(base.sps, mergedW, mergedH, newLevel, oldLevel)
	check(err)
	mergedPPSnal, err := retile.RewritePPS(base.pps, retile.TileGrid{ColWidths: colWidths, RowHeights: rowHeights})
	check(err)

	mergedSPS, err := hevc.ParseSPSNALUnit(mergedSPSnal)
	if err != nil {
		check(fmt.Errorf("parse merged SPS: %w", err))
	}
	spsMap := map[uint32]*hevc.SPS{uint32(mergedSPS.SpsID): mergedSPS}
	mergedPPS, err := hevc.ParsePPSNALUnit(mergedPPSnal, spsMap)
	if err != nil {
		check(fmt.Errorf("parse merged PPS: %w", err))
	}
	picWc := retile.PicWidthInCtbs(mergedSPS)

	// Tile origins (in CTBs) from cumulative column/row sizes.
	colStart := make([]uint, cols)
	for c := 1; c < cols; c++ {
		colStart[c] = colStart[c-1] + colWidths[c-1]
	}
	rowStart := make([]uint, rows)
	for r := 1; r < rows; r++ {
		rowStart[r] = rowStart[r-1] + rowHeights[r-1]
	}

	var outBuf []byte
	emit := func(nal []byte) { outBuf = append(append(outBuf, 0, 0, 0, 1), nal...) }
	emit(base.vps)
	emit(mergedSPSnal)
	emit(mergedPPSnal)

	for f := range frames {
		for r := range rows {
			for c := range cols {
				s := at(r, c)
				// All tiles of one picture must share type and POC.
				if s.sliceTypes[f] != base.sliceTypes[f] {
					check(fmt.Errorf("frame %d tile (%d,%d): NAL type %s != %s",
						f, r, c, s.sliceTypes[f], base.sliceTypes[f]))
				}
				if s.sliceHdrs[f].PicOrderCntLsb != base.sliceHdrs[f].PicOrderCntLsb {
					check(fmt.Errorf("frame %d tile (%d,%d): POC mismatch", f, r, c))
				}
				segAddr := rowStart[r]*picWc + colStart[c]
				p := retile.SliceParams{FirstSlice: r == 0 && c == 0, SegmentAddress: segAddr}
				nal, err := retile.BuildSliceNAL(s.slices[f], s.sliceHdrs[f], mergedSPS, mergedPPS, p)
				check(err)
				emit(nal)
			}
		}
	}

	check(os.WriteFile(*out, outBuf, 0o644))
	fmt.Printf("wrote %s: %dx%d, %d frames, grid %dx%d (%d tiles), level %.1f, %d bytes\n",
		*out, mergedW, mergedH, frames, rows, cols, rows*cols, float64(newLevel)/30, len(outBuf))

	if *doVerify {
		var tiles []retile.Tile
		for r := range rows {
			for c := range cols {
				tiles = append(tiles, retile.Tile{
					Path: inputs[r*cols+c],
					X:    int(colStart[c] * ctb), Y: int(rowStart[r] * ctb),
					W: int(colWidths[c] * ctb), H: int(rowHeights[r] * ctb),
				})
			}
		}
		check(retile.Verify(*out, mergedW, mergedH, frames, tiles))
		fmt.Println("verify: all tiles pixel-perfect")
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
