package encode

import (
	"os"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

func TestEncodeBlack16x16(t *testing.T) {
	width, height := 16, 16
	qp := 26

	// Black frame: Y=16 (BT.601 black), Cb=Cr=128 (neutral chroma)
	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	annexB, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	// Decode with our decoder
	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]

	// Verify dimensions
	if f.Width != width || f.Height != height {
		t.Fatalf("dimensions: got %dx%d, want %dx%d", f.Width, f.Height, width, height)
	}

	// Verify Y pixels
	for i, v := range f.Y[:width*height] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}

	// Verify Cb pixels
	for i, v := range f.Cb[:width/2*height/2] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cb[%d] = %d, want 128 (±1)", i, v)
		}
	}

	// Verify Cr pixels
	for i, v := range f.Cr[:width/2*height/2] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cr[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

func TestEncodeBlack32x32(t *testing.T) {
	width, height := 32, 32
	qp := 26

	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	annexB, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y[:width*height] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}
}

func TestEncodeGray16x16(t *testing.T) {
	width, height := 16, 16
	qp := 26

	// Gray frame: Y=128
	y := makeUniform(width*height, 128)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	annexB, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y[:width*height] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Y[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

func TestEncodePSkip(t *testing.T) {
	width, height := 16, 16
	qp := 26

	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	// Encode IDR
	idrBytes, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	// Encode P-skip
	pBytes, err := EncodePSkipFrame(EncodeParams{Width: width, Height: height, QP: qp}, 1)
	if err != nil {
		t.Fatalf("EncodePSkipFrame: %v", err)
	}

	// Concatenate and decode
	stream := make([]byte, 0, len(idrBytes)+len(pBytes))
	stream = append(stream, idrBytes...)
	stream = append(stream, pBytes...)

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}

	// P-frame should match IDR frame (copy)
	idr := frames[0]
	pFrame := frames[1]
	for i := range width * height {
		if idr.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idr.Y[i])
		}
	}
}

// TestEncodeIDRFromSPSPPS tests that EncodeIDRSliceFromSPSPPS generates a valid
// IDR slice by parsing SPS/PPS from an encoder-generated stream, then using the
// new API to produce a compatible IDR frame.
func TestEncodeIDRFromSPSPPS(t *testing.T) {
	width, height := 16, 16
	qp := 26

	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	// Encode IDR frame (includes VPS+SPS+PPS+IDR) to get parameter sets
	idrBytes, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	nalus := avc.ExtractNalusFromByteStream(idrBytes)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU []byte
	var spsNALU []byte
	var ppsNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])
		switch naluType {
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
	idrSliceBytes, err := EncodeIDRSliceFromSPSPPS(sps, pps, y, cb, cr)
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

	// Verify decoded pixels match input (±1 for quantization)
	f := frames[0]
	for i, v := range f.Y[:width*height] {
		if abs(int(v)-16) > 1 {
			t.Fatalf("Y[%d] = %d, want 16 (±1)", i, v)
		}
	}
	for i, v := range f.Cb[:width/2*height/2] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cb[%d] = %d, want 128 (±1)", i, v)
		}
	}
	for i, v := range f.Cr[:width/2*height/2] {
		if abs(int(v)-128) > 1 {
			t.Fatalf("Cr[%d] = %d, want 128 (±1)", i, v)
		}
	}
}

// TestEncodeIDRAndPSkipFromSPSPPS tests the full workflow: encode IDR + P-skip
// both using external SPS/PPS from an initial encoder stream.
func TestEncodeIDRAndPSkipFromSPSPPS(t *testing.T) {
	width, height := 32, 32
	qp := 26

	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	// Encode IDR frame to get parameter sets
	idrBytes, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	nalus := avc.ExtractNalusFromByteStream(idrBytes)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU []byte
	var spsNALU []byte
	var ppsNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])
		switch naluType {
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
	idrSliceBytes, err := EncodeIDRSliceFromSPSPPS(sps, pps, y, cb, cr)
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

	// P-frame should be pixel-exact copy of IDR
	idrFrame := frames[0]
	pFrame := frames[1]
	for i := range width * height {
		if idrFrame.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idrFrame.Y[i])
		}
	}
}

// TestEncodePSkipFromSPSPPS tests that EncodePSkipSliceFromSPSPPS generates a valid
// P-skip slice by parsing SPS/PPS from an encoder-generated stream, then using the
// new API to produce a compatible P-skip frame.
func TestEncodePSkipFromSPSPPS(t *testing.T) {
	width, height := 16, 16
	qp := 26

	y := makeUniform(width*height, 16)
	cb := makeUniform(width/2*height/2, 128)
	cr := makeUniform(width/2*height/2, 128)

	// Encode IDR frame (includes VPS+SPS+PPS+IDR)
	idrBytes, err := EncodeIDRFrame(EncodeParams{Width: width, Height: height, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	// Extract NALUs and parse SPS/PPS
	nalus := avc.ExtractNalusFromByteStream(idrBytes)
	spsMap := make(map[uint32]*hevc.SPS)
	var vpsNALU []byte
	var spsNALU []byte
	var ppsNALU []byte
	var idrNALU []byte
	var sps *hevc.SPS
	var pps *hevc.PPS

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluType := hevc.GetNaluType(nalu[0])
		switch naluType {
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

	// Build stream: reuse original VPS+SPS+PPS+IDR, append new P-skip
	dec := decoder.New()
	frames, err := dec.DecodeNALUs([][]byte{vpsNALU, spsNALU, ppsNALU, idrNALU})
	if err != nil {
		t.Fatalf("DecodeNALUs (IDR): %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 IDR frame, got %d", len(frames))
	}

	// Now decode the P-skip NALU
	pNalus := avc.ExtractNalusFromByteStream(pSkipBytes)
	pFrames, err := dec.DecodeNALUs(pNalus)
	if err != nil {
		t.Fatalf("DecodeNALUs (P-skip): %v", err)
	}
	if len(pFrames) != 1 {
		t.Fatalf("expected 1 P-skip frame, got %d", len(pFrames))
	}

	// P-frame should be pixel-exact copy of IDR
	idrFrame := frames[0]
	pFrame := pFrames[0]
	for i := range width * height {
		if idrFrame.Y[i] != pFrame.Y[i] {
			t.Fatalf("P-frame Y[%d] = %d, want %d (IDR)", i, pFrame.Y[i], idrFrame.Y[i])
		}
	}
	for i := range width / 2 * height / 2 {
		if idrFrame.Cb[i] != pFrame.Cb[i] {
			t.Fatalf("P-frame Cb[%d] = %d, want %d (IDR)", i, pFrame.Cb[i], idrFrame.Cb[i])
		}
		if idrFrame.Cr[i] != pFrame.Cr[i] {
			t.Fatalf("P-frame Cr[%d] = %d, want %d (IDR)", i, pFrame.Cr[i], idrFrame.Cr[i])
		}
	}
}

// TestEncodePSkipFromSPSPPS_x265 tests compatibility with an x265-generated bitstream.
// It parses SPS/PPS from an existing test bitstream and generates a P-skip slice.
func TestEncodePSkipFromSPSPPS_x265(t *testing.T) {
	// Use an x265-generated test bitstream
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
				naluType := hevc.GetNaluType(nalu[0])
				switch naluType {
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

			// Generate P-skip slice - should not error
			_, err = EncodePSkipSliceFromSPSPPS(sps, pps, 2)
			if err != nil {
				t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
			}

			// Concatenate original stream + new P-skip and decode all
			pSkipBytes, _ := EncodePSkipSliceFromSPSPPS(sps, pps, 2)
			stream := make([]byte, 0, len(data)+len(pSkipBytes))
			stream = append(stream, data...)
			stream = append(stream, pSkipBytes...)

			dec := decoder.New()
			frames, err := dec.DecodeAnnexB(stream)
			if err != nil {
				t.Fatalf("DecodeAnnexB: %v", err)
			}

			// Should have original frames + 1 P-skip frame
			if len(frames) < 2 {
				t.Fatalf("expected at least 2 frames, got %d", len(frames))
			}

			// Last frame (P-skip) should match previous frame (pixel-exact copy)
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
// (left half Y=235, right half Y=16). This exercises multi-CTU encoding with
// significant residual coefficients. Quantization at QP=26 introduces loss;
// the test validates that encode→decode round-trip produces reasonable results.
func TestEncodeTwoColor32x32(t *testing.T) {
	w, h := 32, 32
	qp := 26

	y := make([]uint8, w*h)
	cb := make([]uint8, w/2*h/2)
	cr := make([]uint8, w/2*h/2)
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			if px < 16 {
				y[py*w+px] = 235
			} else {
				y[py*w+px] = 16
			}
		}
	}
	for i := range cb {
		cb[i] = 128
		cr[i] = 128
	}

	annexB, err := EncodeIDRFrame(EncodeParams{Width: w, Height: h, QP: qp}, y, cb, cr)
	if err != nil {
		t.Fatalf("EncodeIDRFrame: %v", err)
	}

	d := decoder.New()
	frames, err := d.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]

	// With QP=26 and high-contrast content, quantization loss up to ~7 is expected.
	// The encoder's reconstruction may differ from the original by more than ±1.
	maxAllowed := 7
	for i := range w * h {
		expected := 235
		if i%w >= 16 {
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
