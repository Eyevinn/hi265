package encode

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// Two committed vectors whose PPS carries non-zero pps_cb_qp_offset and
// pps_cr_qp_offset. The signs differ between the components in both, so a Cb
// offset applied to Cr — or one offset applied to both — cannot pass.
var chromaQPOffsetFixtures = []struct {
	path   string
	cb, cr int
}{
	{"../../testdata/chromaqpoffs_192x96.265", 6, -6},
	{"../../testdata/chromaqpoffs_negcb_192x96.265", -5, 4},
}

func chromaQPOffsetParamSets(t *testing.T, path string, wantCb, wantCr int) (
	annexBPrefix []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vps, spsNALU, ppsNALU, sps, pps := parseParamSets(t, data)
	if int(pps.CbQpOffset) != wantCb || int(pps.CrQpOffset) != wantCr {
		t.Fatalf("%s has pps_cb/cr_qp_offset %d/%d, want %d/%d",
			path, pps.CbQpOffset, pps.CrQpOffset, wantCb, wantCr)
	}
	var buf bytes.Buffer
	for _, nalu := range [][]byte{vps, spsNALU, ppsNALU} {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nalu)
	}
	return buf.Bytes(), sps, pps
}

// TestEncodeIDRWithChromaQPOffsets catches an encoder that quantizes chroma at a
// QP the decoder will not dequantize with.
//
// Such an encoder still writes a syntactically perfect bitstream, so every decoder
// agrees — on the wrong picture. Verified by making only the encoder ignore the
// offsets: this test fails at max delta 62 and 39 against the source (4 when they
// are honoured) while the FFmpeg comparison passes.
//
// The reverse holds too, which is why both tests exist: break the shared
// derivation and encoder and decoder go wrong together, the round trip through our
// own decoder reproduces the source, this test passes, and only FFmpeg notices.
// Verified that way as well.
func TestEncodeIDRWithChromaQPOffsets(t *testing.T) {
	for _, f := range chromaQPOffsetFixtures {
		t.Run(shortName(f.path), func(t *testing.T) {
			prefix, sps, pps := chromaQPOffsetParamSets(t, f.path, f.cb, f.cr)
			w := int(sps.PicWidthInLumaSamples)
			h := int(sps.PicHeightInLumaSamples)
			grid, colors, err := buildGrid(patSMPTE, 0, (w+15)/16, (h+15)/16)
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
			src, err := yuv.BuildFrame(grid, colors)
			if err != nil {
				t.Fatal(err)
			}
			src.Width, src.Height = w, h

			pe := measurePatternError(frames[0].YUV420Bytes(), src.YUV420Bytes())
			t.Logf("max delta %d, mean abs %.4f", pe.maxDelta, pe.meanAbs)
			if pe.maxDelta > 8 || pe.meanAbs > 1.0 {
				t.Errorf("decoded picture drifts from the source: max delta %d (limit 8), "+
					"mean abs %.4f (limit 1.0) — the chroma was quantized at the wrong QP",
					pe.maxDelta, pe.meanAbs)
			}
		})
	}
}

// The bitstream still has to be right, which is what an independent decoder is
// for. This catches a chroma QP that is wrong on both sides of our own code, and
// anything that shifts a bin; it is blind to an encoder-only error, since our
// decoder would then be the one telling the truth. See
// TestEncodeIDRWithChromaQPOffsets.
func TestEncodeIDRWithChromaQPOffsetsAgainstFFmpeg(t *testing.T) {
	ffmpeg := ffmpegBin(t)

	for _, f := range chromaQPOffsetFixtures {
		t.Run(shortName(f.path), func(t *testing.T) {
			prefix, sps, pps := chromaQPOffsetParamSets(t, f.path, f.cb, f.cr)
			w := int(sps.PicWidthInLumaSamples)
			h := int(sps.PicHeightInLumaSamples)
			grid, colors, err := buildGrid(patSMPTE, 0, (w+15)/16, (h+15)/16)
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
				t.Errorf("chroma-QP-offset stream decodes differently in FFmpeg: %s", rep)
			}
		})
	}
}

// The two components must not end up sharing an offset, which is the mistake a
// single chromaQP value for a whole transform unit would make. Swapping the
// offsets has to change the bitstream; it would not if only one were used, or if
// the same one were used for both.
func TestChromaQPOffsetsAreUsedPerComponent(t *testing.T) {
	_, sps, pps := chromaQPOffsetParamSets(t, chromaQPOffsetFixtures[0].path,
		chromaQPOffsetFixtures[0].cb, chromaQPOffsetFixtures[0].cr)
	w := int(sps.PicWidthInLumaSamples)
	h := int(sps.PicHeightInLumaSamples)
	grid, colors, err := buildGrid(patSMPTE, 0, (w+15)/16, (h+15)/16)
	if err != nil {
		t.Fatal(err)
	}

	encode := func(cb, cr int8) []byte {
		p := *pps
		p.CbQpOffset = cb
		p.CrQpOffset = cr
		idr, err := EncodeIDRSliceFromSPSPPS(sps, &p, grid, colors)
		if err != nil {
			t.Fatal(err)
		}
		return idr
	}

	straight := encode(pps.CbQpOffset, pps.CrQpOffset)
	swapped := encode(pps.CrQpOffset, pps.CbQpOffset)
	if bytes.Equal(straight, swapped) {
		t.Error("swapping pps_cb_qp_offset and pps_cr_qp_offset left the bitstream " +
			"unchanged, so the two components are not being scaled independently")
	}
	if bytes.Equal(straight, encode(0, 0)) {
		t.Error("zeroing both offsets left the bitstream unchanged, so they are not " +
			"reaching the chroma quantizer at all")
	}
}

// A per-CU chroma QP offset needs cu_chroma_qp_offset_flag and
// cu_chroma_qp_offset_idx in the transform unit, which nothing here writes, so it
// is refused rather than silently dropped like the picture-level offsets used to
// be.
func TestChromaQPOffsetListIsRefused(t *testing.T) {
	_, sps, pps := chromaQPOffsetParamSets(t, chromaQPOffsetFixtures[0].path,
		chromaQPOffsetFixtures[0].cb, chromaQPOffsetFixtures[0].cr)
	grid, colors, err := buildGrid(patSMPTE, 0, 12, 6)
	if err != nil {
		t.Fatal(err)
	}
	p := *pps
	p.RangeExtension = &hevc.RangeExtension{ChromaQpOffsetListEnabledFlag: true}
	if _, err := EncodeIDRSliceFromSPSPPS(sps, &p, grid, colors); err == nil {
		t.Error("expected chroma_qp_offset_list_enabled_flag to be refused")
	}
	// The gray writer codes no coefficients, so no chroma QP applies to it at all
	// and it has nothing to refuse.
	if _, err := EncodeGrayIDRSliceFromSPSPPS(sps, &p); err != nil {
		t.Errorf("the gray writer uses no chroma QP and should not refuse: %v", err)
	}
}

// The writers refuse the parameter sets they cannot honour, and only those. Each
// flag below would otherwise put a bin in the coding unit that the writer never
// emits — the decoder then reads one that is not there — or leave the residual
// quantized with weights the decoder will not use.
//
// The gray writer is deliberately more permissive: it codes no coefficients and
// no residual, so bit depth, chroma format, scaling lists and the chroma QP
// offsets cannot reach it. Only syntax in the coding unit itself does.
func TestWritersRefuseWhatTheyCannotHonour(t *testing.T) {
	_, sps, pps := chromaQPOffsetParamSets(t, chromaQPOffsetFixtures[0].path,
		chromaQPOffsetFixtures[0].cb, chromaQPOffsetFixtures[0].cr)
	grid, colors, err := buildGrid(patSMPTE, 0, 12, 6)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		want       string
		grayToo    bool // whether the gray writer must refuse it as well
		breakParam func(sps *hevc.SPS, pps *hevc.PPS)
	}{
		// In the coding unit, so every writer needs it.
		{"transquant_bypass", "transquant_bypass_enabled_flag", true,
			func(_ *hevc.SPS, p *hevc.PPS) { p.TransquantBypassEnabledFlag = true }},
		{"pcm", "pcm_enabled_flag", true,
			func(s *hevc.SPS, _ *hevc.PPS) { s.PCMEnabledFlag = true }},
		// Only where residuals are written.
		{"scaling_lists", "scaling_list_enabled_flag", false,
			func(s *hevc.SPS, _ *hevc.PPS) { s.ScalingListEnabledFlag = true }},
		{"bit_depth", "8-bit", false,
			func(s *hevc.SPS, _ *hevc.PPS) { s.BitDepthLumaMinus8 = 2 }},
		{"chroma_format", "chroma_format_idc", false,
			func(s *hevc.SPS, _ *hevc.PPS) { s.ChromaFormatIDC = 3 }},
		{"persistent_rice", "persistent_rice_adaptation", false,
			func(s *hevc.SPS, _ *hevc.PPS) {
				s.RangeExtension = &hevc.SPSRangeExtension{PersistentRiceAdaptationEnabledFlag: true}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brokenSPS, brokenPPS := *sps, *pps
			tc.breakParam(&brokenSPS, &brokenPPS)

			_, err := EncodeIDRSliceFromSPSPPS(&brokenSPS, &brokenPPS, grid, colors)
			if err == nil {
				t.Fatalf("the grid IDR writer should refuse %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error should mention %q, got: %v", tc.want, err)
			}

			_, grayErr := EncodeGrayIDRSliceFromSPSPPS(&brokenSPS, &brokenPPS)
			switch {
			case tc.grayToo && grayErr == nil:
				t.Errorf("the gray writer should refuse %s too: it is coding unit syntax", tc.name)
			case !tc.grayToo && grayErr != nil:
				t.Errorf("the gray writer codes no residual and should accept %s: %v",
					tc.name, grayErr)
			}
		})
	}
}
