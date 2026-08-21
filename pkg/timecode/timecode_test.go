package timecode

import (
	"fmt"
	"testing"
)

func TestComponentsNonDrop(t *testing.T) {
	cases := []struct {
		frame int64
		rate  int
		want  string
	}{
		{0, 25, "00:00:00:00"},
		{24, 25, "00:00:00:24"},
		{25, 25, "00:00:01:00"},
		{76, 25, "00:00:03:01"},
		{25 * 60, 25, "00:01:00:00"},
		{25 * 3600, 25, "01:00:00:00"},
		{29, 30, "00:00:00:29"},
		{30, 30, "00:00:01:00"},
		{60 * 60 * 24 * 25, 25, "00:00:00:00"}, // wraps at 24h
		{-1, 25, "23:59:59:24"},                // negative wraps into range
		{0, 0, "00:00:00:00"},                  // rate <= 0 treated as 1
	}
	for _, c := range cases {
		h, m, s, f, dropped := Components(c.frame, c.rate, false)
		got := fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, f)
		if got != c.want {
			t.Errorf("Components(%d, %d, false) = %s, want %s", c.frame, c.rate, got, c.want)
		}
		if dropped {
			t.Errorf("Components(%d, %d, false) reported dropped", c.frame, c.rate)
		}
	}
}

// TestComponentsDropFrame30 walks the 29.97 minute boundary: labels ;00 and ;01
// are skipped entering minute 1, and the tenth minute keeps all its labels.
func TestComponentsDropFrame30(t *testing.T) {
	cases := []struct {
		frame       int64
		want        string
		wantDropped bool
	}{
		{0, "00:00:00:00", false},
		{1798, "00:00:59:28", false},
		{1799, "00:00:59:29", false},
		{1800, "00:01:00:02", true}, // labels :00 and :01 dropped
		{1801, "00:01:00:03", false},
		// Minutes 1-9 of a ten-minute block are 1798 frames long.
		{1800 + 1797, "00:01:59:29", false},
		{1800 + 1798, "00:02:00:02", true},
		// The tenth minute (index 9) keeps all labels: 10 minutes of 29.97
		// counting is 17982 frames.
		{17982 - 1, "00:09:59:29", false},
		{17982, "00:10:00:00", false},
		{17982 * 6, "01:00:00:00", false},
	}
	for _, c := range cases {
		h, m, s, f, dropped := Components(c.frame, 30, true)
		got := fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, f)
		if got != c.want {
			t.Errorf("Components(%d, 30, true) = %s, want %s", c.frame, got, c.want)
		}
		if dropped != c.wantDropped {
			t.Errorf("Components(%d, 30, true) dropped = %v, want %v", c.frame, dropped, c.wantDropped)
		}
	}
}

// TestComponentsDropFrame60 checks that 59.94 drops four labels per minute.
func TestComponentsDropFrame60(t *testing.T) {
	cases := []struct {
		frame       int64
		want        string
		wantDropped bool
	}{
		{3599, "00:00:59:59", false},
		{3600, "00:01:00:04", true},
		{3601, "00:01:00:05", false},
		{35964 - 1, "00:09:59:59", false}, // 60*600 - 9*4 = 35964
		{35964, "00:10:00:00", false},
	}
	for _, c := range cases {
		h, m, s, f, dropped := Components(c.frame, 60, true)
		got := fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, f)
		if got != c.want {
			t.Errorf("Components(%d, 60, true) = %s, want %s", c.frame, got, c.want)
		}
		if dropped != c.wantDropped {
			t.Errorf("Components(%d, 60, true) dropped = %v, want %v", c.frame, dropped, c.wantDropped)
		}
	}
}

// TestComponentsDropFrameMonotonic verifies drop-frame counting stays strictly
// increasing (as an absolute label) across two ten-minute blocks.
func TestComponentsDropFrameMonotonic(t *testing.T) {
	prev := int64(-1)
	for frame := int64(0); frame < 2*17982; frame++ {
		h, m, s, f, _ := Components(frame, 30, true)
		label := int64(((h*60+m)*60+s)*30 + f)
		if label <= prev {
			t.Fatalf("frame %d: label %d not greater than previous %d", frame, label, prev)
		}
		prev = label
	}
}

func TestFormatText(t *testing.T) {
	cases := []struct {
		pattern   string
		frame     int
		rate      int
		dropFrame bool
		want      string
	}{
		{"%03d", 7, 25, false, "007"},
		{"%d", 123, 25, false, "123"},
		{"%5d", 42, 25, false, "   42"},
		{"%mm:%ss.%ff", 76, 25, false, "00:03.01"},
		{"%hh:%mm:%ss", 25 * 3661, 25, false, "01:01:01"},
		{"%ms", 12, 25, false, "480"},
		{"100%% X", 0, 25, false, "100% X"},
		{"%q", 0, 25, false, "%q"},
		{"%", 0, 25, false, "%"},
		{"FRAME %04d\n%mm:%ss", 30, 25, false, "FRAME 0030\n00:01"},
		// Drop-frame: the frame counter is absolute, the timecode is dropped.
		{"%d %hh:%mm:%ss:%ff", 1800, 30, true, "1800 00:01:00:02"},
		{"%hh:%mm:%ss:%ff", 1800, 30, false, "00:01:00:00"},
	}
	for _, c := range cases {
		got := FormatText(c.pattern, c.frame, c.rate, c.dropFrame)
		if got != c.want {
			t.Errorf("FormatText(%q, %d, %d, %v) = %q, want %q",
				c.pattern, c.frame, c.rate, c.dropFrame, got, c.want)
		}
	}
}
