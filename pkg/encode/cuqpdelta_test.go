package encode

import (
	"bytes"
	"os"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// cuQpDeltaCase is one external parameter set with cu_qp_delta enabled.
//
// diff_cu_qp_delta_depth is what makes these interesting, and it cannot come
// from x265: with a 16x16 CTU it only ever writes 0, so a depth of 1 — a
// quantization group of 8x8, four to a CTB — has no real-encoder source. These
// build the parameter sets with this package's own writers instead and hand them
// back through the external-SPS/PPS entry points, which is the same code path a
// real encoder's parameter sets take. testdata/cuqp_crf32_128x128.265 covers the
// real-encoder side; see TestEncodeIDRWithRealCuQpDeltaPPS.
type cuQpDeltaCase struct {
	name          string
	width, height int
	// use8x8CU picks the minimum coding block size, 8 when set and 16 otherwise.
	// diff_cu_qp_delta_depth may not exceed
	// log2_diff_max_min_luma_coding_block_size, so a depth of 1 needs it.
	use8x8CU bool
	depth    int
	qp       int
	// maxPatternDelta is how far quantization alone may move a flat CTU pattern,
	// which grows with the quantizer step and so with the QP.
	maxPatternDelta int
}

func cuQpDeltaCases() []cuQpDeltaCase {
	return []cuQpDeltaCase{
		// The group is the whole CTB: one cu_qp_delta per CTB at most.
		{name: "group_16x16_mincb16", width: 128, height: 128, depth: 0, qp: 26, maxPatternDelta: 4},
		{name: "group_16x16_mincb8", width: 128, height: 128, use8x8CU: true,
			depth: 0, qp: 26, maxPatternDelta: 4},
		// The group is one 8x8 CU, so every CU that codes a coefficient opens a
		// group of its own and four deltas fit in a CTB.
		{name: "group_8x8", width: 128, height: 128, use8x8CU: true, depth: 1, qp: 26, maxPatternDelta: 4},
		// Partial CTBs at the bottom edge. Spec 7.3.8.4 does not recurse into a
		// quadtree node that begins outside the picture, so such a node opens no
		// quantization group — getting that wrong shifts every delta after the
		// first ragged CTB.
		{name: "group_16x16_short_ctb_row", width: 128, height: 72, use8x8CU: true,
			depth: 0, qp: 26, maxPatternDelta: 4},
		{name: "group_8x8_short_ctb_row", width: 128, height: 72, use8x8CU: true,
			depth: 1, qp: 26, maxPatternDelta: 4},
		// Partial CTBs at the right edge as well. This one carries no intended
		// picture to compare against: see hasIntendedPicture.
		{name: "group_8x8_narrow_ctb_column", width: 120, height: 72, use8x8CU: true, depth: 1, qp: 26},
		// A coarse quantizer leaves most blocks with nothing to code, so most
		// groups write no delta at all; a fine one makes almost every group write
		// one.
		{name: "group_8x8_qp40", width: 128, height: 128, use8x8CU: true, depth: 1, qp: 40, maxPatternDelta: 14},
		{name: "group_8x8_qp10", width: 128, height: 128, use8x8CU: true, depth: 1, qp: 10, maxPatternDelta: 2},
	}
}

// paramSets builds the VPS/SPS/PPS for the case and parses the SPS and PPS back,
// which is how the encoder is meant to receive someone else's parameter sets.
func (c cuQpDeltaCase) paramSets(t *testing.T) (annexBPrefix []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	vps := generateVPS()
	spsRBSP := generateSPS(c.width, c.height, yuv.BT601, yuv.LimitedRange, c.use8x8CU)
	ppsRBSP := generatePPS(c.qp, 1, 1, false, c.depth)

	var buf bytes.Buffer
	WriteNALU(&buf, naluVPS, vps)
	WriteNALU(&buf, naluSPS, spsRBSP)
	WriteNALU(&buf, naluPPS, ppsRBSP)

	_, _, _, sps, pps = parseParamSets(t, buf.Bytes())
	if !pps.CuQpDeltaEnabledFlag {
		t.Fatal("cu_qp_delta_enabled_flag is not set in the generated PPS")
	}
	if got := int(pps.DiffCuQpDeltaDepth); got != c.depth {
		t.Fatalf("diff_cu_qp_delta_depth = %d, want %d", got, c.depth)
	}
	return buf.Bytes(), sps, pps
}

// hasIntendedPicture reports whether the case can be compared against the
// picture its grid describes.
//
// yuv.BuildFrame lays a grid out at grid.Width*16 samples per row, and the
// external-SPS/PPS entry points then reset the frame's declared width to the
// SPS's. When the two differ — any picture whose width is not a multiple of 16 —
// the encoder walks the source planes with the wrong stride, so both the coded
// picture and any reference built from the same grid are meaningless. That is a
// defect in how the grid entry points take a frame, not in anything here, and it
// is why such a case is still worth running against FFmpeg (the bitstream is
// self-consistent and has to decode identically) but not against the pattern.
// Note that only the width matters: a short bottom CTB row strides correctly.
func (c cuQpDeltaCase) hasIntendedPicture() bool {
	return c.width%16 == 0
}

// TestEncodeIDRWithCuQpDelta writes a grid IDR against parameter sets that
// enable cu_qp_delta and checks the picture comes back.
//
// Without the element the bitstream is not merely suboptimal, it is unparseable
// where the decoder expects it: spec 7.3.8.10 codes cu_qp_delta_abs at the first
// transform unit of a quantization group that carries any coefficient, so
// leaving it out desynchronises CABAC from that point on. Before this landed the
// same streams decoded to garbage — a 120x72 case missed 1514 samples, an 8x8
// group case 24431 of 24576, and one failed outright in pkg/decoder.
func TestEncodeIDRWithCuQpDelta(t *testing.T) {
	for _, c := range cuQpDeltaCases() {
		t.Run(c.name, func(t *testing.T) {
			if !c.hasIntendedPicture() {
				t.Skip("no intended picture to compare against; see hasIntendedPicture")
			}
			prefix, sps, pps := c.paramSets(t)
			grid, colors, err := buildGrid(patTiles, 0, (c.width+15)/16, (c.height+15)/16)
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

			// The intended picture, quantized at the slice QP. Every CU is coded
			// at SliceQpY and the delta written is always zero, so the decoded QP
			// has to be that same QP — a wrong one shows up here as a picture that
			// drifts from the source far more than quantization alone explains.
			f, err := yuv.BuildFrame(grid, colors)
			if err != nil {
				t.Fatal(err)
			}
			f.Width, f.Height = c.width, c.height
			pe := measurePatternError(frames[0].YUV420Bytes(), f.YUV420Bytes())
			t.Logf("%s: max delta %d, mean abs %.4f (QP %d)", c.name, pe.maxDelta, pe.meanAbs, c.qp)
			if pe.maxDelta > c.maxPatternDelta {
				t.Errorf("decoded picture drifts from the source: max delta %d, limit %d",
					pe.maxDelta, c.maxPatternDelta)
			}
		})
	}
}

// TestEncodeIDRWithCuQpDeltaAgainstFFmpeg is the assertion that the element is
// written in the right place with the right binarisation: FFmpeg has to agree
// with pkg/decoder sample for sample. Our own decoder alone cannot settle it —
// it reads cu_qp_delta_abs with the same two context models the encoder writes
// it with, so the two would agree on a wrong binarisation.
func TestEncodeIDRWithCuQpDeltaAgainstFFmpeg(t *testing.T) {
	ffmpeg := ffmpegBin(t)

	for _, c := range cuQpDeltaCases() {
		t.Run(c.name, func(t *testing.T) {
			prefix, sps, pps := c.paramSets(t)
			grid, colors, err := buildGrid(patTiles, 0, (c.width+15)/16, (c.height+15)/16)
			if err != nil {
				t.Fatal(err)
			}
			idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
			if err != nil {
				t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
			}
			stream := append(prefix, idr...)

			ff := decodeWithFFmpeg(t, ffmpeg, stream)
			own := decodeWithHi265(t, stream, c.width, c.height, 1)
			if rep := compareYUV(t, own, ff, c.width, c.height, 1); !rep.exact() {
				t.Errorf("cu_qp_delta stream decodes differently in FFmpeg: %s", rep)
			}
		})
	}
}

// TestEncodeIDRWithRealCuQpDeltaPPS uses parameter sets a real encoder wrote:
// x265 at CRF 32 with a 16x16 CTU, which turns cu_qp_delta on to let its rate
// control vary the QP per CTB. It guards against the generated parameter sets and
// our own reader agreeing on a field layout no other encoder writes.
//
// The other x265 defaults are off in this vector because the grid encoder does
// not implement them: sign data hiding above all, which changes how the last sign
// of a coefficient group is coded, and weighted prediction, which it refuses
// outright. That is what a default-settings x265 PPS still runs into here, and
// it is unrelated to cu_qp_delta.
func TestEncodeIDRWithRealCuQpDeltaPPS(t *testing.T) {
	data, err := os.ReadFile("../../testdata/cuqp_crf32_128x128.265")
	if err != nil {
		t.Fatal(err)
	}
	vps, spsNALU, ppsNALU, sps, pps := parseParamSets(t, data)
	if !pps.CuQpDeltaEnabledFlag {
		t.Fatal("fixture is supposed to have cu_qp_delta enabled")
	}
	if pps.SignDataHidingEnabledFlag {
		t.Fatal("fixture is supposed to have sign data hiding off, which this encoder cannot write")
	}

	var prefix bytes.Buffer
	for _, nalu := range [][]byte{vps, spsNALU, ppsNALU} {
		prefix.Write([]byte{0, 0, 0, 1})
		prefix.Write(nalu)
	}

	w := int(sps.PicWidthInLumaSamples)
	h := int(sps.PicHeightInLumaSamples)
	grid, colors, err := buildGrid(patTiles, 0, (w+15)/16, (h+15)/16)
	if err != nil {
		t.Fatal(err)
	}
	idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
	if err != nil {
		t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
	}
	if n := len(avc.ExtractNalusFromByteStream(idr)); n != 1 {
		t.Fatalf("emitted %d slice NALUs, want 1", n)
	}
	stream := append(prefix.Bytes(), idr...)

	frames, err := decoder.New().DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("decoded %d frames, want 1", len(frames))
	}
	t.Run("ffmpeg", func(t *testing.T) {
		ffmpeg := ffmpegBin(t)
		ff := decodeWithFFmpeg(t, ffmpeg, stream)
		own := decodeWithHi265(t, stream, w, h, 1)
		if rep := compareYUV(t, own, ff, w, h, 1); !rep.exact() {
			t.Errorf("stream against a real cu_qp_delta PPS decodes differently in FFmpeg: %s", rep)
		}
	})
}

// TestCRAWithCuQpDelta covers the other IRAP the same writer produces, since a
// CRA at a chosen POC is the picture a GDR splice actually inserts — and a
// wavefront or rate-controlled stream is exactly where one gets spliced.
func TestCRAWithCuQpDelta(t *testing.T) {
	c := cuQpDeltaCase{
		name: "cra", width: 128, height: 128, use8x8CU: true, depth: 1,
		qp: 26, maxPatternDelta: 4,
	}
	prefix, sps, pps := c.paramSets(t)
	grid, colors, err := buildGrid(patTiles, 0, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	cra, err := EncodeCRASliceFromSPSPPS(sps, pps, grid, colors, 42)
	if err != nil {
		t.Fatalf("EncodeCRASliceFromSPSPPS: %v", err)
	}
	nalus := avc.ExtractNalusFromByteStream(cra)
	if got := hevc.GetNaluType(nalus[0][0]); got != hevc.NALU_CRA {
		t.Errorf("NALU type is %s, want CRA", got)
	}

	frames, err := decoder.New().DecodeAnnexB(append(prefix, cra...))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("decoded %d frames, want 1", len(frames))
	}
	f, err := yuv.BuildFrame(grid, colors)
	if err != nil {
		t.Fatal(err)
	}
	f.Width, f.Height = c.width, c.height
	if pe := measurePatternError(frames[0].YUV420Bytes(), f.YUV420Bytes()); pe.maxDelta > c.maxPatternDelta {
		t.Errorf("decoded CRA drifts from the source: max delta %d, limit %d",
			pe.maxDelta, c.maxPatternDelta)
	}
}

// The gray and P-skip writers need no cu_qp_delta whatever the PPS says, and this
// pins the reason rather than leaving it to chance. Spec 7.3.8.10 codes the
// element inside transform_unit, only for a unit that carries a coefficient: a
// gray picture has every cbf clear, and a skipped CU has no transform tree at
// all. If either ever started coding coefficients, this test would fail rather
// than the bitstream silently going wrong.
func TestGrayAndPSkipNeedNoCuQpDelta(t *testing.T) {
	c := cuQpDeltaCase{name: "gray", width: 128, height: 128, use8x8CU: true, depth: 1, qp: 26}
	prefix, sps, pps := c.paramSets(t)

	idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		t.Fatalf("gray IDR: %v", err)
	}
	pskip, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("P-skip: %v", err)
	}

	stream := append(append(prefix, idr...), pskip...)
	frames, err := decoder.New().DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("decoded %d frames, want 2", len(frames))
	}
	for i, f := range frames {
		for j, v := range f.YUV420Bytes() {
			if v != 128 {
				t.Fatalf("frame %d sample %d is %d, want the mid-grey 128 everywhere", i, j, v)
			}
		}
	}
}
