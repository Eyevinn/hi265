package encode

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/sei"
)

// decodeTimeCodeNALU parses a raw HEVC NALU (2-byte header + EBSP) as a
// PREFIX_SEI carrying a single Time Code SEI message and returns its first
// clock timestamp.
func decodeTimeCodeNALU(t *testing.T, nalu []byte) sei.ClockTS {
	t.Helper()
	if len(nalu) < 3 {
		t.Fatalf("NALU too short: %d bytes", len(nalu))
	}
	if nt := (nalu[0] >> 1) & 0x3F; nt != naluPrefixSEI {
		t.Fatalf("nal_unit_type = %d, want %d (PREFIX_SEI)", nt, naluPrefixSEI)
	}
	sd, err := sei.ExtractSEIData(bytes.NewReader(nalu[2:]))
	if err != nil {
		t.Fatalf("ExtractSEIData: %v", err)
	}
	if len(sd) != 1 {
		t.Fatalf("got %d SEI messages, want 1", len(sd))
	}
	if sd[0].Type() != sei.SEITimeCodeType {
		t.Fatalf("SEI payload type = %d, want %d", sd[0].Type(), sei.SEITimeCodeType)
	}
	msg, err := sei.DecodeTimeCodeSEI(&sd[0])
	if err != nil {
		t.Fatalf("DecodeTimeCodeSEI: %v", err)
	}
	tcMsg, ok := msg.(*sei.TimeCodeSEI)
	if !ok {
		t.Fatalf("decoded message type %T, want *sei.TimeCodeSEI", msg)
	}
	if len(tcMsg.Clocks) != 1 {
		t.Fatalf("got %d clock timestamps, want 1", len(tcMsg.Clocks))
	}
	return tcMsg.Clocks[0]
}

// TestTimeCodeSEIRoundTrip encodes a Time Code SEI and parses it back with
// mp4ff's decoder, checking every field we set.
func TestTimeCodeSEIRoundTrip(t *testing.T) {
	cases := []struct {
		name             string
		tc               TimeCode
		wantCountingType byte
	}{
		{"zero", TimeCode{}, 1},
		{"plain", TimeCode{Hours: 1, Minutes: 2, Seconds: 3, Frames: 4}, 1},
		{"max", TimeCode{Hours: 23, Minutes: 59, Seconds: 59, Frames: 59}, 1},
		{"dropframe", TimeCode{Minutes: 1, Frames: 2, Dropped: true, DropFrame: true}, 4},
		{"dropframe-nodrop", TimeCode{Minutes: 1, Seconds: 30, Frames: 5, DropFrame: true}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nalu, err := BuildTimeCodeSEINALU(c.tc)
			if err != nil {
				t.Fatalf("BuildTimeCodeSEINALU: %v", err)
			}
			clock := decodeTimeCodeNALU(t, nalu)
			if clock.Hours != c.tc.Hours || clock.Minutes != c.tc.Minutes ||
				clock.Seconds != c.tc.Seconds || clock.NFrames != c.tc.Frames {
				t.Errorf("timecode = %02d:%02d:%02d:%02d, want %02d:%02d:%02d:%02d",
					clock.Hours, clock.Minutes, clock.Seconds, clock.NFrames,
					c.tc.Hours, c.tc.Minutes, c.tc.Seconds, c.tc.Frames)
			}
			if !clock.ClockTimeStampFlag {
				t.Error("clock_timestamp_flag = false, want true")
			}
			if !clock.FullTimeStampFlag {
				t.Error("full_timestamp_flag = false, want true")
			}
			if clock.CountingType != c.wantCountingType {
				t.Errorf("counting_type = %d, want %d", clock.CountingType, c.wantCountingType)
			}
			if clock.CntDroppedFlag != c.tc.Dropped {
				t.Errorf("cnt_dropped_flag = %v, want %v", clock.CntDroppedFlag, c.tc.Dropped)
			}
			if clock.UnitsFieldBasedFlag {
				t.Error("units_field_based_flag = true, want false")
			}
			if clock.DiscontinuityFlag {
				t.Error("discontinuity_flag = true, want false")
			}
			if clock.TimeOffsetLength != 0 {
				t.Errorf("time_offset_length = %d, want 0", clock.TimeOffsetLength)
			}
		})
	}
}

// TestGenerateTimeCodeSEIAnnexB checks that the Annex-B form is the raw NALU
// prefixed by a 4-byte start code.
func TestGenerateTimeCodeSEIAnnexB(t *testing.T) {
	tc := TimeCode{Hours: 12, Minutes: 34, Seconds: 56, Frames: 7}
	annexB, err := GenerateTimeCodeSEI(tc)
	if err != nil {
		t.Fatalf("GenerateTimeCodeSEI: %v", err)
	}
	if !bytes.HasPrefix(annexB, []byte{0, 0, 0, 1}) {
		t.Fatalf("Annex-B NALU does not start with a start code: % x", annexB[:min(4, len(annexB))])
	}
	raw, err := BuildTimeCodeSEINALU(tc)
	if err != nil {
		t.Fatalf("BuildTimeCodeSEINALU: %v", err)
	}
	if !bytes.Equal(annexB[4:], raw) {
		t.Errorf("Annex-B payload % x != raw NALU % x", annexB[4:], raw)
	}
	clock := decodeTimeCodeNALU(t, annexB[4:])
	if clock.Hours != 12 || clock.Minutes != 34 || clock.Seconds != 56 || clock.NFrames != 7 {
		t.Errorf("timecode = %02d:%02d:%02d:%02d, want 12:34:56:07",
			clock.Hours, clock.Minutes, clock.Seconds, clock.NFrames)
	}
}

func TestTimeCodeSEIValidation(t *testing.T) {
	bad := []TimeCode{
		{Hours: 24},
		{Minutes: 60},
		{Seconds: 60},
		{Frames: 512},
	}
	for _, tc := range bad {
		if _, err := GenerateTimeCodeSEI(tc); err == nil {
			t.Errorf("GenerateTimeCodeSEI(%+v) expected an error", tc)
		}
		if _, err := BuildTimeCodeSEINALU(tc); err == nil {
			t.Errorf("BuildTimeCodeSEINALU(%+v) expected an error", tc)
		}
	}
}
