package transform

import "testing"

// The default matrices of spec Table 7-6, checked at the corners and at the
// upsampling boundaries rather than reproduced wholesale — copying the table into
// the test twice would only pin the copy.
func TestDefaultScalingLists(t *testing.T) {
	s := DefaultScalingLists()

	// 4x4 is flat, which is why a picture coded entirely in 4x4 transform blocks
	// decodes the same whether or not the lists are applied. That is exactly what
	// hid this: the first stream tried used only small blocks.
	for _, matrixID := range []int{0, 3} {
		m := s.Matrix(2, matrixID)
		if len(m) != 16 {
			t.Fatalf("4x4 matrix %d has %d entries, want 16", matrixID, len(m))
		}
		for i, v := range m {
			if v != 16 {
				t.Fatalf("4x4 matrix %d position %d is %d, want the flat 16", matrixID, i, v)
			}
		}
	}

	// 8x8 intra: the corners of Table 7-6, and its symmetry.
	intra := s.Matrix(3, MatrixID(true, 0))
	if len(intra) != 64 {
		t.Fatalf("8x8 matrix has %d entries, want 64", len(intra))
	}
	for _, tc := range []struct{ x, y, want int }{
		{0, 0, 16}, {7, 0, 24}, {0, 7, 24}, {7, 7, 115}, {4, 4, 30},
	} {
		if got := int(intra[tc.y*8+tc.x]); got != tc.want {
			t.Errorf("intra 8x8 (%d,%d) = %d, want %d", tc.x, tc.y, got, tc.want)
		}
	}
	for y := range 8 {
		for x := range 8 {
			if intra[y*8+x] != intra[x*8+y] {
				t.Fatalf("the intra 8x8 matrix is not symmetric at (%d,%d)", x, y)
			}
		}
	}

	// Inter differs from intra, or one of the two tables is a copy of the other.
	inter := s.Matrix(3, MatrixID(false, 0))
	same := true
	for i := range intra {
		if intra[i] != inter[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("the intra and inter 8x8 default matrices are identical")
	}

	// 16x16 and 32x32 upsample the 8x8 matrix, each entry covering an n x n block.
	for _, tc := range []struct{ log2Size, n int }{{4, 2}, {5, 4}} {
		size := 1 << tc.log2Size
		m := s.Matrix(tc.log2Size, MatrixID(true, 0))
		if len(m) != size*size {
			t.Fatalf("%dx%d matrix has %d entries, want %d", size, size, len(m), size*size)
		}
		for y := range size {
			for x := range size {
				want := intra[(y/tc.n)*8+x/tc.n]
				if got := m[y*size+x]; got != want {
					t.Fatalf("%dx%d (%d,%d) = %d, want the 8x8 entry %d",
						size, size, x, y, got, want)
				}
			}
		}
	}
}

// A nil *ScalingLists is a stream with scaling_list_enabled_flag clear, and every
// lookup on it has to say "flat" rather than panic.
func TestNilScalingListsAreFlat(t *testing.T) {
	var s *ScalingLists
	for log2Size := 2; log2Size <= 5; log2Size++ {
		if m := s.Matrix(log2Size, 0); m != nil {
			t.Errorf("Matrix(%d, 0) on a nil ScalingLists = %v, want nil", log2Size, m)
		}
	}
}

// MatrixID is Table 7-4. Only entries 0 and 3 are signalled at 32x32, which is
// why chroma never reaches that size in 4:2:0.
func TestMatrixID(t *testing.T) {
	for _, tc := range []struct {
		intra bool
		cIdx  int
		want  int
	}{
		{true, 0, 0}, {true, 1, 1}, {true, 2, 2},
		{false, 0, 3}, {false, 1, 4}, {false, 2, 5},
	} {
		if got := MatrixID(tc.intra, tc.cIdx); got != tc.want {
			t.Errorf("MatrixID(%v, %d) = %d, want %d", tc.intra, tc.cIdx, got, tc.want)
		}
	}
}

// Dequantize must weight by the matrix, and treat nil as the flat 16 the scaling
// process uses when no lists are enabled.
func TestDequantizeUsesTheMatrix(t *testing.T) {
	coeffs := []int32{4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	flat := Dequantize(coeffs, 4, 26, nil)

	sixteens := make([]int32, 16)
	for i := range sixteens {
		sixteens[i] = 16
	}
	if explicit := Dequantize(coeffs, 4, 26, sixteens); explicit[0] != flat[0] {
		t.Errorf("an all-16 matrix gave %d, the flat path gave %d", explicit[0], flat[0])
	}

	doubled := make([]int32, 16)
	for i := range doubled {
		doubled[i] = 32
	}
	if got := Dequantize(coeffs, 4, 26, doubled); got[0] != 2*flat[0] {
		t.Errorf("doubling every weight gave %d, want twice the flat %d", got[0], flat[0])
	}
}
