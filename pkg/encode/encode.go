package encode

import (
	"bytes"

	"github.com/Eyevinn/hi264/pkg/yuv"
)

// EncodeParams holds parameters for HEVC encoding.
type EncodeParams struct {
	Width      int            // must be multiple of 16
	Height     int            // must be multiple of 16
	QP         int            // 0-51, default 26
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
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
	WriteNALU(&buf, naluVPS, generateVPS())

	// Write SPS
	WriteNALU(&buf, naluSPS, generateSPS(p.Width, p.Height, p.ColorSpace, p.Range))

	// Write PPS
	WriteNALU(&buf, naluPPS, generatePPS(qp))

	// Write IDR slice
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(p.Width, p.Height, qp, y, cb, cr))

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
	WriteNALU(&buf, naluTrailR, encodePSkipSlice(p.Width, p.Height, qp, poc))

	return buf.Bytes(), nil
}

// FrameEncoder provides grid-based HEVC encoding using hi264's yuv package.
type FrameEncoder struct {
	Grid       *yuv.Grid
	Colors     yuv.ColorMap
	QP         int
	Width      int            // pixel width (0 = Grid.Width*16)
	Height     int            // pixel height (0 = Grid.Height*16)
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

// Encode produces a complete Annex-B IDR frame: VPS + SPS + PPS + IDR slice.
func (e *FrameEncoder) Encode() ([]byte, error) {
	f, err := yuv.BuildFrame(e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	w := e.frameWidth()
	h := e.frameHeight()
	f.Width = w
	f.Height = h
	return EncodeIDRFrame(EncodeParams{
		Width:      w,
		Height:     h,
		QP:         e.qp(),
		ColorSpace: e.ColorSpace,
		Range:      e.Range,
	}, f.Y, f.Cb, f.Cr)
}

// EncodeVPSSPSPPS writes the VPS, SPS, and PPS NALUs to buf.
func (e *FrameEncoder) EncodeVPSSPSPPS(buf *bytes.Buffer) {
	WriteNALU(buf, naluVPS, generateVPS())
	WriteNALU(buf, naluSPS, generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range))
	WriteNALU(buf, naluPPS, generatePPS(e.qp()))
}

// EncodeIDRSlice produces an Annex-B IDR slice (no VPS/SPS/PPS).
func (e *FrameEncoder) EncodeIDRSlice() ([]byte, error) {
	f, err := yuv.BuildFrame(e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	w := e.frameWidth()
	h := e.frameHeight()
	f.Width = w
	f.Height = h

	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(w, h, e.qp(), f.Y, f.Cb, f.Cr))
	return buf.Bytes(), nil
}

// EncodePSkipSlice produces an Annex-B P-skip slice.
func (e *FrameEncoder) EncodePSkipSlice(poc int) ([]byte, error) {
	var buf bytes.Buffer
	WriteNALU(&buf, naluTrailR, encodePSkipSlice(e.frameWidth(), e.frameHeight(), e.qp(), poc))
	return buf.Bytes(), nil
}

// VPSNALUs returns the raw VPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) VPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluVPS, generateVPS())}
}

// SPSNALUs returns the raw SPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) SPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluSPS, generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range))}
}

// PPSNALUs returns the raw PPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) PPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluPPS, generatePPS(e.qp()))}
}

func (e *FrameEncoder) frameWidth() int {
	if e.Width > 0 {
		return e.Width
	}
	if e.Grid != nil {
		return e.Grid.Width * 16
	}
	return 16
}

func (e *FrameEncoder) frameHeight() int {
	if e.Height > 0 {
		return e.Height
	}
	if e.Grid != nil {
		return e.Grid.Height * 16
	}
	return 16
}

func (e *FrameEncoder) qp() int {
	if e.QP == 0 {
		return 26
	}
	return e.QP
}
