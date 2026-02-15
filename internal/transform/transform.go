package transform

// InverseDCT performs the HEVC inverse DCT on a block of coefficients.
// size is the block dimension (4, 8, 16, or 32).
// Coefficients are in raster scan order.
func InverseDCT(coeffs []int32, size int) []int32 {
	switch size {
	case 4:
		return inverseDCT4(coeffs)
	case 8:
		return inverseDCT8(coeffs)
	case 16:
		return inverseDCT16(coeffs)
	default:
		return coeffs
	}
}

// InverseDST performs the 4x4 inverse DST (for intra luma 4x4 TU only).
func InverseDST(coeffs []int32) []int32 {
	return inverseDST4(coeffs)
}

// HEVC 4x4 DCT matrix (spec Table 8-3) — transposed for inverse
var dct4 = [4][4]int32{
	{64, 64, 64, 64},
	{83, 36, -36, -83},
	{64, -64, -64, 64},
	{36, -83, 83, -36},
}

// HEVC 4x4 DST matrix (spec Table 8-5)
var dst4 = [4][4]int32{
	{29, 55, 74, 84},
	{74, 74, 0, -74},
	{84, -29, -74, 55},
	{55, -84, 74, -29},
}

// HEVC 8x8 DCT matrix (spec Table 8-3)
var dct8 = [8][8]int32{
	{64, 64, 64, 64, 64, 64, 64, 64},
	{89, 75, 50, 18, -18, -50, -75, -89},
	{83, 36, -36, -83, -83, -36, 36, 83},
	{75, -18, -89, -50, 50, 89, 18, -75},
	{64, -64, -64, 64, 64, -64, -64, 64},
	{50, -89, 18, 75, -75, -18, 89, -50},
	{36, -83, 83, -36, -36, 83, -83, 36},
	{18, -50, 75, -89, 89, -75, 50, -18},
}

// HEVC 16x16 DCT matrix (spec Table 8-4)
var dct16 = [16][16]int32{
	{64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64},
	{90, 87, 80, 70, 57, 43, 25, 9, -9, -25, -43, -57, -70, -80, -87, -90},
	{89, 75, 50, 18, -18, -50, -75, -89, -89, -75, -50, -18, 18, 50, 75, 89},
	{87, 57, 9, -43, -80, -90, -70, -25, 25, 70, 90, 80, 43, -9, -57, -87},
	{83, 36, -36, -83, -83, -36, 36, 83, 83, 36, -36, -83, -83, -36, 36, 83},
	{80, 9, -70, -87, -25, 57, 90, 43, -43, -90, -57, 25, 87, 70, -9, -80},
	{75, -18, -89, -50, 50, 89, 18, -75, -75, 18, 89, 50, -50, -89, -18, 75},
	{70, -43, -87, 9, 90, 25, -80, -57, 57, 80, -25, -90, -9, 87, 43, -70},
	{64, -64, -64, 64, 64, -64, -64, 64, 64, -64, -64, 64, 64, -64, -64, 64},
	{57, -80, -25, 90, -9, -87, 43, 70, -70, -43, 87, 9, -90, 25, 80, -57},
	{50, -89, 18, 75, -75, -18, 89, -50, -50, 89, -18, -75, 75, 18, -89, 50},
	{43, -90, 57, 25, -87, 70, 9, -80, 80, -9, -70, 87, -25, -57, 90, -43},
	{36, -83, 83, -36, -36, 83, -83, 36, 36, -83, 83, -36, -36, 83, -83, 36},
	{25, -70, 90, -80, 43, 9, -57, 87, -87, 57, -9, -43, 80, -90, 70, -25},
	{18, -50, 75, -89, 89, -75, 50, -18, -18, 50, -75, 89, -89, 75, -50, 18},
	{9, -25, 43, -57, 70, -80, 87, -90, 90, -87, 80, -70, 57, -43, 25, -9},
}

func inverseDCT4(coeffs []int32) []int32 {
	out := make([]int32, 16)
	tmp := make([]int32, 16)

	// 1D inverse DCT on rows
	for i := range 4 {
		for j := range 4 {
			var sum int32
			for k := range 4 {
				sum += dct4[k][j] * coeffs[i*4+k]
			}
			tmp[i*4+j] = (sum + 64) >> 7
		}
	}

	// 1D inverse DCT on columns
	for j := range 4 {
		for i := range 4 {
			var sum int32
			for k := range 4 {
				sum += dct4[k][i] * tmp[k*4+j]
			}
			out[i*4+j] = (sum + 2048) >> 12
		}
	}
	return out
}

func inverseDST4(coeffs []int32) []int32 {
	out := make([]int32, 16)
	tmp := make([]int32, 16)

	// 1D inverse DST on rows
	for i := range 4 {
		for j := range 4 {
			var sum int32
			for k := range 4 {
				sum += dst4[k][j] * coeffs[i*4+k]
			}
			tmp[i*4+j] = (sum + 64) >> 7
		}
	}

	// 1D inverse DST on columns
	for j := range 4 {
		for i := range 4 {
			var sum int32
			for k := range 4 {
				sum += dst4[k][i] * tmp[k*4+j]
			}
			out[i*4+j] = (sum + 2048) >> 12
		}
	}
	return out
}

func inverseDCT8(coeffs []int32) []int32 {
	out := make([]int32, 64)
	tmp := make([]int32, 64)

	for i := range 8 {
		for j := range 8 {
			var sum int32
			for k := range 8 {
				sum += dct8[k][j] * coeffs[i*8+k]
			}
			tmp[i*8+j] = (sum + 64) >> 7
		}
	}

	for j := range 8 {
		for i := range 8 {
			var sum int32
			for k := range 8 {
				sum += dct8[k][i] * tmp[k*8+j]
			}
			out[i*8+j] = (sum + 2048) >> 12
		}
	}
	return out
}

func inverseDCT16(coeffs []int32) []int32 {
	n := 16
	out := make([]int32, n*n)
	tmp := make([]int32, n*n)

	for i := range n {
		for j := range n {
			var sum int32
			for k := range n {
				sum += dct16[k][j] * coeffs[i*n+k]
			}
			tmp[i*n+j] = (sum + 64) >> 7
		}
	}

	for j := range n {
		for i := range n {
			var sum int32
			for k := range n {
				sum += dct16[k][i] * tmp[k*n+j]
			}
			out[i*n+j] = (sum + 2048) >> 12
		}
	}
	return out
}
