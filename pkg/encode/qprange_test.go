package encode

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// The quantization parameters a bitstream carries are bounded by the standard,
// and hi265 used to trust them. A slice QP below zero that is not a multiple of 6
// panicked the dequantizer with "index out of range [-1]", and every other value
// outside 0..51 decoded without error into a picture FFmpeg refuses to produce.
// The encoder wrote such a PPS on request: EncodeParams{QP: -1} gave
// init_qp_minus26 = −27.

const qpRangeSize = 32

// qpRangePPS returns hi265's own PPS RBSP with init_qp_minus26 and the two
// picture-level chroma QP offsets replaced. generatePPS cannot write these values
// itself: its QP is validated, and its offsets are always zero.
func qpRangePPS(initQPMinus26, cbOffset, crOffset int) []byte {
	src := generatePPS(26, 1, 1, false, -1)
	r := NewBitReader(src)
	w := NewBitWriter()
	w.WriteUE(uint32(r.ReadUE()))         // pps_pic_parameter_set_id
	w.WriteUE(uint32(r.ReadUE()))         // pps_seq_parameter_set_id
	w.WriteBits(uint32(r.ReadBits(7)), 7) // dependent_slice_segments_enabled_flag .. cabac_init_present_flag
	w.WriteUE(uint32(r.ReadUE()))         // num_ref_idx_l0_default_active_minus1
	w.WriteUE(uint32(r.ReadUE()))         // num_ref_idx_l1_default_active_minus1
	r.ReadSE()
	w.WriteSE(int32(initQPMinus26))
	// constrained_intra_pred_flag, transform_skip_enabled_flag and
	// cu_qp_delta_enabled_flag, which is clear, so no diff_cu_qp_delta_depth.
	w.WriteBits(uint32(r.ReadBits(3)), 3)
	r.ReadSE()
	r.ReadSE()
	w.WriteSE(int32(cbOffset))
	w.WriteSE(int32(crOffset))

	// Everything else unchanged, up to rbsp_stop_one_bit, which is the last bit set.
	stop := len(src)*8 - 1
	for (src[stop/8]>>(7-stop%8))&1 == 0 {
		stop--
	}
	for r.Pos() < stop {
		w.WriteBit(uint8(r.ReadBit()))
	}
	w.WriteBit(1)
	w.AlignToByte()
	return w.Bytes()
}

// qpRangeParamSets returns the VPS, SPS and given PPS RBSP as Annex-B, with the
// SPS and PPS parsed back.
func qpRangeParamSets(t *testing.T, ppsRBSP []byte) (annexB []byte, sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()
	var buf bytes.Buffer
	WriteNALU(&buf, naluVPS, generateVPS())
	WriteNALU(&buf, naluSPS, generateSPS(qpRangeSize, qpRangeSize, yuv.BT601, yuv.LimitedRange, false))
	WriteNALU(&buf, naluPPS, ppsRBSP)
	_, _, _, sps, pps = parseParamSets(t, buf.Bytes())
	return buf.Bytes(), sps, pps
}

// qpRangeIDR returns an IDR slice coded at QP 26, whose slice_qp_delta of 0 makes
// its SliceQpY whatever the PPS it is decoded against says.
func qpRangeIDR(t *testing.T) []byte {
	t.Helper()
	grid, colors := yuv.SolidGrid(qpRangeSize, qpRangeSize, yuv.Color{Y: 200, Cb: 100, Cr: 160})
	idr, err := GenerateIDR(EncodeParams{Width: qpRangeSize, Height: qpRangeSize, QP: 26}, grid, colors)
	if err != nil {
		t.Fatal(err)
	}
	return idr
}

// decodeNoPanic decodes a stream, turning a panic into a test failure that names
// the stream rather than aborting the whole package.
func decodeNoPanic(t *testing.T, stream []byte) (n int, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("decode panicked: %v", r)
		}
	}()
	frames, err := decoder.New().DecodeAnnexB(stream)
	return len(frames), err
}

// TestDecoderRefusesSliceQPOutOfRange pins the decoder's refusal of a SliceQpY
// outside 0..51, on both slice header paths. The PPS is the faulty part here, but
// the check is on the slice QP the decode would use, so it names both terms.
//
// Measured before the check, on the IDR path: −1 panicked in transform.Dequantize,
// while −6 and 52 decoded without error into flat luma 128, for a source of 200.
// The P-skip path was refused, but only by accident and with a misleading message:
// CABAC initialised at the wrong QP misread the skip CUs as motion-compensated, and
// the refusal named motion compensation.
func TestDecoderRefusesSliceQPOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initQPMinus26 int
	}{
		{"qp-1", -27},
		{"qp-6", -32},
		{"qp52", 26},
	} {
		t.Run(tc.name+"/idr", func(t *testing.T) {
			prefix, _, _ := qpRangeParamSets(t, qpRangePPS(tc.initQPMinus26, 0, 0))
			n, err := decodeNoPanic(t, append(prefix, qpRangeIDR(t)...))
			if err == nil {
				t.Fatalf("expected the slice to be refused, got %d frames", n)
			}
			if !strings.Contains(err.Error(), "SliceQpY") ||
				!strings.Contains(err.Error(), "init_qp_minus26") {
				t.Errorf("the error should name SliceQpY and init_qp_minus26, got: %v", err)
			}
		})

		// The P-skip path parses its own slice header. A stream may send a new
		// PPS between pictures, so a legal IDR followed by a P slice decoded
		// against a replacement PPS reaches it.
		t.Run(tc.name+"/pskip", func(t *testing.T) {
			p := EncodeParams{Width: qpRangeSize, Height: qpRangeSize, QP: 26}
			prefix, _, _ := qpRangeParamSets(t, generatePPS(26, 1, 1, false, -1))
			pskip, err := GeneratePSkip(p, 1)
			if err != nil {
				t.Fatal(err)
			}
			var stream bytes.Buffer
			stream.Write(prefix)
			stream.Write(qpRangeIDR(t))
			WriteNALU(&stream, naluPPS, qpRangePPS(tc.initQPMinus26, 0, 0))
			stream.Write(pskip)

			n, err := decodeNoPanic(t, stream.Bytes())
			if err == nil {
				t.Fatalf("expected the P slice to be refused, got %d frames", n)
			}
			if !strings.Contains(err.Error(), "SliceQpY") {
				t.Errorf("the error should name SliceQpY, got: %v", err)
			}
		})
	}
}

// TestDecoderRefusesChromaQPOffsetOutOfRange pins the refusal of a picture-level
// chroma QP offset outside ±12, per component. Chroma QP does not take part in
// parsing, so the slice decodes at the limits, which is the control.
func TestDecoderRefusesChromaQPOffsetOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cb, cr   int
		wantName string // empty when the stream must decode
	}{
		{"limits", 12, -12, ""},
		{"cb13", 13, 0, "pps_cb_qp_offset"},
		{"cr-13", 0, -13, "pps_cr_qp_offset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix, _, _ := qpRangeParamSets(t, qpRangePPS(0, tc.cb, tc.cr))
			n, err := decodeNoPanic(t, append(prefix, qpRangeIDR(t)...))
			if tc.wantName == "" {
				if err != nil {
					t.Fatalf("decode: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected the slice to be refused, got %d frames", n)
			}
			if !strings.Contains(err.Error(), tc.wantName) {
				t.Errorf("the error should name %s, got: %v", tc.wantName, err)
			}
		})
	}
}

// TestSliceQPLimitsRoundTrip codes a picture at either end of the QP range,
// through an external PPS since EncodeParams reads a QP of 0 as the default, and
// checks it decodes close to the source. QP 0 is near-lossless; QP 51 is the
// coarsest quantizer there is.
func TestSliceQPLimitsRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		qp       int
		maxDelta int
	}{
		{0, 1},
		{51, 16},
	} {
		t.Run("qp"+strconv.Itoa(tc.qp), func(t *testing.T) {
			prefix, sps, pps := qpRangeParamSets(t, qpRangePPS(tc.qp-26, 0, 0))
			grid, colors := yuv.SolidGrid(qpRangeSize, qpRangeSize, yuv.Color{Y: 200, Cb: 100, Cr: 160})
			idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
			if err != nil {
				t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
			}
			frames, err := decoder.New().DecodeAnnexB(append(prefix, idr...))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			for i, v := range frames[0].Y[:qpRangeSize*qpRangeSize] {
				if d := abs(int(v) - 200); d > tc.maxDelta {
					t.Fatalf("luma sample %d is %d, want 200 within %d", i, v, tc.maxDelta)
				}
			}
		})
	}
}

// TestEncodeParamsQPOutOfRangeIsRefused pins the generators' refusal of a QP no
// PPS can carry. QP 0 stays the default, 26.
func TestEncodeParamsQPOutOfRangeIsRefused(t *testing.T) {
	grid, colors := yuv.SolidGrid(qpRangeSize, qpRangeSize, yuv.Color{Y: 200, Cb: 100, Cr: 160})
	for _, qp := range []int{-1, -6, 52, 100} {
		p := EncodeParams{Width: qpRangeSize, Height: qpRangeSize, QP: qp}
		for name, gen := range map[string]func() error{
			"GenerateVPSSPSPPS": func() error { _, err := GenerateVPSSPSPPS(p); return err },
			"GenerateIDR":       func() error { _, err := GenerateIDR(p, grid, colors); return err },
			"GeneratePSkip":     func() error { _, err := GeneratePSkip(p, 1); return err },
		} {
			err := gen()
			if err == nil {
				t.Errorf("%s accepted QP %d", name, qp)
				continue
			}
			if !strings.Contains(err.Error(), "QP") {
				t.Errorf("%s: the error should name the QP, got: %v", name, err)
			}
		}
	}
	for _, qp := range []int{0, 1, 51} {
		p := EncodeParams{Width: qpRangeSize, Height: qpRangeSize, QP: qp}
		if _, err := GenerateVPSSPSPPS(p); err != nil {
			t.Errorf("GenerateVPSSPSPPS refused QP %d: %v", qp, err)
		}
	}
}

// TestExternalPPSQPOutOfRangeIsRefused pins the refusal of an external PPS whose
// QP fields are out of range, by every writer that takes one. Each writes
// slice_qp_delta 0, so an illegal init_qp_minus26 becomes an illegal slice QP, and
// the residual writers would quantize at it.
func TestExternalPPSQPOutOfRangeIsRefused(t *testing.T) {
	grid, colors := yuv.SolidGrid(qpRangeSize, qpRangeSize, yuv.Color{Y: 200, Cb: 100, Cr: 160})
	writers := map[string]func(*hevc.SPS, *hevc.PPS) error{
		"EncodeIDRSliceFromSPSPPS": func(s *hevc.SPS, p *hevc.PPS) error {
			_, err := EncodeIDRSliceFromSPSPPS(s, p, grid, colors)
			return err
		},
		"EncodeCRASliceFromSPSPPS": func(s *hevc.SPS, p *hevc.PPS) error {
			_, err := EncodeCRASliceFromSPSPPS(s, p, grid, colors, 8)
			return err
		},
		"EncodePSkipSliceFromSPSPPS": func(s *hevc.SPS, p *hevc.PPS) error {
			_, err := EncodePSkipSliceFromSPSPPS(s, p, 1)
			return err
		},
		"EncodeGrayIDRSliceFromSPSPPS": func(s *hevc.SPS, p *hevc.PPS) error {
			_, err := EncodeGrayIDRSliceFromSPSPPS(s, p)
			return err
		},
		"EncodeGrayCRASliceFromSPSPPS": func(s *hevc.SPS, p *hevc.PPS) error {
			_, err := EncodeGrayCRASliceFromSPSPPS(s, p, 8)
			return err
		},
	}
	for _, tc := range []struct {
		name          string
		initQPMinus26 int
		cb, cr        int
		wantName      string
		grayToo       bool // a gray picture uses no chroma QP, so its writer ignores the offsets
	}{
		{"qp-1", -27, 0, 0, "init_qp_minus26", true},
		{"qp52", 26, 0, 0, "init_qp_minus26", true},
		{"cb13", 0, 13, 0, "pps_cb_qp_offset", false},
		{"cr-13", 0, 0, -13, "pps_cr_qp_offset", false},
	} {
		_, sps, pps := qpRangeParamSets(t, qpRangePPS(tc.initQPMinus26, tc.cb, tc.cr))
		for name, write := range writers {
			if strings.Contains(name, "Gray") && !tc.grayToo {
				continue
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				var err error
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("panicked: %v", r)
						}
					}()
					err = write(sps, pps)
				}()
				if err == nil {
					t.Fatal("expected the PPS to be refused")
				}
				if !strings.Contains(err.Error(), tc.wantName) {
					t.Errorf("the error should name %s, got: %v", tc.wantName, err)
				}
			})
		}
	}
}
