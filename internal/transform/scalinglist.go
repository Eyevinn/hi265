package transform

// Scaling lists are the quantization matrices of spec 7.4.5: a per-position
// weight m[x][y] that multiplies every coefficient during scaling (spec 8.6.3),
// so a stream can quantize high frequencies more coarsely than low ones.
//
// When scaling_list_enabled_flag is 0 the matrix is flat 16 everywhere, which is
// what a nil *ScalingLists means here. When the flag is 1 and no explicit
// scaling_list_data() is present, the default matrices of Tables 7-5 and 7-6
// apply — that is what DefaultScalingLists builds, and it is the case a real
// encoder produces when simply asked to switch scaling lists on.

// defaultIntra8x8 and defaultInter8x8 are Table 7-6, as the 8x8 matrices they
// derive: the table lists them in diagonal scan order, and these are the same
// values already placed at their (x, y). Both are symmetric, so they read the
// same by row or by column.
var defaultIntra8x8 = [64]int32{
	16, 16, 16, 16, 17, 18, 21, 24,
	16, 16, 16, 16, 17, 19, 22, 25,
	16, 16, 17, 18, 20, 22, 25, 29,
	16, 16, 18, 21, 24, 27, 31, 36,
	17, 17, 20, 24, 30, 35, 41, 47,
	18, 19, 22, 27, 35, 44, 54, 65,
	21, 22, 25, 31, 41, 54, 70, 88,
	24, 25, 29, 36, 47, 65, 88, 115,
}

var defaultInter8x8 = [64]int32{
	16, 16, 16, 16, 17, 18, 20, 24,
	16, 16, 16, 17, 18, 20, 24, 25,
	16, 16, 17, 18, 20, 24, 25, 28,
	16, 17, 18, 20, 24, 25, 28, 33,
	17, 18, 20, 24, 25, 28, 33, 41,
	18, 20, 24, 25, 28, 33, 41, 54,
	20, 24, 25, 28, 33, 41, 54, 71,
	24, 25, 28, 33, 41, 54, 71, 91,
}

// MatrixID is the index of Table 7-4: the prediction mode and colour component a
// scaling list applies to. Intra Y, Cb, Cr are 0, 1, 2 and the inter three are
// 3, 4, 5. Only 0 and 3 are signalled for 32x32 blocks, which is why chroma never
// reaches that size in 4:2:0.
func MatrixID(intra bool, cIdx int) int {
	if intra {
		return cIdx
	}
	return 3 + cIdx
}

// ScalingLists holds one matrix per (block size, matrix id), each in raster
// order. Index 0 is 4x4, 1 is 8x8, 2 is 16x16 and 3 is 32x32.
type ScalingLists struct {
	factors [4][6][]int32
}

// defaultScalingLists is built once: the matrices are constant and read-only, and
// rebuilding them per slice segment would allocate some 30 kB each time.
var defaultScalingLists = buildDefaultScalingLists()

// DefaultScalingLists returns the matrices spec 7.4.5 infers when
// scaling_list_enabled_flag is set and nothing overrides them. The result is
// shared, so callers must treat it and the matrices it returns as read-only.
//
// The 4x4 matrices are flat, which is why a picture coded entirely in 4x4
// transform blocks looks the same whether or not the lists are applied. The
// larger sizes take the 8x8 defaults and upsample: each entry covers a 2x2 block
// of a 16x16 matrix and a 4x4 block of a 32x32 one. The DC position of the two
// large sizes is then overridden with scaling_list_dc_coef, inferred as 16 here,
// which is what the 8x8 default already has there.
func DefaultScalingLists() *ScalingLists { return defaultScalingLists }

func buildDefaultScalingLists() *ScalingLists {
	s := &ScalingLists{}
	for matrixID := range 6 {
		base := &defaultIntra8x8
		if matrixID >= 3 {
			base = &defaultInter8x8
		}

		flat := make([]int32, 4*4)
		for i := range flat {
			flat[i] = 16
		}
		s.factors[0][matrixID] = flat

		eight := make([]int32, len(base))
		copy(eight, base[:])
		s.factors[1][matrixID] = eight
		s.factors[2][matrixID] = upsample(base, 2)
		s.factors[3][matrixID] = upsample(base, 4)
	}
	return s
}

// upsample spreads an 8x8 matrix over an 8n x 8n one, each entry covering an
// n x n block (spec 7.4.5).
func upsample(base *[64]int32, n int) []int32 {
	size := 8 * n
	out := make([]int32, size*size)
	for y := range size {
		for x := range size {
			out[y*size+x] = base[(y/n)*8+x/n]
		}
	}
	return out
}

// Matrix returns the m[x][y] of spec 8.6.3 for one transform block, in raster
// order, or nil for the flat matrix that needs no multiply. A nil receiver is a
// stream with scaling_list_enabled_flag clear.
//
// log2Size is the transform block's, so 2 through 5.
func (s *ScalingLists) Matrix(log2Size, matrixID int) []int32 {
	if s == nil {
		return nil
	}
	sizeID := log2Size - 2
	if sizeID < 0 || sizeID >= len(s.factors) || matrixID < 0 || matrixID >= 6 {
		return nil
	}
	return s.factors[sizeID][matrixID]
}
