package encode

import (
	"os"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

func TestEncodeBlack16x16(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16, QP: 26}
	grid, _ := yuv.ParseGrid("A")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	annexB := append(vpsSPSPPS, idr...)

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	if f.Width != 16 || f.Height != 16 {
		t.Fatalf("dimensions: got %dx%d, want 16x16", f.Width, f.Height)
	}

	for i, v := range f.Y[:16*16] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}
	for i, v := range f.Cb[:8*8] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cb[%d] = %d, want 128 (±1)", i, v)
		}
	}
	for i, v := range f.Cr[:8*8] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cr[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

func TestEncodeBlack32x32(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26}
	grid, _ := yuv.ParseGrid("AA,AA")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	annexB := append(vpsSPSPPS, idr...)

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y[:32*32] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}
}

func TestEncodeGray16x16(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16, QP: 26}
	grid, _ := yuv.ParseGrid("A")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 128, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	annexB := append(vpsSPSPPS, idr...)

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y[:16*16] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Y[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

func TestEncodePSkip(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16, QP: 26}
	grid, _ := yuv.ParseGrid("A")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	pSkip, err := GeneratePSkip(p, 1)
	if err != nil {
		t.Fatalf("GeneratePSkip: %v", err)
	}

	stream := append(vpsSPSPPS, idr...)
	stream = append(stream, pSkip...)

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}

	idrFrame := frames[0]
	pFrame := frames[1]
	for i := range 16 * 16 {
		if idrFrame.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idrFrame.Y[i])
		}
	}
}

// TestEncodeIDRFromSPSPPS tests that EncodeIDRSliceFromSPSPPS generates a valid
// IDR slice by parsing SPS/PPS from an encoder-generated stream, then using the
// new API to produce a compatible IDR frame.
func TestEncodeIDRFromSPSPPS(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16, QP: 26}
	grid, _ := yuv.ParseGrid("A")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	nalus := avc.ExtractNalusFromByteStream(vpsSPSPPS)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU, spsNALU, ppsNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_VPS:
			vpsNALU = nalu
		case hevc.NALU_SPS:
			spsNALU = nalu
			sps, err = hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			ppsNALU = nalu
			pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		}
	}

	if sps == nil || pps == nil {
		t.Fatal("failed to parse SPS/PPS from encoded stream")
	}

	// Generate IDR slice using external SPS/PPS
	idrSliceBytes, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
	if err != nil {
		t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
	}

	// Build stream: VPS+SPS+PPS + new IDR slice
	idrNalus := avc.ExtractNalusFromByteStream(idrSliceBytes)
	allNalus := [][]byte{vpsNALU, spsNALU, ppsNALU}
	allNalus = append(allNalus, idrNalus...)

	dec := decoder.New()
	frames, err := dec.DecodeNALUs(allNalus)
	if err != nil {
		t.Fatalf("DecodeNALUs: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y[:16*16] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}
	for i, v := range f.Cb[:8*8] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cb[%d] = %d, want 128 (±1)", i, v)
		}
	}
	for i, v := range f.Cr[:8*8] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cr[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

// TestEncodeIDRAndPSkipFromSPSPPS tests the full workflow: encode IDR + P-skip
// both using external SPS/PPS from an initial encoder stream.
func TestEncodeIDRAndPSkipFromSPSPPS(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26}
	grid, _ := yuv.ParseGrid("AA,AA")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	nalus := avc.ExtractNalusFromByteStream(vpsSPSPPS)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU, spsNALU, ppsNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_VPS:
			vpsNALU = nalu
		case hevc.NALU_SPS:
			spsNALU = nalu
			sps, err = hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			ppsNALU = nalu
			pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		}
	}

	// Generate IDR using external SPS/PPS
	idrSliceBytes, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
	if err != nil {
		t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
	}

	// Generate P-skip using external SPS/PPS
	pSkipBytes, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
	}

	// Build stream: VPS+SPS+PPS + IDR + P-skip
	idrNalus := avc.ExtractNalusFromByteStream(idrSliceBytes)
	pNalus := avc.ExtractNalusFromByteStream(pSkipBytes)
	allNalus := [][]byte{vpsNALU, spsNALU, ppsNALU}
	allNalus = append(allNalus, idrNalus...)
	allNalus = append(allNalus, pNalus...)

	dec := decoder.New()
	frames, err := dec.DecodeNALUs(allNalus)
	if err != nil {
		t.Fatalf("DecodeNALUs: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}

	idrFrame := frames[0]
	pFrame := frames[1]
	for i := range 32 * 32 {
		if idrFrame.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idrFrame.Y[i])
		}
	}
}

// TestEncodePSkipFromSPSPPS tests that EncodePSkipSliceFromSPSPPS generates a valid
// P-skip slice by parsing SPS/PPS from an encoder-generated stream.
func TestEncodePSkipFromSPSPPS(t *testing.T) {
	p := EncodeParams{Width: 16, Height: 16, QP: 26}
	grid, _ := yuv.ParseGrid("A")
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idrBytes, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	fullStream := append(vpsSPSPPS, idrBytes...)
	nalus := avc.ExtractNalusFromByteStream(fullStream)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU, spsNALU, ppsNALU, idrNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_VPS:
			vpsNALU = nalu
		case hevc.NALU_SPS:
			spsNALU = nalu
			sps, err = hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			ppsNALU = nalu
			pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		case hevc.NALU_IDR_W_RADL, hevc.NALU_IDR_N_LP:
			idrNALU = nalu
		}
	}

	if sps == nil || pps == nil {
		t.Fatal("failed to parse SPS/PPS from encoded stream")
	}

	// Generate P-skip slice using the new API
	pSkipBytes, err := EncodePSkipSliceFromSPSPPS(sps, pps, 1)
	if err != nil {
		t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
	}

	// Decode IDR first
	dec := decoder.New()
	frames, err := dec.DecodeNALUs([][]byte{vpsNALU, spsNALU, ppsNALU, idrNALU})
	if err != nil {
		t.Fatalf("DecodeNALUs (IDR): %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 IDR frame, got %d", len(frames))
	}

	// Decode P-skip
	pNalus := avc.ExtractNalusFromByteStream(pSkipBytes)
	pFrames, err := dec.DecodeNALUs(pNalus)
	if err != nil {
		t.Fatalf("DecodeNALUs (P-skip): %v", err)
	}
	if len(pFrames) != 1 {
		t.Fatalf("expected 1 P-skip frame, got %d", len(pFrames))
	}

	idrFrame := frames[0]
	pFrame := pFrames[0]
	for i := range 16 * 16 {
		if idrFrame.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idrFrame.Y[i])
		}
	}
	for i := range 8 * 8 {
		if idrFrame.Cb[i] != pFrame.Cb[i] {
			t.Fatalf("P-frame Cb[%d] = %d, want %d (IDR)", i, pFrame.Cb[i], idrFrame.Cb[i])
		}
		if idrFrame.Cr[i] != pFrame.Cr[i] {
			t.Fatalf("P-frame Cr[%d] = %d, want %d (IDR)", i, pFrame.Cr[i], idrFrame.Cr[i])
		}
	}
}

// TestEncodePSkipFromSPSPPS_x265 tests compatibility with an x265-generated bitstream.
func TestEncodePSkipFromSPSPPS_x265(t *testing.T) {
	testFiles := []string{
		"../../testdata/gray_2frames_128x64.265",
	}

	for _, tf := range testFiles {
		t.Run(tf, func(t *testing.T) {
			data, err := readTestFile(tf)
			if err != nil {
				t.Skipf("test file not available: %v", err)
			}

			nalus := avc.ExtractNalusFromByteStream(data)
			spsMap := make(map[uint32]*hevc.SPS)
			var sps *hevc.SPS
			var pps *hevc.PPS

			for _, nalu := range nalus {
				if len(nalu) < 2 {
					continue
				}
				switch hevc.GetNaluType(nalu[0]) {
				case hevc.NALU_SPS:
					sps, err = hevc.ParseSPSNALUnit(nalu)
					if err != nil {
						t.Fatalf("ParseSPSNALUnit: %v", err)
					}
					spsMap[uint32(sps.SpsID)] = sps
				case hevc.NALU_PPS:
					pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
					if err != nil {
						t.Fatalf("ParsePPSNALUnit: %v", err)
					}
				}
			}

			if sps == nil || pps == nil {
				t.Fatal("failed to parse SPS/PPS from test bitstream")
			}

			pSkipBytes, err := EncodePSkipSliceFromSPSPPS(sps, pps, 2)
			if err != nil {
				t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
			}

			stream := make([]byte, 0, len(data)+len(pSkipBytes))
			stream = append(stream, data...)
			stream = append(stream, pSkipBytes...)

			dec := decoder.New()
			frames, err := dec.DecodeAnnexB(stream)
			if err != nil {
				t.Fatalf("DecodeAnnexB: %v", err)
			}

			if len(frames) < 2 {
				t.Fatalf("expected at least 2 frames, got %d", len(frames))
			}

			prev := frames[len(frames)-2]
			pSkip := frames[len(frames)-1]
			w := int(sps.PicWidthInLumaSamples)
			h := int(sps.PicHeightInLumaSamples)
			for i := range w * h {
				if prev.Y[i] != pSkip.Y[i] {
					t.Fatalf("P-skip Y[%d] = %d, want %d (previous frame)", i, pSkip.Y[i], prev.Y[i])
				}
			}
		})
	}
}

// TestEncodeTwoColor32x32 tests encoding a 32x32 frame with high-contrast content
// (left half Y=235, right half Y=16).
func TestEncodeTwoColor32x32(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26}
	grid, _ := yuv.ParseGrid("AB,AB")
	colors := yuv.ColorMap{
		'A': yuv.Color{Y: 235, Cb: 128, Cr: 128},
		'B': yuv.Color{Y: 16, Cb: 128, Cr: 128},
	}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	idr, err := GenerateIDR(p, grid, colors)
	if err != nil {
		t.Fatalf("GenerateIDR: %v", err)
	}
	annexB := append(vpsSPSPPS, idr...)

	d := decoder.New()
	frames, err := d.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	maxAllowed := 7
	for i := range 32 * 32 {
		expected := 235
		if i%32 >= 16 {
			expected = 16
		}
		actual := int(f.Y[i])
		diff := expected - actual
		if diff < 0 {
			diff = -diff
		}
		if diff > maxAllowed {
			t.Fatalf("Y[%d] diff=%d (expected=%d, actual=%d), max allowed=%d",
				i, diff, expected, actual, maxAllowed)
		}
	}
}

func readTestFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func makeUniform(n int, val uint8) []uint8 {
	buf := make([]uint8, n)
	for i := range buf {
		buf[i] = val
	}
	return buf
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
