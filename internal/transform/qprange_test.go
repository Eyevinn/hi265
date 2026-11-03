package transform

import (
	"strings"
	"testing"
)

// The bounds at 8 bits are the ones the decoder and the encoder use; the 10-bit
// ones are where QpBdOffsetY = 12 moves the lower end, which the gray writer
// meets with a Main 10 SPS.

func TestCheckInitQP(t *testing.T) {
	for _, tc := range []struct {
		v, bitDepth int
		ok          bool
	}{
		{-26, 8, true}, {25, 8, true}, {-27, 8, false}, {26, 8, false},
		{-38, 10, true}, {-39, 10, false}, {25, 10, true}, {26, 10, false},
	} {
		err := CheckInitQP(tc.v, tc.bitDepth)
		if (err == nil) != tc.ok {
			t.Errorf("CheckInitQP(%d, %d) = %v, want ok=%v", tc.v, tc.bitDepth, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "init_qp_minus26") {
			t.Errorf("CheckInitQP(%d, %d): the error should name init_qp_minus26, got: %v",
				tc.v, tc.bitDepth, err)
		}
	}
}

func TestCheckSliceQP(t *testing.T) {
	for _, tc := range []struct {
		init, delta, bitDepth int
		ok                    bool
	}{
		{-26, 0, 8, true}, {25, 0, 8, true}, {0, -26, 8, true}, {0, 25, 8, true},
		{-27, 0, 8, false}, {0, 26, 8, false}, {0, -27, 8, false},
		// A legal PPS with a delta that leaves the range, and an illegal one the
		// delta brings back: only the sum is the slice QP.
		{25, 1, 8, false}, {-27, 1, 8, true},
		{0, -38, 10, true}, {0, -39, 10, false}, {0, 25, 10, true}, {0, 26, 10, false},
	} {
		err := CheckSliceQP(tc.init, tc.delta, tc.bitDepth)
		if (err == nil) != tc.ok {
			t.Errorf("CheckSliceQP(%d, %d, %d) = %v, want ok=%v",
				tc.init, tc.delta, tc.bitDepth, err, tc.ok)
		}
	}
}

func TestCheckChromaQPOffsets(t *testing.T) {
	for _, tc := range []struct {
		pps, slice ChromaQPOffsets
		wantName   string // empty when the offsets are legal
	}{
		{ChromaQPOffsets{}, ChromaQPOffsets{}, ""},
		{ChromaQPOffsets{Cb: 12, Cr: -12}, ChromaQPOffsets{}, ""},
		{ChromaQPOffsets{Cb: 12, Cr: -12}, ChromaQPOffsets{Cb: -12, Cr: 12}, ""},
		{ChromaQPOffsets{Cb: 13}, ChromaQPOffsets{}, "pps_cb_qp_offset"},
		{ChromaQPOffsets{Cr: -13}, ChromaQPOffsets{}, "pps_cr_qp_offset"},
		{ChromaQPOffsets{}, ChromaQPOffsets{Cb: -13}, "slice_cb_qp_offset"},
		{ChromaQPOffsets{}, ChromaQPOffsets{Cr: 13}, "slice_cr_qp_offset"},
		// Each legal on its own, but not together.
		{ChromaQPOffsets{Cb: 7}, ChromaQPOffsets{Cb: 6}, "pps_cb_qp_offset + slice_cb_qp_offset"},
		{ChromaQPOffsets{Cr: -7}, ChromaQPOffsets{Cr: -6}, "pps_cr_qp_offset + slice_cr_qp_offset"},
	} {
		err := CheckChromaQPOffsets(tc.pps, tc.slice)
		if tc.wantName == "" {
			if err != nil {
				t.Errorf("CheckChromaQPOffsets(%+v, %+v) = %v, want nil", tc.pps, tc.slice, err)
			}
			continue
		}
		if err == nil || !strings.HasPrefix(err.Error(), tc.wantName+" ") {
			t.Errorf("CheckChromaQPOffsets(%+v, %+v) = %v, want an error naming %s",
				tc.pps, tc.slice, err, tc.wantName)
		}
	}
}

func TestCheckCuQpDelta(t *testing.T) {
	for _, tc := range []struct {
		delta, bitDepth int
		ok              bool
	}{
		{-26, 8, true}, {25, 8, true}, {-27, 8, false}, {26, 8, false},
		{-32, 10, true}, {31, 10, true}, {-33, 10, false}, {32, 10, false},
	} {
		if err := CheckCuQpDelta(tc.delta, tc.bitDepth); (err == nil) != tc.ok {
			t.Errorf("CheckCuQpDelta(%d, %d) = %v, want ok=%v", tc.delta, tc.bitDepth, err, tc.ok)
		}
	}
}
