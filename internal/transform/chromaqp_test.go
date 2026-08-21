package transform

import "testing"

// TestChromaQPFromLumaQP pins spec Table 8-10 for ChromaArrayType 1. The
// mapping used to be duplicated in the encoder and the decoder, and both copies
// carried the same off-by-one at qPi 34 (32 instead of 33), which produced a
// chroma-only mismatch against FFmpeg at exactly that QP.
func TestChromaQPFromLumaQP(t *testing.T) {
	// Table 8-10: identity below 30, the listed values for 30..43, qPi-6 above.
	want := map[int]int{
		0: 0, 17: 17, 29: 29,
		30: 29, 31: 30, 32: 31, 33: 32, 34: 33, 35: 33, 36: 34,
		37: 34, 38: 35, 39: 35, 40: 36, 41: 36, 42: 37, 43: 37,
		44: 38, 45: 39, 51: 45,
	}
	for qpY, expected := range want {
		if got := ChromaQPFromLumaQP(qpY); got != expected {
			t.Errorf("ChromaQPFromLumaQP(%d) = %d, want %d", qpY, got, expected)
		}
	}

	// The mapping must never decrease as the luma QP rises.
	prev := -1
	for qpY := 0; qpY <= 51; qpY++ {
		got := ChromaQPFromLumaQP(qpY)
		if got < prev {
			t.Errorf("not monotonic: ChromaQPFromLumaQP(%d) = %d after %d", qpY, got, prev)
		}
		prev = got
	}
}
