// Package transform implements HEVC inverse quantization and inverse transform.
package transform

// levelScale is the HEVC dequantization scale factor table (spec Table 8-10).
// Indexed by QP % 6.
var levelScale = [6]int{40, 45, 51, 57, 64, 72}

// TransformSkipShift applies the additional dequantization shift needed for transform skip mode.
// After normal Dequantize(), transform skip coefficients need this fixed bit-depth shift
// instead of the inverse DCT/DST. Per HEVC spec / FFmpeg hevcdsp dequant():
// shift = 15 - bitDepth - log2TrafoSize
func TransformSkipShift(coeffs []int32, log2TrafoSize, bitDepth int) []int32 {
	out := make([]int32, len(coeffs))
	shift := 15 - bitDepth - log2TrafoSize
	if shift > 0 {
		offset := int32(1) << uint(shift-1)
		for i, c := range coeffs {
			out[i] = (c + offset) >> uint(shift)
		}
	} else {
		for i, c := range coeffs {
			out[i] = c << uint(-shift)
		}
	}
	return out
}

// Dequantize performs HEVC inverse quantization on a block of transform coefficients.
// size is the block dimension (4, 8, 16, 32).
// qp is the quantization parameter.
// Returns dequantized coefficients in raster scan order.
//
// HEVC spec 8.6.3 with flat scaling matrix (m=16, no custom scaling list):
//
//	bdShift = bitDepth + log2TrafoSize - 5
//	d[x][y] = Clip3(coeffMin, coeffMax,
//	    (TransCoeffLevel * m * levelScale[qp%6] << (qp/6) + (1 << (bdShift-1))) >> bdShift)
func Dequantize(coeffs []int32, size, qp int) []int32 {
	out := make([]int32, len(coeffs))

	log2Size := 0
	for (1 << log2Size) < size {
		log2Size++
	}

	bitDepth := 8
	bdShift := bitDepth + log2Size - 5
	scale := levelScale[qp%6]
	qpPer := qp / 6 // qp/6, the per-6 QP component

	// m = 16 for flat scaling matrix
	// Full multiply: coeff * 16 * levelScale[qp%6] * (1 << qpPer)
	// Then add rounding offset and right-shift by bdShift.

	for i, c := range coeffs {
		if c == 0 {
			continue
		}
		val := int64(c) * 16 * int64(scale) * (int64(1) << uint(qpPer))
		add := int64(1) << uint(bdShift-1)
		val = (val + add) >> uint(bdShift)
		// Clip to 16-bit range
		if val < -32768 {
			val = -32768
		} else if val > 32767 {
			val = 32767
		}
		out[i] = int32(val)
	}
	return out
}
