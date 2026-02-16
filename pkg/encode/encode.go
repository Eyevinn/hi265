package encode

import "bytes"

// EncodeParams holds parameters for HEVC encoding.
type EncodeParams struct {
	Width  int // must be multiple of 16
	Height int // must be multiple of 16
	QP     int // 0-51, default 26
}

// EncodeIDRFrame returns Annex-B bytes: VPS + SPS + PPS + IDR slice.
// y, cb, cr are raw pixel planes in raster scan order (4:2:0).
func EncodeIDRFrame(p EncodeParams, y, cb, cr []uint8) ([]byte, error) {
	qp := p.QP
	if qp == 0 {
		qp = 26
	}

	var buf bytes.Buffer

	// Write VPS
	writeNALU(&buf, naluVPS, generateVPS())

	// Write SPS
	writeNALU(&buf, naluSPS, generateSPS(p.Width, p.Height))

	// Write PPS
	writeNALU(&buf, naluPPS, generatePPS(qp))

	// Write IDR slice
	writeNALU(&buf, naluIDRWRadl, encodeIDRSlice(p.Width, p.Height, qp, y, cb, cr))

	return buf.Bytes(), nil
}

// EncodePSkipFrame returns Annex-B bytes: P-slice (all skip CUs).
// Must be called after EncodeIDRFrame to produce a valid stream.
func EncodePSkipFrame(p EncodeParams, poc int) ([]byte, error) {
	qp := p.QP
	if qp == 0 {
		qp = 26
	}

	var buf bytes.Buffer

	// Write P-slice
	writeNALU(&buf, naluTrailR, encodePSkipSlice(p.Width, p.Height, qp, poc))

	return buf.Bytes(), nil
}
