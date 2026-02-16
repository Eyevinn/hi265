package encode

import (
	"testing"

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
