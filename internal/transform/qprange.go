package transform

import "fmt"

// The quantization parameters a bitstream carries are bounded by the standard,
// and nothing between the bitstream and the tables indexed by QP bounds them
// otherwise: mp4ff stores the PPS fields as read. Past the bounds a decode either
// panics, since a negative QP that is not a multiple of 6 indexes LevelScale at a
// negative position, or returns a picture without any error that no conforming
// decoder would produce. FFmpeg refuses these streams too.

// maxLumaQP is the highest luma QP at every bit depth.
const maxLumaQP = 51

// maxChromaQPOffset bounds every chroma QP offset, and the sum of a picture-level
// and a slice-level one, to ±12 (spec 7.4.3.3.1 and 7.4.7.1).
const maxChromaQPOffset = 12

// minLumaQP is the lowest luma QP at a bit depth, −QpBdOffsetY, with QpBdOffsetY
// being 6 * (bitDepth − 8) (spec equation 7-5).
func minLumaQP(bitDepth int) int {
	return -6 * (bitDepth - 8)
}

// CheckInitQP refuses an init_qp_minus26 outside −(26 + QpBdOffsetY)..25 (spec
// 7.4.3.3.1), which is the same as asking that the initial slice QP it sets,
// 26 + init_qp_minus26, lie in −QpBdOffsetY..51.
func CheckInitQP(initQPMinus26, bitDepth int) error {
	lo, hi := minLumaQP(bitDepth)-26, maxLumaQP-26
	if initQPMinus26 < lo || initQPMinus26 > hi {
		return fmt.Errorf("init_qp_minus26 %d is outside %d..%d", initQPMinus26, lo, hi)
	}
	return nil
}

// CheckSliceQP refuses a SliceQpY outside −QpBdOffsetY..51 (spec 7.4.7.1). The
// slice QP is 26 + init_qp_minus26 + slice_qp_delta, and the error names all
// three, since the fault can be in the PPS as easily as in the slice header.
func CheckSliceQP(initQPMinus26, sliceQPDelta, bitDepth int) error {
	qp := 26 + initQPMinus26 + sliceQPDelta
	if lo := minLumaQP(bitDepth); qp < lo || qp > maxLumaQP {
		return fmt.Errorf(
			"SliceQpY %d (26 + init_qp_minus26 %d + slice_qp_delta %d) is outside %d..%d",
			qp, initQPMinus26, sliceQPDelta, lo, maxLumaQP)
	}
	return nil
}

// CheckChromaQPOffsets refuses chroma QP offsets outside ±12: the picture-level
// pair, the slice-level pair and the sum of the two for each component are each
// bounded that way (spec 7.4.3.3.1 and 7.4.7.1). Pass a zero slice pair for a
// PPS on its own, or for a slice header that carries no offsets.
func CheckChromaQPOffsets(pps, slice ChromaQPOffsets) error {
	for _, o := range []struct {
		name string
		v    int
	}{
		{"pps_cb_qp_offset", pps.Cb},
		{"pps_cr_qp_offset", pps.Cr},
		{"slice_cb_qp_offset", slice.Cb},
		{"slice_cr_qp_offset", slice.Cr},
		{"pps_cb_qp_offset + slice_cb_qp_offset", pps.Cb + slice.Cb},
		{"pps_cr_qp_offset + slice_cr_qp_offset", pps.Cr + slice.Cr},
	} {
		if o.v < -maxChromaQPOffset || o.v > maxChromaQPOffset {
			return fmt.Errorf("%s %d is outside %d..%d",
				o.name, o.v, -maxChromaQPOffset, maxChromaQPOffset)
		}
	}
	return nil
}

// CheckCuQpDelta refuses a CuQpDeltaVal outside −(26 + QpBdOffsetY/2)..
// +(25 + QpBdOffsetY/2) (spec 7.4.9.14). Within it, the wrap that derives QpY
// from the predicted QP and the delta never sees a negative operand.
func CheckCuQpDelta(delta, bitDepth int) error {
	half := -minLumaQP(bitDepth) / 2
	if lo, hi := -(26 + half), 25+half; delta < lo || delta > hi {
		return fmt.Errorf("cu_qp_delta %d is outside %d..%d", delta, lo, hi)
	}
	return nil
}
