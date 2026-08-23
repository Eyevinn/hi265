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

// TestChromaQP pins spec 8.6.1 around Table 8-10: the offset is added to the luma
// QP, the sum is clipped to the range the table is indexed over, and only then is
// it mapped. Both ends of the clip matter, since pps_cb_qp_offset and its slice
// counterpart together reach ±24.
func TestChromaQP(t *testing.T) {
	for _, tc := range []struct {
		qpY, offset, want int
		why               string
	}{
		{26, 0, 26, "no offset is the plain table"},
		{26, 6, 31, "the offset lands the index at 32, which the table maps to 31"},
		{26, -6, 20, "a negative offset lowers the index"},
		{40, 6, 40, "the index 46 is past the table, so qPi-6"},
		{34, 0, 33, "the entry that used to be off by one"},
		{34, 1, 33, "35 maps to 33 as well"},
		// The clip. Without it these index outside the table, and the low end goes
		// negative, which the dequantizer cannot use.
		{2, -12, 0, "clipped at the bottom to -QpBdOffsetC, zero for 8-bit"},
		{0, -12, 0, "still zero"},
		{51, 12, 51, "clipped at the top to 57, which maps to 51"},
		{45, 12, 51, "57 is the largest index, giving 51"},
	} {
		if got := ChromaQP(tc.qpY, tc.offset); got != tc.want {
			t.Errorf("ChromaQP(%d, %+d) = %d, want %d (%s)",
				tc.qpY, tc.offset, got, tc.want, tc.why)
		}
	}

	// The result must stay in the range a quantizer can use, whatever the offset.
	for qpY := 0; qpY <= 51; qpY++ {
		for offset := -24; offset <= 24; offset++ {
			if got := ChromaQP(qpY, offset); got < 0 || got > 51 {
				t.Fatalf("ChromaQP(%d, %+d) = %d, outside 0..51", qpY, offset, got)
			}
		}
	}
}

// TestChromaQPOffsetsFor pins which component gets which offset. Getting this
// backwards is invisible whenever the two happen to be equal, which is most
// streams.
func TestChromaQPOffsetsFor(t *testing.T) {
	o := ChromaQPOffsets{Cb: 6, Cr: -6}
	if got := o.For(0); got != 6 {
		t.Errorf("For(0) = %d, want the Cb offset 6", got)
	}
	if got := o.For(1); got != -6 {
		t.Errorf("For(1) = %d, want the Cr offset -6", got)
	}
}
