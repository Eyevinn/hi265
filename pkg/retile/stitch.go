package retile

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/Eyevinn/mp4ff/hevc"
)

// Input is one source stream to place in the tile grid.
type Input struct {
	Name string // label used in messages, typically the file path
	Data []byte // Annex-B byte stream
}

// Result is a stitched stream together with the geometry it was built with.
type Result struct {
	Data        []byte // merged Annex-B byte stream
	Width       int    // merged picture width in luma samples
	Height      int    // merged picture height in luma samples
	Frames      int
	Rows, Cols  int
	LevelIDC    byte   // general_level_idc written into the merged SPS
	Tiles       []Tile // one per input, row-major
	InterSlices bool   // any picture carries a P or B slice
	Notes       []string
}

// ParseGrid turns a "RxC" grid spec into rows and columns, defaulting to a
// vertical Nx1 stack when spec is empty.
func ParseGrid(spec string, nInputs int) (rows, cols int, err error) {
	if spec == "" {
		return nInputs, 1, nil // default: vertical stack
	}
	parts := strings.SplitN(strings.ToLower(spec), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad grid %q, want RxC", spec)
	}
	if rows, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("bad grid rows: %w", err)
	}
	if cols, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("bad grid cols: %w", err)
	}
	if rows < 1 || cols < 1 {
		return 0, 0, fmt.Errorf("grid %dx%d: rows and columns must be positive", rows, cols)
	}
	if rows*cols != nInputs {
		return 0, 0, fmt.Errorf("grid %dx%d needs %d inputs, got %d", rows, cols, rows*cols, nInputs)
	}
	return rows, cols, nil
}

// ReadInputs reads Annex-B files into Inputs, keeping the path as the name.
func ReadInputs(paths []string) ([]Input, error) {
	inputs := make([]Input, len(paths))
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		inputs[i] = Input{Name: p, Data: data}
	}
	return inputs, nil
}

type stream struct {
	name          string
	vps, sps, pps []byte
	spsParsed     *hevc.SPS
	ppsParsed     *hevc.PPS
	slices        [][]byte
	sliceHdrs     []*hevc.SliceHeader
	sliceTypes    []hevc.NaluType
	notes         []string
}

// parseStream splits one input and refuses every shape the bit-splice cannot
// handle. Everything refused here would otherwise produce a structurally valid
// stream that decodes to the wrong picture.
func parseStream(in Input) (*stream, error) {
	s := &stream{name: in.Name}
	spsMap := map[uint32]*hevc.SPS{}
	ppsMap := map[uint32]*hevc.PPS{}
	for _, n := range SplitAnnexB(in.Data) {
		t := hevc.GetNaluType(n[0])
		switch {
		case t == hevc.NALU_VPS:
			if s.vps == nil {
				s.vps = n
			}
		case t == hevc.NALU_SPS:
			if s.sps != nil {
				// A repeated parameter set is normal (x265 --repeat-headers);
				// a *different* one mid-stream is a resolution or tool switch
				// that one merged SPS cannot describe.
				if !equalBytes(s.sps, n) {
					return nil, fmt.Errorf("%s: stream carries more than one distinct SPS", in.Name)
				}
				continue
			}
			s.sps = n
			sps, err := hevc.ParseSPSNALUnit(n)
			if err != nil {
				return nil, fmt.Errorf("%s: SPS: %w", in.Name, err)
			}
			s.spsParsed = sps
			spsMap[uint32(sps.SpsID)] = sps
		case t == hevc.NALU_PPS:
			if s.pps != nil {
				if !equalBytes(s.pps, n) {
					return nil, fmt.Errorf("%s: stream carries more than one distinct PPS", in.Name)
				}
				continue
			}
			s.pps = n
			if s.spsParsed == nil {
				return nil, fmt.Errorf("%s: PPS before SPS", in.Name)
			}
			pps, err := hevc.ParsePPSNALUnit(n, spsMap)
			if err != nil {
				return nil, fmt.Errorf("%s: PPS: %w", in.Name, err)
			}
			if err := checkParamSets(in.Name, s.spsParsed, pps); err != nil {
				return nil, err
			}
			s.ppsParsed = pps
			ppsMap[pps.PicParameterSetID] = pps
		case t < 32: // VCL slice
			if s.ppsParsed == nil {
				return nil, fmt.Errorf("%s: slice before SPS/PPS", in.Name)
			}
			sh, err := hevc.ParseSliceHeader(n, spsMap, ppsMap)
			if err != nil {
				return nil, fmt.Errorf("%s: slice %d: %w", in.Name, len(s.slices), err)
			}
			if err := checkSlice(in.Name, len(s.slices), sh, s.ppsParsed); err != nil {
				return nil, err
			}
			s.slices = append(s.slices, n)
			s.sliceHdrs = append(s.sliceHdrs, sh)
			s.sliceTypes = append(s.sliceTypes, t)
			// SEI, AUD, filler and end-of-stream NALs are dropped: the merged
			// picture is not the picture any of them describes.
		}
	}
	if s.sps == nil || s.pps == nil || len(s.slices) == 0 {
		return nil, fmt.Errorf("%s: missing SPS/PPS/slices", in.Name)
	}
	return s, nil
}

// checkParamSets refuses the parameter-set shapes the rewrite cannot express.
func checkParamSets(name string, sps *hevc.SPS, pps *hevc.PPS) error {
	if pps.TilesEnabledFlag || pps.EntropyCodingSyncEnabledFlag {
		return fmt.Errorf("%s: input must have tiles and WPP disabled: the merged PPS turns tiles on, "+
			"and a slice that already carries entry point offsets cannot be re-addressed", name)
	}
	if pps.SliceSegmentHeaderExtensionPresentFlag {
		return fmt.Errorf("%s: input must not use slice_segment_header_extension", name)
	}
	if sps.ConformanceWindowFlag {
		return fmt.Errorf("%s: input has a conformance window (%d,%d,%d,%d); crop it before stitching, "+
			"since the merged SPS applies one window to the whole picture, not one per tile", name,
			sps.ConformanceWindow.LeftOffset, sps.ConformanceWindow.RightOffset,
			sps.ConformanceWindow.TopOffset, sps.ConformanceWindow.BottomOffset)
	}
	if sps.SeparateColourPlaneFlag {
		return fmt.Errorf("%s: separate_colour_plane_flag is not supported", name)
	}
	return nil
}

// checkSlice refuses slice shapes whose header the splice would mis-copy.
func checkSlice(name string, idx int, sh *hevc.SliceHeader, pps *hevc.PPS) error {
	// BuildSliceNAL copies the header verbatim from just after
	// slice_pic_parameter_set_id. In a segment that is not the first of its
	// picture, that is where dependent_slice_segment_flag and
	// slice_segment_address sit, and they would be copied into the middle of
	// the rewritten header. One slice segment per picture is also what makes
	// one tile per slice segment possible at all.
	if !sh.FirstSliceSegmentInPicFlag {
		return fmt.Errorf("%s: slice %d is not the first segment of its picture; "+
			"each input must code one picture as exactly one slice segment", name, idx)
	}
	if sh.DependentSliceSegmentFlag {
		return fmt.Errorf("%s: slice %d is a dependent slice segment", name, idx)
	}
	if sh.NumEntryPointOffsets != 0 {
		return fmt.Errorf("%s: slice %d carries %d entry point offsets", name, idx, sh.NumEntryPointOffsets)
	}
	if sh.PicParameterSetId != pps.PicParameterSetID {
		return fmt.Errorf("%s: slice %d references PPS %d, not the stream's PPS %d",
			name, idx, sh.PicParameterSetId, pps.PicParameterSetID)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// Stitch places inputs in a rows x cols tile grid, row-major, and returns one
// merged Annex-B stream. Parameter sets and slice headers are rewritten; the
// CABAC payloads are copied verbatim, so nothing is re-encoded and nothing is
// re-quantized.
//
// Every input must code one picture per slice segment, share all coding tools
// with the others, be CTB-aligned, and be all-intra or motion-constrained
// (MCTS) for inter frames. Everything but the MCTS property is checked here;
// MCTS cannot be checked from the bitstream, so it is what Verify is for.
func Stitch(inputs []Input, rows, cols int) (*Result, error) {
	if rows < 1 || cols < 1 {
		return nil, fmt.Errorf("grid %dx%d: rows and columns must be positive", rows, cols)
	}
	if len(inputs) != rows*cols {
		return nil, fmt.Errorf("grid %dx%d needs %d inputs, got %d", rows, cols, rows*cols, len(inputs))
	}

	streams := make([]*stream, len(inputs))
	var notes []string
	for i, in := range inputs {
		s, err := parseStream(in)
		if err != nil {
			return nil, err
		}
		streams[i] = s
		notes = append(notes, s.notes...)
	}
	at := func(r, c int) *stream { return streams[r*cols+c] }
	base := streams[0]
	ctb := CtbSizeY(base.spsParsed)

	// Validate a consistent CTB size and a rectangular grid: every tile in a
	// column shares its width, every tile in a row shares its height.
	colWidths := make([]uint, cols)  // CTBs
	rowHeights := make([]uint, rows) // CTBs
	frames := len(base.slices)
	for r := range rows {
		for c := range cols {
			s := at(r, c)
			if r != 0 || c != 0 {
				n, err := compatible(base, s)
				if err != nil {
					return nil, fmt.Errorf("tile (%d,%d): %w", r, c, err)
				}
				notes = append(notes, n...)
			}
			if CtbSizeY(s.spsParsed) != ctb {
				return nil, fmt.Errorf("tile (%d,%d): CTB size %d != %d",
					r, c, CtbSizeY(s.spsParsed), ctb)
			}
			w := uint(s.spsParsed.PicWidthInLumaSamples)
			h := uint(s.spsParsed.PicHeightInLumaSamples)
			if w%ctb != 0 || h%ctb != 0 {
				return nil, fmt.Errorf("tile (%d,%d): %dx%d not a multiple of CTB %d", r, c, w, h, ctb)
			}
			wc, hc := w/ctb, h/ctb
			if c == 0 {
				rowHeights[r] = hc
			} else if rowHeights[r] != hc {
				return nil, fmt.Errorf("tile (%d,%d): height %d != row height %d",
					r, c, h, rowHeights[r]*ctb)
			}
			if r == 0 {
				colWidths[c] = wc
			} else if colWidths[c] != wc {
				return nil, fmt.Errorf("tile (%d,%d): width %d != column width %d",
					r, c, w, colWidths[c]*ctb)
			}
			if len(s.slices) != frames {
				return nil, fmt.Errorf("tile (%d,%d): %d frames != %d", r, c, len(s.slices), frames)
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

	oldLevel := base.spsParsed.ProfileTierLevel.GeneralLevelIDC
	newLevel := pickLevelIDC(mergedW*mergedH, oldLevel)
	mergedSPSnal, err := RewriteSPS(base.sps, mergedW, mergedH, newLevel, oldLevel)
	if err != nil {
		return nil, err
	}
	mergedPPSnal, err := RewritePPS(base.pps, TileGrid{ColWidths: colWidths, RowHeights: rowHeights})
	if err != nil {
		return nil, err
	}

	mergedSPS, err := hevc.ParseSPSNALUnit(mergedSPSnal)
	if err != nil {
		return nil, fmt.Errorf("parse merged SPS: %w", err)
	}
	spsMap := map[uint32]*hevc.SPS{uint32(mergedSPS.SpsID): mergedSPS}
	mergedPPS, err := hevc.ParsePPSNALUnit(mergedPPSnal, spsMap)
	if err != nil {
		return nil, fmt.Errorf("parse merged PPS: %w", err)
	}
	picWc := PicWidthInCtbs(mergedSPS)

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

	inter := false
	for f := range frames {
		for r := range rows {
			for c := range cols {
				s := at(r, c)
				// All tiles of one picture must share type and POC.
				if s.sliceTypes[f] != base.sliceTypes[f] {
					return nil, fmt.Errorf("frame %d tile (%d,%d): NAL type %s != %s",
						f, r, c, s.sliceTypes[f], base.sliceTypes[f])
				}
				if s.sliceHdrs[f].PicOrderCntLsb != base.sliceHdrs[f].PicOrderCntLsb {
					return nil, fmt.Errorf("frame %d tile (%d,%d): POC mismatch", f, r, c)
				}
				if s.sliceHdrs[f].SliceType != hevc.SLICE_I {
					inter = true
				}
				segAddr := rowStart[r]*picWc + colStart[c]
				p := SliceParams{FirstSlice: r == 0 && c == 0, SegmentAddress: segAddr}
				nal, err := BuildSliceNAL(s.slices[f], s.sliceHdrs[f], mergedSPS, mergedPPS, p)
				if err != nil {
					return nil, err
				}
				emit(nal)
			}
		}
	}

	res := &Result{
		Data: outBuf, Width: mergedW, Height: mergedH, Frames: frames,
		Rows: rows, Cols: cols, LevelIDC: newLevel, InterSlices: inter, Notes: notes,
	}
	for r := range rows {
		for c := range cols {
			res.Tiles = append(res.Tiles, Tile{
				Name: inputs[r*cols+c].Name,
				Data: inputs[r*cols+c].Data,
				X:    int(colStart[c] * ctb), Y: int(rowStart[r] * ctb),
				W: int(colWidths[c] * ctb), H: int(rowHeights[r] * ctb),
			})
		}
	}
	return res, nil
}

// compatible checks that one input can be decoded by the merged SPS/PPS, which
// are rewritten from the first input's. Everything a tile's CABAC payload
// depends on has to agree; only the picture size and the level legitimately
// differ per tile, and the VUI is display metadata that is reported rather
// than refused.
func compatible(base, s *stream) ([]string, error) {
	a, b := normalizeSPS(base.spsParsed), normalizeSPS(s.spsParsed)
	aVUI, bVUI := a.VUI, b.VUI
	a.VUI, b.VUI = nil, nil
	if path, ok := firstDiff(reflect.ValueOf(a), reflect.ValueOf(b), "SPS"); !ok {
		return nil, fmt.Errorf("SPS differs from tile (0,0) at %s; one merged SPS describes every tile, "+
			"so all inputs must be encoded with the same coding tools", path)
	}
	pa, pb := normalizePPS(base.ppsParsed), normalizePPS(s.ppsParsed)
	if path, ok := firstDiff(reflect.ValueOf(pa), reflect.ValueOf(pb), "PPS"); !ok {
		return nil, fmt.Errorf("PPS differs from tile (0,0) at %s; one merged PPS describes every tile, "+
			"and a slice QP is coded as a delta from its init_qp", path)
	}
	var notes []string
	if path, ok := firstDiff(reflect.ValueOf(aVUI), reflect.ValueOf(bVUI), "VUI"); !ok {
		notes = append(notes, fmt.Sprintf("%s: VUI differs from tile (0,0) at %s; "+
			"the merged stream carries tile (0,0)'s VUI", s.name, path))
	}
	return notes, nil
}

// normalizeSPS returns a copy with the fields that legitimately differ per tile
// zeroed: the picture size, which is exactly what the stitch rewrites, the
// conformance window, which is refused outright, and the level, which scales
// with picture size and is recomputed for the merged picture.
func normalizeSPS(sps *hevc.SPS) *hevc.SPS {
	c := *sps
	c.PicWidthInLumaSamples, c.PicHeightInLumaSamples = 0, 0
	c.ConformanceWindowFlag, c.ConformanceWindow = false, hevc.ConformanceWindow{}
	c.ProfileTierLevel.GeneralLevelIDC = 0
	c.SpsID, c.VpsID = 0, 0
	return &c
}

// normalizePPS returns a copy with the parameter-set ids zeroed: slice headers
// are rewritten to reference the merged PPS, so an id that differs is harmless
// as long as the contents match.
func normalizePPS(pps *hevc.PPS) *hevc.PPS {
	c := *pps
	c.PicParameterSetID, c.SeqParameterSetID = 0, 0
	return &c
}

// firstDiff walks two values of the same type and returns the path of the first
// field that differs, or ("", true) when they are equal. It exists so a refusal
// can name the offending syntax element instead of just saying "they differ".
func firstDiff(a, b reflect.Value, path string) (string, bool) {
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() != b.IsNil() {
			return path, false
		}
		if a.IsNil() {
			return "", true
		}
		return firstDiff(a.Elem(), b.Elem(), path)
	case reflect.Struct:
		for i := range a.NumField() {
			if !a.Type().Field(i).IsExported() {
				continue
			}
			if p, ok := firstDiff(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); !ok {
				return p, false
			}
		}
		return "", true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return fmt.Sprintf("%s (length %d vs %d)", path, a.Len(), b.Len()), false
		}
		for i := range a.Len() {
			if p, ok := firstDiff(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i)); !ok {
				return p, false
			}
		}
		return "", true
	default:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			return fmt.Sprintf("%s (%v vs %v)", path, a.Interface(), b.Interface()), false
		}
		return "", true
	}
}
