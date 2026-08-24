package retile

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
)

const (
	inA  = "../../testdata/retile_a_128x64.265"  // x265 all-intra, 2 frames
	inB  = "../../testdata/retile_b_128x64.265"  // x265 all-intra, 2 frames
	inC  = "../../testdata/retile_c_64x64.265"   // x265 all-intra, 2 frames
	inPA = "../../testdata/retile_pa_128x64.265" // kvazaar MCTS P, 2 frames
	inPB = "../../testdata/retile_pb_128x64.265" // kvazaar MCTS P, 2 frames
)

func stitchFiles(t *testing.T, rows, cols int, paths ...string) *Result {
	t.Helper()
	inputs, err := ReadInputs(paths)
	if err != nil {
		t.Fatalf("ReadInputs: %v", err)
	}
	res, err := Stitch(inputs, rows, cols)
	if err != nil {
		t.Fatalf("Stitch %dx%d: %v", rows, cols, err)
	}
	return res
}

// TestStitchByteStable pins the output of the bit-splice. The digests are of
// streams produced by the standalone hevc-retiler this package was moved from,
// so a change here is a change in emitted bits and has to be deliberate.
func TestStitchByteStable(t *testing.T) {
	tests := []struct {
		name       string
		rows, cols int
		inputs     []string
		digest     string
	}{
		{"2x1_intra", 2, 1, []string{inA, inB},
			"8433b2e031eff4aa3c06a20689761929dbc9849a5091faa080df46c64b14608c"},
		{"2x2_intra", 2, 2, []string{inA, inB, inB, inA},
			"9d4fc348acf961c53e805519cc35482f50b8ff246027c181b3e266b9662750d5"},
		// 128-wide next to 64-wide: explicit column_width_minus1, not uniform.
		{"1x2_nonuniform", 1, 2, []string{inA, inC},
			"65f9915e647eba36fabab5654f910d7185c4313f996ffcd1f85ed2632451e338"},
		// Inter slices exercise the other half of BuildSliceNAL: no
		// no_output_of_prior_pics_flag, and an RPS copied verbatim.
		{"2x1_mcts_pframe", 2, 1, []string{inPA, inPB},
			"d51bd744f1967ce9bc46de661ff0c63f4fce1bf2551875dc97f8c0cfc6eddf6d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := stitchFiles(t, tc.rows, tc.cols, tc.inputs...)
			sum := sha256.Sum256(res.Data)
			if got := hex.EncodeToString(sum[:]); got != tc.digest {
				t.Errorf("digest = %s, want %s (%d bytes)", got, tc.digest, len(res.Data))
			}
		})
	}
}

func TestStitchGeometry(t *testing.T) {
	tests := []struct {
		name          string
		rows, cols    int
		inputs        []string
		width, height int
		uniform       bool
		colWidths     []uint // in CTBs, when not uniform
		tiles         [][4]int
	}{
		{
			name: "2x1_vertical", rows: 2, cols: 1, inputs: []string{inA, inB},
			width: 128, height: 128, uniform: true,
			tiles: [][4]int{{0, 0, 128, 64}, {0, 64, 128, 64}},
		},
		{
			name: "2x2_grid", rows: 2, cols: 2, inputs: []string{inA, inB, inB, inA},
			width: 256, height: 128, uniform: true,
			tiles: [][4]int{{0, 0, 128, 64}, {128, 0, 128, 64}, {0, 64, 128, 64}, {128, 64, 128, 64}},
		},
		{
			name: "1x2_nonuniform", rows: 1, cols: 2, inputs: []string{inA, inC},
			width: 192, height: 64, uniform: false, colWidths: []uint{2, 1},
			tiles: [][4]int{{0, 0, 128, 64}, {128, 0, 64, 64}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := stitchFiles(t, tc.rows, tc.cols, tc.inputs...)
			if res.Width != tc.width || res.Height != tc.height {
				t.Errorf("merged size = %dx%d, want %dx%d", res.Width, res.Height, tc.width, tc.height)
			}
			if res.Frames != 2 {
				t.Errorf("frames = %d, want 2", res.Frames)
			}
			if len(res.Tiles) != len(tc.tiles) {
				t.Fatalf("tiles = %d, want %d", len(res.Tiles), len(tc.tiles))
			}
			for i, want := range tc.tiles {
				got := res.Tiles[i]
				if [4]int{got.X, got.Y, got.W, got.H} != want {
					t.Errorf("tile %d at (%d,%d) %dx%d, want (%d,%d) %dx%d",
						i, got.X, got.Y, got.W, got.H, want[0], want[1], want[2], want[3])
				}
			}
			// The merged PPS is what a decoder reads the geometry from.
			_, pps := mergedParamSets(t, res.Data)
			if !pps.TilesEnabledFlag {
				t.Error("merged PPS does not have tiles enabled")
			}
			if pps.LoopFilterAcrossTilesEnabledFlag {
				t.Error("loop_filter_across_tiles_enabled_flag must be 0; a filter " +
					"crossing a seam would mix two unrelated pictures")
			}
			if int(pps.NumTileColumnsMinus1)+1 != tc.cols || int(pps.NumTileRowsMinus1)+1 != tc.rows {
				t.Errorf("merged PPS grid = %dx%d cols x rows, want %dx%d",
					pps.NumTileColumnsMinus1+1, pps.NumTileRowsMinus1+1, tc.cols, tc.rows)
			}
			if pps.UniformSpacingFlag != tc.uniform {
				t.Errorf("uniform_spacing_flag = %v, want %v", pps.UniformSpacingFlag, tc.uniform)
			}
			if !tc.uniform {
				for i, w := range tc.colWidths[:len(tc.colWidths)-1] {
					if pps.ColumnWidthMinus1[i] != w-1 {
						t.Errorf("column_width_minus1[%d] = %d, want %d",
							i, pps.ColumnWidthMinus1[i], w-1)
					}
				}
			}
		})
	}
}

func TestStitchReportsInterSlices(t *testing.T) {
	if res := stitchFiles(t, 2, 1, inA, inB); res.InterSlices {
		t.Error("all-intra stitch reported inter slices")
	}
	if res := stitchFiles(t, 2, 1, inPA, inPB); !res.InterSlices {
		t.Error("P-frame stitch did not report inter slices")
	}
}

// mergedParamSets parses the SPS and PPS out of a stitched stream.
func mergedParamSets(t *testing.T, data []byte) (*hevc.SPS, *hevc.PPS) {
	t.Helper()
	var sps *hevc.SPS
	var pps *hevc.PPS
	spsMap := map[uint32]*hevc.SPS{}
	for _, n := range SplitAnnexB(data) {
		var err error
		switch hevc.GetNaluType(n[0]) {
		case hevc.NALU_SPS:
			if sps, err = hevc.ParseSPSNALUnit(n); err != nil {
				t.Fatalf("merged SPS: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			if pps, err = hevc.ParsePPSNALUnit(n, spsMap); err != nil {
				t.Fatalf("merged PPS: %v", err)
			}
		}
	}
	if sps == nil || pps == nil {
		t.Fatal("merged stream has no SPS/PPS")
	}
	return sps, pps
}

func TestParseGrid(t *testing.T) {
	tests := []struct {
		spec       string
		n          int
		rows, cols int
		wantErr    string
	}{
		{"", 3, 3, 1, ""}, // default vertical stack
		{"2x2", 4, 2, 2, ""},
		{"1X2", 2, 1, 2, ""}, // case-insensitive
		{"2x2", 3, 0, 0, "needs 4 inputs, got 3"},
		{"2", 2, 0, 0, "want RxC"},
		{"ax2", 2, 0, 0, "bad grid rows"},
		{"2xb", 2, 0, 0, "bad grid cols"},
		{"0x2", 2, 0, 0, "must be positive"},
	}
	for _, tc := range tests {
		rows, cols, err := ParseGrid(tc.spec, tc.n)
		switch {
		case tc.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseGrid(%q, %d) error = %v, want %q", tc.spec, tc.n, err, tc.wantErr)
			}
		case err != nil:
			t.Errorf("ParseGrid(%q, %d): %v", tc.spec, tc.n, err)
		case rows != tc.rows || cols != tc.cols:
			t.Errorf("ParseGrid(%q, %d) = %dx%d, want %dx%d", tc.spec, tc.n, rows, cols, tc.rows, tc.cols)
		}
	}
}

// TestStitchRefusesUnusableInputs covers the shapes that would otherwise
// produce a structurally valid stream decoding to the wrong picture.
func TestStitchRefusesUnusableInputs(t *testing.T) {
	tests := []struct {
		name       string
		rows, cols int
		inputs     []string
		wantErr    string
	}{
		{
			name: "already_tiled", rows: 2, cols: 1,
			inputs:  []string{"../../testdata/tiles_2x2_128x128.265", inA},
			wantErr: "tiles and WPP disabled",
		},
		{
			name: "wavefront", rows: 2, cols: 1,
			inputs:  []string{"../../testdata/slices_wpp_2slices_256x128.265", inA},
			wantErr: "tiles and WPP disabled",
		},
		{
			name: "width_mismatch_in_column", rows: 2, cols: 1,
			inputs:  []string{inA, inC},
			wantErr: "width 64 != column width 128",
		},
		{
			name: "grid_size_mismatch", rows: 2, cols: 2,
			inputs:  []string{inA, inB},
			wantErr: "needs 4 inputs, got 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inputs, err := ReadInputs(tc.inputs)
			if err != nil {
				t.Fatalf("ReadInputs: %v", err)
			}
			_, err = Stitch(inputs, tc.rows, tc.cols)
			if err == nil {
				t.Fatalf("Stitch accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestStitchRefusesNonHEVCInput(t *testing.T) {
	inputs := []Input{
		{Name: "junk.bin", Data: []byte("this is not an Annex-B byte stream")},
		{Name: "junk2.bin", Data: []byte("nor is this")},
	}
	_, err := Stitch(inputs, 2, 1)
	if err == nil || !strings.Contains(err.Error(), "missing SPS/PPS/slices") {
		t.Errorf("error = %v, want it to mention missing SPS/PPS/slices", err)
	}
}

// TestCheckSliceRefuses covers the slice shapes the header splice would
// mis-copy. They need no fixture: the splice reads exactly these fields.
func TestCheckSliceRefuses(t *testing.T) {
	pps := &hevc.PPS{PicParameterSetID: 0}
	ok := hevc.SliceHeader{FirstSliceSegmentInPicFlag: true}
	tests := []struct {
		name    string
		sh      hevc.SliceHeader
		wantErr string
	}{
		{"ok", ok, ""},
		{
			name:    "not_first_segment",
			sh:      hevc.SliceHeader{FirstSliceSegmentInPicFlag: false},
			wantErr: "one picture as exactly one slice segment",
		},
		{
			name:    "dependent_segment",
			sh:      hevc.SliceHeader{FirstSliceSegmentInPicFlag: true, DependentSliceSegmentFlag: true},
			wantErr: "dependent slice segment",
		},
		{
			name:    "entry_points",
			sh:      hevc.SliceHeader{FirstSliceSegmentInPicFlag: true, NumEntryPointOffsets: 3},
			wantErr: "entry point offsets",
		},
		{
			name:    "other_pps",
			sh:      hevc.SliceHeader{FirstSliceSegmentInPicFlag: true, PicParameterSetId: 1},
			wantErr: "references PPS 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSlice("in.265", 0, &tc.sh, pps)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckParamSetsRefuses(t *testing.T) {
	sps := func(mut func(*hevc.SPS)) *hevc.SPS {
		s := &hevc.SPS{}
		mut(s)
		return s
	}
	tests := []struct {
		name    string
		sps     *hevc.SPS
		pps     *hevc.PPS
		wantErr string
	}{
		{"ok", &hevc.SPS{}, &hevc.PPS{}, ""},
		{"tiles", &hevc.SPS{}, &hevc.PPS{TilesEnabledFlag: true}, "tiles and WPP disabled"},
		{"wpp", &hevc.SPS{}, &hevc.PPS{EntropyCodingSyncEnabledFlag: true}, "tiles and WPP disabled"},
		{
			name: "slice_header_extension", sps: &hevc.SPS{},
			pps:     &hevc.PPS{SliceSegmentHeaderExtensionPresentFlag: true},
			wantErr: "slice_segment_header_extension",
		},
		{
			name: "conformance_window",
			sps: sps(func(s *hevc.SPS) {
				s.ConformanceWindowFlag = true
				s.ConformanceWindow = hevc.ConformanceWindow{BottomOffset: 4}
			}),
			pps: &hevc.PPS{}, wantErr: "conformance window",
		},
		{
			name:    "separate_colour_planes",
			sps:     sps(func(s *hevc.SPS) { s.SeparateColourPlaneFlag = true }),
			pps:     &hevc.PPS{},
			wantErr: "separate_colour_plane_flag",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkParamSets("in.265", tc.sps, tc.pps)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestCompatibleRefusesDivergentCodingTools is the gate that matters most and
// is invisible without it: one merged SPS/PPS describes every tile, so a tile
// encoded with different tools decodes as garbage in its own corner. The size
// and level legitimately differ; nothing else does.
func TestCompatibleRefusesDivergentCodingTools(t *testing.T) {
	base := &stream{
		name:      "a.265",
		spsParsed: &hevc.SPS{PicWidthInLumaSamples: 128, PicHeightInLumaSamples: 64},
		ppsParsed: &hevc.PPS{},
	}
	tests := []struct {
		name    string
		mutate  func(*hevc.SPS, *hevc.PPS)
		wantErr string
	}{
		{"same", func(*hevc.SPS, *hevc.PPS) {}, ""},
		{
			name: "different_size_is_fine",
			mutate: func(s *hevc.SPS, _ *hevc.PPS) {
				s.PicWidthInLumaSamples, s.PicHeightInLumaSamples = 64, 64
			},
		},
		{
			name:   "different_level_is_fine",
			mutate: func(s *hevc.SPS, _ *hevc.PPS) { s.ProfileTierLevel.GeneralLevelIDC = 120 },
		},
		{
			name:    "sao",
			mutate:  func(s *hevc.SPS, _ *hevc.PPS) { s.SampleAdaptiveOffsetEnabledFlag = true },
			wantErr: "SPS.SampleAdaptiveOffsetEnabledFlag",
		},
		{
			name:    "amp",
			mutate:  func(s *hevc.SPS, _ *hevc.PPS) { s.AmpEnabledFlag = true },
			wantErr: "SPS.AmpEnabledFlag",
		},
		{
			name:    "cu_size",
			mutate:  func(s *hevc.SPS, _ *hevc.PPS) { s.Log2DiffMaxMinLumaCodingBlockSize = 2 },
			wantErr: "SPS.Log2DiffMaxMinLumaCodingBlockSize",
		},
		{
			// A slice QP is coded as a delta from init_qp, and the delta bits
			// are copied verbatim, so a differing init_qp silently reQPs a tile.
			name:    "init_qp",
			mutate:  func(_ *hevc.SPS, p *hevc.PPS) { p.InitQpMinus26 = 4 },
			wantErr: "PPS.InitQpMinus26",
		},
		{
			name:    "sign_data_hiding",
			mutate:  func(_ *hevc.SPS, p *hevc.PPS) { p.SignDataHidingEnabledFlag = true },
			wantErr: "PPS.SignDataHidingEnabledFlag",
		},
		{
			name:    "transform_skip",
			mutate:  func(_ *hevc.SPS, p *hevc.PPS) { p.TransformSkipEnabledFlag = true },
			wantErr: "PPS.TransformSkipEnabledFlag",
		},
		{
			name:    "chroma_qp_offset",
			mutate:  func(_ *hevc.SPS, p *hevc.PPS) { p.CbQpOffset = -2 },
			wantErr: "PPS.CbQpOffset",
		},
		{
			// Parameter-set ids are rewritten by the splice, so they may differ.
			name:   "parameter_set_ids_are_fine",
			mutate: func(s *hevc.SPS, p *hevc.PPS) { s.SpsID, p.SeqParameterSetID, p.PicParameterSetID = 1, 1, 1 },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sps, pps := *base.spsParsed, *base.ppsParsed
			tc.mutate(&sps, &pps)
			other := &stream{name: "b.265", spsParsed: &sps, ppsParsed: &pps}
			_, err := compatible(base, other)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to name %q", err, tc.wantErr)
			}
		})
	}
}

// TestCompatibleReportsVUIDifference: a differing VUI changes how the picture
// is displayed, not how it decodes, so it is reported rather than refused.
func TestCompatibleReportsVUIDifference(t *testing.T) {
	base := &stream{
		name:      "a.265",
		spsParsed: &hevc.SPS{VUI: &hevc.VUIParameters{SampleAspectRatioWidth: 1, SampleAspectRatioHeight: 1}},
		ppsParsed: &hevc.PPS{},
	}
	other := &stream{
		name:      "b.265",
		spsParsed: &hevc.SPS{VUI: &hevc.VUIParameters{SampleAspectRatioWidth: 4, SampleAspectRatioHeight: 3}},
		ppsParsed: &hevc.PPS{},
	}
	notes, err := compatible(base, other)
	if err != nil {
		t.Fatalf("VUI difference must not be fatal: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "VUI.SampleAspectRatioWidth") {
		t.Errorf("notes = %v, want one naming VUI.SampleAspectRatioWidth", notes)
	}
}
