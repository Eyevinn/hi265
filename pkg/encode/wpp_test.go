package encode

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// TestDecodeWPPStreams decodes x265 output with wavefront parallel processing
// left on, which is x265's default. Each CTU row is its own CABAC substream:
// the engine restarts at the row's entry point and its context state comes from
// a snapshot taken after the second CTU of the row above, not from the end of
// that row.
//
// Flat content keeps this a test of structure rather than of residual coding —
// busy content still diverges for an unrelated reason (roadmap 0.10), which
// would mask what this test is for.
func TestDecodeWPPStreams(t *testing.T) {
	ffmpeg := ffmpegBin(t)
	x265 := x265Bin(t)

	cases := []struct {
		name          string
		width, height int
		extra         []string
	}{
		// 22 CTU rows, so the row-to-row context sync runs many times.
		{"ctu16_many_rows", 640, 352, []string{"--ctu", "16", "--min-cu-size", "16"}},
		{"ctu32", 256, 192, []string{"--ctu", "32", "--min-cu-size", "16"}},
		{"ctu64", 256, 192, []string{"--ctu", "64", "--min-cu-size", "16"}},
		// One CTU per row: there is no second CTU to snapshot, so every row has
		// to fall back to a fresh context initialisation.
		{"single_ctu_column", 16, 128, []string{"--ctu", "16", "--min-cu-size", "16"}},
		// x265's own defaults: WPP plus SAO, sign data hiding, CTU 64 and 32x32
		// transforms, which is what real-world input actually looks like.
		{"x265_defaults", 256, 192, nil},
		{"x265_defaults_720p", 1280, 720, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stream := encodeWPPWithX265(t, x265, c.width, c.height, c.extra)

			ff := decodeWithFFmpeg(t, ffmpeg, stream)
			own := decodeWithHi265(t, stream, c.width, c.height, 1)
			if rep := compareYUV(t, own, ff, c.width, c.height, 1); !rep.exact() {
				t.Errorf("WPP stream decodes differently from FFmpeg: %s", rep)
			}
		})
	}
}

// encodeWPPWithX265 encodes flat content with WPP enabled (x265's default) and
// returns the Annex-B stream.
func encodeWPPWithX265(t *testing.T, x265 string, w, h int, extra []string) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "src.yuv")
	out := filepath.Join(dir, "out.265")

	// Flat mid-grey luma with distinct flat chroma planes.
	src := make([]byte, 0, w*h*3/2)
	for range w * h {
		src = append(src, 128)
	}
	for range w * h / 4 {
		src = append(src, 100)
	}
	for range w * h / 4 {
		src = append(src, 200)
	}
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatalf("write x265 input: %v", err)
	}

	args := []string{
		"--input", in,
		"--input-res", fmt.Sprintf("%dx%d", w, h),
		"--fps", "25", "--frames", "1", "--qp", "30",
		"--no-info", "--output", out,
	}
	args = append(args, extra...)

	cmd := exec.Command(x265, args...)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("x265 failed (%v): %s", err, combined)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read x265 output: %v", err)
	}
	return data
}

// ---------------------------------------------------------------------------
// WPP emission
// ---------------------------------------------------------------------------

// wppParamSets returns the parameter sets of a committed wavefront vector, an
// x265 --wpp encode of a 256x128 picture with CTU 64: four CTB columns by two
// CTB rows, so the slice data is two substreams with one entry point offset
// between them.
func wppParamSets(t *testing.T) (annexBPrefix []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	data, err := os.ReadFile("../../testdata/slices_wpp_2slices_256x128.265")
	if err != nil {
		t.Fatal(err)
	}
	vps, spsNALU, ppsNALU, sps, pps := parseParamSets(t, data)
	if !pps.EntropyCodingSyncEnabledFlag {
		t.Fatal("fixture is supposed to have wavefront parallel processing enabled")
	}
	if pps.TilesEnabledFlag {
		t.Fatal("fixture is supposed to have tiles disabled")
	}
	var buf bytes.Buffer
	for _, nalu := range [][]byte{vps, spsNALU, ppsNALU} {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nalu)
	}
	return buf.Bytes(), sps, pps
}

// A wavefront parameter set leaves the picture one slice segment — unlike tiles,
// which need one segment per tile — and splits only the CABAC stream inside it.
// So there is exactly one NALU, and mid-grey is again the content that makes the
// result checkable without a golden.
func TestEncodeGrayIDRWithWpp(t *testing.T) {
	prefix, sps, pps := wppParamSets(t)

	slices, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		t.Fatalf("EncodeGrayIDRSliceFromSPSPPS: %v", err)
	}
	if n := len(avc.ExtractNalusFromByteStream(slices)); n != 1 {
		t.Fatalf("emitted %d slice NALUs, want 1: WPP does not split slice segments", n)
	}
	decodeGray(t, append(prefix, slices...), 256, 128)
}

// The same for the CRA, which is the Gradual Decoder Refresh primitive. x265
// enables WPP by default, so this is the shape a refresh point actually has to
// be spliced into — the case that motivated hi265gray in the first place.
func TestEncodeGrayCRAWithWpp(t *testing.T) {
	prefix, sps, pps := wppParamSets(t)

	slices, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, 42)
	if err != nil {
		t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
	}
	nalus := avc.ExtractNalusFromByteStream(slices)
	if len(nalus) != 1 {
		t.Fatalf("emitted %d slice NALUs, want 1", len(nalus))
	}
	if got := hevc.GetNaluType(nalus[0][0]); got != hevc.NALU_CRA {
		t.Errorf("NALU type is %s, want CRA", got)
	}
	decodeGray(t, append(prefix, slices...), 256, 128)
}

// A P-skip picture against a wavefront parameter set, which is what
// hi265-mp4-extend appends to a default-settings x265 stream. Every CU is a
// zero-motion skip, so the picture has to come out identical to the gray IDR
// before it.
func TestEncodePSkipWithWpp(t *testing.T) {
	prefix, sps, pps := wppParamSets(t)

	idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		t.Fatalf("gray IDR: %v", err)
	}
	pskip, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("P-skip: %v", err)
	}
	if n := len(avc.ExtractNalusFromByteStream(pskip)); n != 1 {
		t.Fatalf("P-skip emitted %d slice NALUs, want 1", n)
	}

	frames, err := decoder.New().DecodeAnnexB(append(append(prefix, idr...), pskip...))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("decoded %d frames, want 2", len(frames))
	}
	if !bytes.Equal(frames[0].YUV420Bytes(), frames[1].YUV420Bytes()) {
		t.Error("the P-skip picture should be a copy of the one before it")
	}
}

// Wavefront parallel processing changes only how the slice data is entropy
// coded: the syntax elements, and so the reconstruction, are identical to the
// same picture coded as one continuous substream. Availability does not change
// at a CTB row boundary the way it does at a tile boundary, so unlike the tiles
// case this is an exact equality and not a tolerance.
//
// That makes it the sharpest check available without an external decoder: any
// mistake in the context snapshot, the substream splitting or the entry point
// offsets shows up as a different picture.
func TestGenerateWppMatchesUntiled(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		qp            int
		use8x8        bool
	}{
		{"128x128", 128, 128, 26, false},
		// One CTB column: no row has a second CTB to snapshot, so every row
		// after the first must fall back to a fresh context initialisation.
		{"one_ctb_column", 16, 128, 26, false},
		// Two CTB columns: the snapshot is taken from the last CTB of the row
		// above, which is the narrowest case where it exists at all.
		{"two_ctb_columns", 32, 128, 26, false},
		// One CTB row: no substream boundary, so no entry point offsets either.
		{"one_ctb_row", 128, 16, 26, false},
		// Partial CTBs at the right and bottom edges.
		{"ragged_edges", 120, 72, 26, false},
		{"8x8_cus", 128, 128, 26, true},
		{"qp10", 128, 128, 10, false},
		{"qp40", 128, 128, 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grid, colors, err := buildGrid(patTiles, 0, (tc.width+15)/16, (tc.height+15)/16)
			if err != nil {
				t.Fatal(err)
			}
			base := EncodeParams{
				Width: tc.width, Height: tc.height, QP: tc.qp, Use8x8CU: tc.use8x8,
			}
			wppParams := base
			wppParams.WPP = true

			decode := func(p EncodeParams) []byte {
				ps, err := GenerateVPSSPSPPS(p)
				if err != nil {
					t.Fatalf("GenerateVPSSPSPPS: %v", err)
				}
				idr, err := GenerateIDR(p, grid, colors)
				if err != nil {
					t.Fatalf("GenerateIDR: %v", err)
				}
				if n := len(avc.ExtractNalusFromByteStream(idr)); n != 1 {
					t.Fatalf("emitted %d slice NALUs, want 1", n)
				}
				frames, err := decoder.New().DecodeAnnexB(append(ps, idr...))
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				return frames[0].YUV420Bytes()
			}

			if got, want := decode(wppParams), decode(base); !bytes.Equal(got, want) {
				diff := 0
				for i := range got {
					if got[i] != want[i] {
						diff++
					}
				}
				t.Errorf("the wavefront encode decodes differently from the plain one: "+
					"%d of %d samples differ", diff, len(want))
			}
		})
	}
}

// The generated parameter sets have to carry the flag the slice data assumes,
// and the two parallelism tools are mutually exclusive: no HEVC profile permits
// tiles together with wavefront parallel processing, so asking for both is an
// error rather than a stream no decoder will take.
func TestGenerateWppParameterSets(t *testing.T) {
	p := EncodeParams{Width: 128, Height: 128, QP: 26, WPP: true}
	ps, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	_, _, _, _, pps := parseParamSets(t, ps)
	if !pps.EntropyCodingSyncEnabledFlag {
		t.Error("entropy_coding_sync_enabled_flag is not set in the generated PPS")
	}
	if pps.TilesEnabledFlag {
		t.Error("tiles_enabled_flag is set in a wavefront-only PPS")
	}

	both := EncodeParams{Width: 128, Height: 128, QP: 26, WPP: true, TileCols: 2, TileRows: 2}
	grid, colors, err := buildGrid(patTiles, 0, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateVPSSPSPPS(both); err == nil {
		t.Error("expected tiles combined with WPP to be refused for the parameter sets")
	}
	if _, err := GenerateIDR(both, grid, colors); err == nil {
		t.Error("expected tiles combined with WPP to be refused")
	}
	if _, err := GeneratePSkip(both, 1); err == nil {
		t.Error("expected tiles combined with WPP to be refused for a P-skip too")
	}
}

// A picture generated with WPP must announce one entry point offset per CTB row
// after the first, and the offsets have to be the lengths of the substreams that
// follow the header. Our own decoder navigates by those offsets, so it cannot
// tell a wrong byte alignment from a right one; asserting the count here pins
// the structure independently of the round trip above.
func TestGenerateWppEntryPointCount(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		wantOffsets   int
	}{
		{"eight_ctb_rows", 128, 128, 7},
		{"one_ctb_row", 128, 16, 0},
		{"two_ctb_rows", 128, 32, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grid, colors, err := buildGrid(patTiles, 0, (tc.width+15)/16, (tc.height+15)/16)
			if err != nil {
				t.Fatal(err)
			}
			p := EncodeParams{Width: tc.width, Height: tc.height, QP: 26, WPP: true}
			ps, err := GenerateVPSSPSPPS(p)
			if err != nil {
				t.Fatal(err)
			}
			idr, err := GenerateIDR(p, grid, colors)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, sps, pps := parseParamSets(t, ps)
			nalu := avc.ExtractNalusFromByteStream(idr)[0]
			sh, err := hevc.ParseSliceHeader(nalu,
				map[uint32]*hevc.SPS{uint32(sps.SpsID): sps},
				map[uint32]*hevc.PPS{pps.PicParameterSetID: pps})
			if err != nil {
				t.Fatalf("ParseSliceHeader: %v", err)
			}
			if got := int(sh.NumEntryPointOffsets); got != tc.wantOffsets {
				t.Errorf("num_entry_point_offsets = %d, want %d (one per CTB row after the first)",
					got, tc.wantOffsets)
			}
		})
	}
}

// TestGeneratedWppAgainstFFmpeg is the check our own decoder structurally cannot
// make. pkg/decoder navigates the slice data by the entry point offsets alone,
// so it lands on the right byte whatever the substreams end with — which means a
// wrong byte_alignment() is invisible to it, and to any round trip through it.
//
// decodeWithFFmpegSequential reads the slice data straight through instead, so
// it only accepts what is byte-exactly right. That is what caught the alignment
// bug this feature was first written with: the terminating bin's flush already
// ends in the one bit byte_alignment() calls for — spec 9.3.4.3.5's note says
// that same bit is the rbsp_stop_one_bit when it ends a slice segment — and
// writing a second one pushed every substream after a byte-aligned flush one
// byte along. Our decoder read it back bit-exactly; FFmpeg lost everything from
// CTB row 4 down.
func TestGeneratedWppAgainstFFmpeg(t *testing.T) {
	ffmpeg := ffmpegBin(t)

	for _, tc := range []struct {
		name          string
		width, height int
		qp            int
		frames        int
		use8x8        bool
	}{
		{"eight_ctb_rows", 128, 128, 26, 1, false},
		{"one_ctb_column", 16, 256, 26, 1, false},
		{"two_ctb_columns", 32, 128, 26, 1, false},
		{"one_ctb_row", 256, 16, 26, 1, false},
		{"ragged_edges", 120, 72, 26, 1, false},
		{"8x8_cus", 128, 128, 26, 1, true},
		{"qp10_dense_residual", 128, 128, 10, 1, false},
		{"qp40", 128, 128, 40, 1, false},
		// A P-skip picture after the IDR: cu_skip_flag's context and the row
		// snapshots have to line up for a P-slice too.
		{"with_pskip", 128, 128, 26, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grid, colors, err := buildGrid(patTiles, 0, (tc.width+15)/16, (tc.height+15)/16)
			if err != nil {
				t.Fatal(err)
			}
			p := EncodeParams{
				Width: tc.width, Height: tc.height, QP: tc.qp, Use8x8CU: tc.use8x8, WPP: true,
			}
			stream, err := GenerateVPSSPSPPS(p)
			if err != nil {
				t.Fatal(err)
			}
			idr, err := GenerateIDR(p, grid, colors)
			if err != nil {
				t.Fatal(err)
			}
			stream = append(stream, idr...)
			for poc := 1; poc < tc.frames; poc++ {
				pskip, err := GeneratePSkip(p, poc)
				if err != nil {
					t.Fatal(err)
				}
				stream = append(stream, pskip...)
			}

			ff := decodeWithFFmpegSequential(t, ffmpeg, stream)
			own := decodeWithHi265(t, stream, tc.width, tc.height, tc.frames)
			if rep := compareYUV(t, own, ff, tc.width, tc.height, tc.frames); !rep.exact() {
				t.Errorf("generated WPP stream decodes differently in FFmpeg: %s", rep)
			}
		})
	}
}
