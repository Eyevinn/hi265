package encode

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/sei"
)

// TimeCode holds the per-picture data for a Time Code SEI (payload type 136,
// ISO/IEC 23008-2 D.2.27) carrying a single SMPTE clock timestamp
// (HH:MM:SS:FF).
//
// Unlike H.264, where the clock timestamp rides in the pic_timing SEI and
// requires the SPS VUI to signal pic_struct_present_flag, HEVC's Time Code SEI
// has no parameter-set prerequisite: it can be attached to any picture of any
// stream.
type TimeCode struct {
	Hours   uint8  // 0-23, 5 bits in the bitstream
	Minutes uint8  // 0-59, 6 bits
	Seconds uint8  // 0-59, 6 bits
	Frames  uint16 // frame index within the current second (n_frames), 9 bits

	// Dropped sets cnt_dropped_flag for this picture: true on the frames where
	// a drop-frame label skip occurs. Only meaningful when DropFrame is set.
	Dropped bool

	// DropFrame selects NTSC drop-frame counting semantics: it sets
	// counting_type = 4 (so decoders show the timecode as drop-frame) instead
	// of 1. Set Dropped on the individual frames where labels are skipped.
	DropFrame bool
}

// validate rejects field values that would not survive the fixed-width
// bitstream fields.
func (tc TimeCode) validate() error {
	switch {
	case tc.Hours > 23:
		return fmt.Errorf("hours must be 0-23, got %d", tc.Hours)
	case tc.Minutes > 59:
		return fmt.Errorf("minutes must be 0-59, got %d", tc.Minutes)
	case tc.Seconds > 59:
		return fmt.Errorf("seconds must be 0-59, got %d", tc.Seconds)
	case tc.Frames > 511:
		return fmt.Errorf("n_frames must be 0-511, got %d", tc.Frames)
	}
	return nil
}

// GenerateTimeCodeSEI returns an Annex-B PREFIX_SEI NALU (nal_unit_type 39)
// carrying a Time Code SEI for one picture. Prepend it to an IDR, CRA or
// P-skip slice to attach the timecode to that picture; it composes identically
// with any slice type.
func GenerateTimeCodeSEI(tc TimeCode) ([]byte, error) {
	if err := tc.validate(); err != nil {
		return nil, fmt.Errorf("GenerateTimeCodeSEI: %w", err)
	}
	var buf bytes.Buffer
	WriteNALU(&buf, naluPrefixSEI, timeCodeRBSP(tc))
	return buf.Bytes(), nil
}

// BuildTimeCodeSEINALU is the raw-NALU form of GenerateTimeCodeSEI (2-byte
// HEVC header + EBSP, no Annex-B start code), suitable for MP4 sample
// assembly.
func BuildTimeCodeSEINALU(tc TimeCode) ([]byte, error) {
	if err := tc.validate(); err != nil {
		return nil, fmt.Errorf("BuildTimeCodeSEINALU: %w", err)
	}
	return buildNALU(naluPrefixSEI, timeCodeRBSP(tc)), nil
}

// timeCodeMessage builds the mp4ff Time Code SEI message for one picture:
// num_clock_ts = 1 with a full timestamp (hours, minutes and seconds always
// present) and no time offset.
func timeCodeMessage(tc TimeCode) *sei.TimeCodeSEI {
	// counting_type: 1 = no dropping of n_frames values; 4 = NTSC drop-frame
	// (the two lowest n_frames values are dropped each minute except every
	// tenth). See ISO/IEC 23008-2 Table D.2.
	countingType := byte(1)
	if tc.DropFrame {
		countingType = 4
	}
	return &sei.TimeCodeSEI{
		Clocks: []sei.ClockTS{{
			ClockTimeStampFlag:  true,
			UnitsFieldBasedFlag: false,
			CountingType:        countingType,
			FullTimeStampFlag:   true,
			DiscontinuityFlag:   false,
			CntDroppedFlag:      tc.Dropped,
			NFrames:             tc.Frames,
			Seconds:             tc.Seconds,
			Minutes:             tc.Minutes,
			Hours:               tc.Hours,
			TimeOffsetLength:    0,
		}},
	}
}

// timeCodeRBSP builds the SEI NAL RBSP: payloadType, payloadSize, the time_code
// payload bytes and rbsp_trailing_bits. EBSP escaping is left to
// WriteNALU/buildNALU so it stays consistent with the rest of the encoder.
func timeCodeRBSP(tc TimeCode) []byte {
	msg := timeCodeMessage(tc)
	payload := msg.Payload()

	w := NewBitWriter()
	writeSEIValue(w, int(msg.Type())) // payloadType (= 136)
	writeSEIValue(w, len(payload))    // payloadSize
	for _, b := range payload {
		w.WriteBits(uint32(b), 8)
	}
	w.WriteBit(1) // rbsp_trailing_bits: stop one bit
	w.AlignToByte()
	return w.Bytes()
}

// writeSEIValue writes an SEI payloadType/payloadSize value using the 0xFF
// continuation encoding from ISO/IEC 23008-2 Section 7.3.5.
func writeSEIValue(w *BitWriter, v int) {
	for v >= 0xFF {
		w.WriteBits(0xFF, 8)
		v -= 0xFF
	}
	w.WriteBits(uint32(v), 8)
}
