package encode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/pkg/decoder"
)

// shortName is a fixture's base name without its directory or extension, so
// subtest names stay readable.
func shortName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".265")
}

// scanOrderForTest is the diagonal scan of one sub-block, and scanIndexAt maps a
// scan position to an index into a levels array of that stride.
func scanOrderForTest(size int) [][2]int { return slice.ScanOrder(size, size, 0) }

func scanIndexAt(scan [][2]int, size, n int) int { return scan[n][1]*size + scan[n][0] }

// The two committed vectors whose PPS sets sign_data_hiding_enabled_flag, which
// is x265's default. One has constant QP so cu_qp_delta stays off and sign hiding
// is the only thing under test; the other is rate-controlled, which is where a
// real stream has both at once. Both have a 16x16 CTU, and weighted prediction,
// transform skip, SAO and transquant bypass off, since the grid encoder does not
// implement those.
var signHideFixtures = []string{
	"../../testdata/signhide_qp30_192x96.265",
	"../../testdata/signhide_crf32_192x96.265",
}

func signHideParamSets(t *testing.T, path string) (annexBPrefix []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vps, spsNALU, ppsNALU, sps, pps := parseParamSets(t, data)
	if !pps.SignDataHidingEnabledFlag {
		t.Fatalf("%s is supposed to have sign data hiding enabled", path)
	}
	var buf bytes.Buffer
	for _, nalu := range [][]byte{vps, spsNALU, ppsNALU} {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nalu)
	}
	return buf.Bytes(), sps, pps
}

// signHidePatterns are the generator patterns dense enough to reach sign hiding
// at all, which TestSignHidingFiresOnlyOnDenseContent pins.
var signHidePatterns = []struct {
	name            string
	kind            patternKind
	maxPatternDelta int
	maxPatternMean  float64
}{
	// One bright CTU per row stepping right, so a CU's prediction comes from
	// neighbours of a different colour and the residual varies along a row.
	{"diagonal", patDiagonal, 4, 0.5},
	// SMPTE bars with a counter overlay, the busiest thing the generator makes.
	{"smpte_text", patSMPTEText, 6, 0.5},
}

// TestEncodeIDRWithSignHiding writes a grid IDR against parameter sets that
// enable sign data hiding and checks the picture survives the round trip.
//
// Ignoring the flag is not a lost optimisation, it is a broken bitstream: the
// decoder does not read a sign the encoder wrote, so every bin after it is
// misaligned. Measured before this landed, with the same fixtures and patterns:
// max delta 217 and 220 against the source, and for the constant-QP fixture the
// desynchronisation was bad enough that the slice terminated early and
// pkg/decoder refused the picture outright ("covered by 20 CTBs, expected 72").
//
// This is also the only test that catches a wrong parity, as opposed to a sign
// omitted in the wrong place — see TestEncodeIDRWithSignHidingAgainstFFmpeg.
//
// The tolerance is a little looser than an encode with hiding off would need,
// because that is what sign data hiding is: the parity of a sub-block's levels
// has to carry a sign, and buying that sometimes costs one quantizer step on one
// coefficient.
func TestEncodeIDRWithSignHiding(t *testing.T) {
	for _, fixture := range signHideFixtures {
		for _, pat := range signHidePatterns {
			t.Run(shortName(fixture)+"/"+pat.name, func(t *testing.T) {
				prefix, sps, pps := signHideParamSets(t, fixture)
				w := int(sps.PicWidthInLumaSamples)
				h := int(sps.PicHeightInLumaSamples)
				grid, colors, err := buildGrid(pat.kind, 0, (w+15)/16, (h+15)/16)
				if err != nil {
					t.Fatal(err)
				}
				idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
				if err != nil {
					t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
				}

				frames, err := decoder.New().DecodeAnnexB(append(prefix, idr...))
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(frames) != 1 {
					t.Fatalf("decoded %d frames, want 1", len(frames))
				}
				src, err := yuv.BuildFrame(grid, colors)
				if err != nil {
					t.Fatal(err)
				}
				src.Width, src.Height = w, h

				pe := measurePatternError(frames[0].YUV420Bytes(), src.YUV420Bytes())
				t.Logf("max delta %d, mean abs %.4f", pe.maxDelta, pe.meanAbs)
				if pe.maxDelta > pat.maxPatternDelta || pe.meanAbs > pat.maxPatternMean {
					t.Errorf("decoded picture drifts from the source: max delta %d (limit %d), "+
						"mean abs %.4f (limit %.4f)",
						pe.maxDelta, pat.maxPatternDelta, pe.meanAbs, pat.maxPatternMean)
				}
			})
		}
	}
}

// TestEncodeIDRWithSignHidingAgainstFFmpeg settles whether the sign is omitted in
// the right place, which is the half that changes the bin count: skip the wrong
// one and every bin after it is misaligned, and only an independent decoder can
// say so.
//
// It cannot settle the other half. Omitting the right sign but leaving the parity
// wrong is a perfectly well-formed bitstream, so FFmpeg and pkg/decoder read it
// identically — both arriving at the same wrong sign — and this test passes.
// Verified by breaking exactly that: the pattern check in
// TestEncodeIDRWithSignHiding is what fails. The two tests are not redundant.
func TestEncodeIDRWithSignHidingAgainstFFmpeg(t *testing.T) {
	ffmpeg := ffmpegBin(t)

	for _, fixture := range signHideFixtures {
		for _, pat := range signHidePatterns {
			t.Run(shortName(fixture)+"/"+pat.name, func(t *testing.T) {
				prefix, sps, pps := signHideParamSets(t, fixture)
				w := int(sps.PicWidthInLumaSamples)
				h := int(sps.PicHeightInLumaSamples)
				grid, colors, err := buildGrid(pat.kind, 0, (w+15)/16, (h+15)/16)
				if err != nil {
					t.Fatal(err)
				}
				idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
				if err != nil {
					t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
				}
				stream := append(prefix, idr...)

				ff := decodeWithFFmpeg(t, ffmpeg, stream)
				own := decodeWithHi265(t, stream, w, h, 1)
				if rep := compareYUV(t, own, ff, w, h, 1); !rep.exact() {
					t.Errorf("sign-hiding stream decodes differently in FFmpeg: %s", rep)
				}
			})
		}
	}
}

// TestSignHidingFiresOnlyOnDenseContent keeps the tests above from going vacuous.
//
// A sign is hidden only where a sub-block's significant coefficients span more
// than three scan positions, and most of what this generator produces never gets
// there: a flat CTU predicted from a uniform edge leaves a constant residual,
// whose transform is one DC coefficient. Only content that makes a CU's
// prediction vary across the block — a differently coloured neighbour — spreads
// coefficients far enough. So the patterns the tests above use must produce a
// different bitstream with the flag set than without, and the flat ones must
// produce an identical one; if that ever stops holding, those tests are no longer
// exercising sign hiding and this one says so.
func TestSignHidingFiresOnlyOnDenseContent(t *testing.T) {
	_, sps, pps := signHideParamSets(t, signHideFixtures[0])
	w := int(sps.PicWidthInLumaSamples)
	h := int(sps.PicHeightInLumaSamples)

	encode := func(t *testing.T, kind patternKind, hiding bool) []byte {
		t.Helper()
		grid, colors, err := buildGrid(kind, 0, (w+15)/16, (h+15)/16)
		if err != nil {
			t.Fatal(err)
		}
		p := *pps
		p.SignDataHidingEnabledFlag = hiding
		idr, err := EncodeIDRSliceFromSPSPPS(sps, &p, grid, colors)
		if err != nil {
			t.Fatal(err)
		}
		return idr
	}

	for _, pat := range signHidePatterns {
		t.Run("fires/"+pat.name, func(t *testing.T) {
			if bytes.Equal(encode(t, pat.kind, true), encode(t, pat.kind, false)) {
				t.Errorf("%s produces the same bitstream with sign hiding on and off, so "+
					"the sign-hiding tests are not exercising it any more", pat.name)
			}
		})
	}

	// Flat CTU content leaves one DC coefficient per sub-block, so the span is
	// always zero and nothing is ever hidden.
	for _, tc := range []struct {
		name string
		kind patternKind
	}{{"flat", patFlat}, {"ctu_checkerboard", patTiles}, {"smpte_bars", patSMPTE}} {
		t.Run("quiet/"+tc.name, func(t *testing.T) {
			if !bytes.Equal(encode(t, tc.kind, true), encode(t, tc.kind, false)) {
				t.Errorf("%s reaches sign hiding after all — it is a better test vector "+
					"than the comment claims, and signHidePatterns should say so", tc.name)
			}
		})
	}
}

// TestHideSignParityMatchesTheDecoderRule pins the rule itself: after
// hideSignParity, the parity of a sub-block's absolute levels says whether its
// lowest-frequency significant coefficient is negative, which is what a decoder
// reads it as. It also pins what the adjustment may not do — zero a level, or move
// either end of the significant run, since the significance map is already coded
// by then.
func TestHideSignParityMatchesTheDecoderRule(t *testing.T) {
	const size = 4 // one sub-block, so the scan positions are the whole block
	scan := scanOrderForTest(size)

	// Every arrangement of a few small levels over the sub-block, signs included.
	for _, levels := range [][]int32{
		{3, 0, 0, 0, 0, -1, 0, 0, 0, 0, 2, 0, 0, 0, 0, 1},
		{-3, 0, 0, 0, 0, 1, 0, 0, 0, 0, -2, 0, 0, 0, 0, 1},
		{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		{-1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, -1},
		{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		{-4, 1, -1, 1, -1, 1, -1, 1, -1, 1, -1, 1, -1, 1, -1, 5},
	} {
		first, last := -1, -1
		for n := range scan {
			if levels[scanIndexAt(scan, size, n)] != 0 {
				if first < 0 {
					first = n
				}
				last = n
			}
		}
		if last-first <= 3 {
			t.Fatalf("test case %v does not reach the span sign hiding needs", levels)
		}

		work := append([]int32(nil), levels...)
		hideSignParity(work, size, 0, 0, scan, first, last)

		// The invariant the decoder relies on.
		var sumAbs int32
		for n := first; n <= last; n++ {
			if v := work[scanIndexAt(scan, size, n)]; v < 0 {
				sumAbs -= v
			} else {
				sumAbs += v
			}
		}
		negative := work[scanIndexAt(scan, size, first)] < 0
		if (sumAbs%2 == 1) != negative {
			t.Errorf("levels %v: sum %d has the wrong parity for a %s first coefficient",
				levels, sumAbs, map[bool]string{true: "negative", false: "positive"}[negative])
		}

		// What it may not disturb.
		for i := range work {
			if (levels[i] == 0) != (work[i] == 0) {
				t.Errorf("levels %v: significance of coefficient %d changed (%d -> %d)",
					levels, i, levels[i], work[i])
			}
		}
		if got := work[scanIndexAt(scan, size, first)] < 0; got != (levels[scanIndexAt(scan, size, first)] < 0) {
			t.Errorf("levels %v: the sign of the first significant coefficient changed", levels)
		}
		// At most one coefficient moved, and by exactly one step.
		moved := 0
		for i := range work {
			if d := work[i] - levels[i]; d != 0 {
				moved++
				if d != 1 && d != -1 {
					t.Errorf("levels %v: coefficient %d moved by %d, want one step", levels, i, d)
				}
			}
		}
		if moved > 1 {
			t.Errorf("levels %v: %d coefficients moved, want at most 1", levels, moved)
		}
	}
}
