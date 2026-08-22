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

// tiledParamSets returns the parameter sets of a committed tiled vector: a 2x2
// tile grid over a 128x128 picture, 8-bit 4:2:0.
func tiledParamSets(t *testing.T) (annexBPrefix []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	data, err := os.ReadFile("../../testdata/tiles_multi_2x2_128x128.265")
	if err != nil {
		t.Fatal(err)
	}
	vps, spsNALU, ppsNALU, sps, pps := parseParamSets(t, data)
	if !pps.TilesEnabledFlag {
		t.Fatal("fixture is supposed to have tiles enabled")
	}
	var buf bytes.Buffer
	for _, nalu := range [][]byte{vps, spsNALU, ppsNALU} {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nalu)
	}
	return buf.Bytes(), sps, pps
}

// decodeGray decodes a generated picture and checks every sample is the mid-grey
// that DC prediction with no residual produces.
func decodeGray(t *testing.T, stream []byte, width, height int) {
	t.Helper()

	frames, err := decoder.New().DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("decoded %d frames, want 1", len(frames))
	}
	f := frames[0]
	if f.Width != width || f.Height != height {
		t.Fatalf("decoded %dx%d, want %dx%d", f.Width, f.Height, width, height)
	}
	for i, v := range f.YUV420Bytes() {
		if v != 128 {
			t.Fatalf("sample %d is %d, want the mid-grey 128 everywhere", i, v)
		}
	}
}

// A tiled parameter set makes a picture several slice segments, one per tile, so
// the encoder emits one NALU per tile and the decoder has to put them back
// together. Mid-grey is the content that makes this checkable without a golden:
// DC prediction with no neighbours and no residual is exactly the mid-point, so
// every tile must come out identical whatever its availability.
func TestEncodeGrayIDRWithTiles(t *testing.T) {
	prefix, sps, pps := tiledParamSets(t)

	slices, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		t.Fatalf("EncodeGrayIDRSliceFromSPSPPS: %v", err)
	}
	nalus := avc.ExtractNalusFromByteStream(slices)
	if len(nalus) != 4 {
		t.Fatalf("emitted %d slice NALUs, want one per tile (4)", len(nalus))
	}
	decodeGray(t, append(prefix, slices...), 128, 128)
}

// The same for the CRA, which is the Gradual Decoder Refresh primitive: a tiled
// stream is exactly where a refresh point has to be spliced in per tile.
func TestEncodeGrayCRAWithTiles(t *testing.T) {
	prefix, sps, pps := tiledParamSets(t)

	slices, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, 42)
	if err != nil {
		t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
	}
	nalus := avc.ExtractNalusFromByteStream(slices)
	if len(nalus) != 4 {
		t.Fatalf("emitted %d slice NALUs, want one per tile (4)", len(nalus))
	}
	for i, nalu := range nalus {
		if got := hevc.GetNaluType(nalu[0]); got != hevc.NALU_CRA {
			t.Errorf("segment %d has NALU type %s, want CRA", i, got)
		}
	}
	decodeGray(t, append(prefix, slices...), 128, 128)
}

// A P-skip picture with tiles, which is what hi265-mp4-extend appends when the
// stream it extends is tiled. Every CU is a zero-motion skip, so the picture must
// come out identical to the gray IDR before it — and cu_skip_flag's context has
// to stop counting neighbours at the tile edge, or CABAC desyncs.
func TestEncodePSkipWithTiles(t *testing.T) {
	prefix, sps, pps := tiledParamSets(t)

	idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		t.Fatalf("gray IDR: %v", err)
	}
	pskip, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("P-skip: %v", err)
	}
	if n := len(avc.ExtractNalusFromByteStream(pskip)); n != 4 {
		t.Fatalf("P-skip emitted %d slice NALUs, want one per tile (4)", n)
	}

	stream := append(append(prefix, idr...), pskip...)
	frames, err := decoder.New().DecodeAnnexB(stream)
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

// Tiles from this package's own parameter sets, which is what makes tiled test
// vectors possible without an external encoder. The content check is the point:
// the encoder must predict from exactly the samples the decoder will have, so a
// tile boundary may not degrade the picture. It used to, because the encoder kept
// its own copy of the reference sample construction with an older availability
// rule; the two now share one implementation in internal/pred.
func TestGenerateTiledIDRMatchesSource(t *testing.T) {
	// Eight cells per row, so the pattern string steps by nine with the comma.
	const pattern = "xyxyxyxy,yxyxyxyx,xxyyxxyy,yyxxyyxx,xyxyxyxy,yxyxyxyx,xxyyxxyy,yyxxyyxx"
	colors := yuv.ColorMap{
		'x': yuv.Color{Y: 235, Cb: 128, Cr: 128},
		'y': yuv.Color{Y: 40, Cb: 90, Cr: 200},
	}
	wantY := map[byte]int{'x': 235, 'y': 40}

	for _, tc := range []struct {
		name       string
		cols, rows int
		wantNALUs  int
	}{
		{"no tiles", 1, 1, 1},
		{"2x2", 2, 2, 4},
		{"4 columns", 4, 1, 4},
		{"8 rows", 1, 8, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grid, err := yuv.ParseGrid(pattern)
			if err != nil {
				t.Fatal(err)
			}
			p := EncodeParams{Width: 128, Height: 128, QP: 26, TileCols: tc.cols, TileRows: tc.rows}
			ps, err := GenerateVPSSPSPPS(p)
			if err != nil {
				t.Fatal(err)
			}
			idr, err := GenerateIDR(p, grid, colors)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(avc.ExtractNalusFromByteStream(idr)); n != tc.wantNALUs {
				t.Fatalf("emitted %d slice NALUs, want %d", n, tc.wantNALUs)
			}

			frames, err := decoder.New().DecodeAnnexB(append(ps, idr...))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			f := frames[0]
			for by := range 8 {
				for bx := range 8 {
					want := wantY[pattern[by*9+bx]]
					for y := by * 16; y < (by+1)*16; y++ {
						for x := bx * 16; x < (bx+1)*16; x++ {
							if got := int(f.Y[y*f.StrideY+x]); got < want-2 || got > want+2 {
								t.Fatalf("pixel (%d,%d) is %d, want %d±2 — a tile boundary must not "+
									"change what the picture looks like", x, y, got, want)
							}
						}
					}
				}
			}
		})
	}
}
