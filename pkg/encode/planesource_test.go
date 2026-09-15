package encode

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
)

// TestGenerateIDRFromPlaneMatchesFFmpeg is the conformance test for
// GenerateIDRFromPlane's 8x8-quadrant path: a BlockSize=8 PlaneGrid carries one
// value per 8x8 quadrant, so — unlike a flat-per-CTU Grid — content genuinely
// varies within a single 16x16 CTU. Encoding it with Use8x8CU must decode
// identically in FFmpeg and this package's own decoder, and reproduce the
// intended pattern within quantization error.
func TestGenerateIDRFromPlaneMatchesFFmpeg(t *testing.T) {
	pg, w, h := buildQuadrantPlane(t)

	p := EncodeParams{Width: w, Height: h, QP: 26, Use8x8CU: true}
	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDRFromPlane(p, pg)
	if err != nil {
		t.Fatalf("GenerateIDRFromPlane: %v", err)
	}
	stream := append(vpsSPSPPS, idr...)

	ff := cuFFmpegDecode(t, stream)
	own := cuHi265Decode(t, stream, 1)

	if !bytes.Equal(ff, own) {
		mean, maxDiff := cuPlaneDiff(t, own, ff)
		t.Fatalf("hi265dec and FFmpeg disagree: mean=%.3f max=%d", mean, maxDiff)
	}

	f := yuv.BuildFrameFromPlaneGrid(pg)
	mean, maxDiff := cuPlaneDiff(t, own, cuIntendedYUV(f.Y, f.Cb, f.Cr))
	t.Logf("plane path vs intended pattern: mean=%.3f max=%d", mean, maxDiff)
	if maxDiff > 12 {
		t.Errorf("max error %d vs intended pattern exceeds 12", maxDiff)
	}
	if mean > 1.5 {
		t.Errorf("mean error %.3f vs intended pattern exceeds 1.5", mean)
	}
}

// TestGenerateIDRFromPlaneMatchesGenerateIDRAt16x16 pins the doc comment's claim
// on GenerateIDRFromPlane: a BlockSize=16 PlaneGrid (one value per whole CTU, same
// resolution as a Grid+ColorMap) must encode byte-identical to GenerateIDR fed the
// equivalent Grid+ColorMap. This is what proves the plane path is a strict
// generalisation of the grid path rather than a separate, possibly-diverging one.
func TestGenerateIDRFromPlaneMatchesGenerateIDRAt16x16(t *testing.T) {
	grid, err := yuv.ParseGrid("ABCD,CDAB")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	w, h := grid.Width*16, grid.Height*16
	p := EncodeParams{Width: w, Height: h, QP: 26}

	wantIDR, err := GenerateIDR(p, grid, quadrantColors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}

	pg, err := yuv.GridToPlaneGridBS(grid, quadrantColors, 16)
	if err != nil {
		t.Fatalf("GridToPlaneGridBS: %v", err)
	}
	gotIDR, err := GenerateIDRFromPlane(p, pg)
	if err != nil {
		t.Fatalf("GenerateIDRFromPlane: %v", err)
	}

	if !bytes.Equal(wantIDR, gotIDR) {
		t.Errorf("GenerateIDRFromPlane at BlockSize=16 diverged from GenerateIDR on the "+
			"same content:\n got %d bytes\nwant %d bytes", len(gotIDR), len(wantIDR))
	}
}

// buildQuadrantPlane builds an 8x8-block-resolution PlaneGrid straight from
// quadrantPattern/quadrantColors (see cu8x8_test.go), the same content
// build8x8Planes renders to raw YUV — but kept as a PlaneGrid rather than
// expanded planes, since that is what GenerateIDRFromPlane actually consumes.
func buildQuadrantPlane(t *testing.T) (pg *yuv.PlaneGrid, w, h int) {
	t.Helper()

	grid, err := yuv.ParseGrid(strings.Join(quadrantPattern, ","))
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	pg, err = yuv.GridToPlaneGridBS(grid, quadrantColors, 8)
	if err != nil {
		t.Fatalf("GridToPlaneGridBS: %v", err)
	}
	return pg, pg.PixelWidth(), pg.PixelHeight()
}
