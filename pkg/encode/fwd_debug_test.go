package encode

import (
	"fmt"
	"os"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/decoder"
	"github.com/Eyevinn/hi265/pkg/frame"
)

func TestForwardInverseRoundTrip(t *testing.T) {
	// Test that forward DCT + quantize + dequantize + inverse DCT is near-lossless
	size := 16
	qp := 26
	residual := make([]int32, size*size)
	for i := range residual {
		residual[i] = -112
	}

	// Forward
	coeffs := forwardDCT(residual, size)
	levels := quantize(coeffs, size, qp)

	// Inverse (using decoder's functions)
	dequant := transform.Dequantize(levels, size, qp, nil)
	reconstructed := transform.InverseDCT(dequant, size)

	// Check that all pixels are close
	maxDiff := int32(0)
	for i := range residual {
		diff := residual[i] - reconstructed[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > maxDiff {
			maxDiff = diff
		}
	}
	if maxDiff > 1 {
		t.Errorf("Forward/inverse DCT round-trip max diff = %d, want ≤ 1", maxDiff)
	}
}

func TestCoeffAbsLevelRemainingDirect(t *testing.T) {
	// Test encodeCoeffAbsLevelRemaining round-trip with decodeAbsLevelRemaining
	for _, tc := range []struct {
		level      int
		cRiceParam int
	}{
		{0, 0}, {1, 0}, {2, 0}, {3, 0}, {5, 0}, {10, 0}, {137, 0},
		{0, 1}, {5, 1}, {137, 1},
	} {
		enc := cabac.NewEncoder()
		models := context.InitModels(context.SliceTypeI, 26)
		enc.EncodeDecision(0, &models[0]) // anchor context bin

		encodeCoeffAbsLevelRemaining(enc, tc.level, tc.cRiceParam)
		enc.EncodeTerminate(1)
		data := enc.Flush()

		// Decode
		models2 := context.InitModels(context.SliceTypeI, 26)
		dec, err := cabac.NewDecoder(data)
		if err != nil {
			t.Fatalf("NewDecoder: %v", err)
		}
		dec.DecodeDecision(&models2[0]) // anchor context bin

		// Read TR+EGk prefix (unary count of 1-bits)
		prefix := 0
		for prefix < 20 {
			b := dec.DecodeBypass()
			if b == 0 {
				break
			}
			prefix++
		}

		var decoded int32
		if prefix < 3 {
			suffix := int32(0)
			for k := tc.cRiceParam - 1; k >= 0; k-- {
				suffix |= int32(dec.DecodeBypass()) << k
			}
			decoded = int32(prefix)<<tc.cRiceParam + suffix
		} else {
			suffixLen := prefix - 3 + tc.cRiceParam
			suffix := int32(0)
			for k := suffixLen - 1; k >= 0; k-- {
				suffix |= int32(dec.DecodeBypass()) << k
			}
			decoded = (((int32(1) << (prefix - 3)) + 3 - 1) << tc.cRiceParam) + suffix
		}

		if int(decoded) != tc.level {
			t.Errorf("level=%d cRiceParam=%d: decoded %d (prefix=%d)",
				tc.level, tc.cRiceParam, decoded, prefix)
		}
	}
}

func TestReconstructionDebug(t *testing.T) {
	width, height := 16, 16
	qp := 26

	reconFrame := frame.NewFrame(width, height)
	lumaSrc := makeUniform(width*height, 16)
	prediction, levels, cbfLuma := computeLumaResidual(0, 0, 16, width, lumaSrc, qp, 1, reconFrame)

	if !cbfLuma {
		t.Fatal("expected cbfLuma=true")
	}
	if levels[0] != -140 {
		t.Errorf("DC level = %d, want -140", levels[0])
	}

	// Verify reconstruction
	dequant := transform.Dequantize(levels, 16, qp, nil)
	invDCT := transform.InverseDCT(dequant, 16)
	recon := prediction[0] + invDCT[0]
	if recon != 16 {
		t.Errorf("Recon[0] = %d, want 16", recon)
	}
}

func TestEncodeWriteFile(t *testing.T) {
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

	// Write to file for manual FFmpeg testing
	_ = os.WriteFile("/tmp/test_encode.265", annexB, 0644)
	fmt.Printf("Wrote %d bytes to /tmp/test_encode.265\n", len(annexB))

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
	if f.Y[0] != 16 || f.Cb[0] != 128 || f.Cr[0] != 128 {
		t.Errorf("Y[0]=%d Cb[0]=%d Cr[0]=%d, want 16/128/128", f.Y[0], f.Cb[0], f.Cr[0])
	}
}
