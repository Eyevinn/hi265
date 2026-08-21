package encode

import (
	"strings"
	"testing"
)

// TestChooseCodingLayout pins the minimum-CB decision. Picture dimensions must
// be an integer multiple of MinCbSizeY, so a 16x16 minimum can only express
// multiples of 16; anything else has to drop to 8.
func TestChooseCodingLayout(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		use8x8        bool
		wantMinCb     int
		wantSplit     bool
	}{
		{"multiple of 16 keeps minCb 16", 192, 96, false, 16, false},
		{"1080 needs minCb 8", 1920, 1080, false, 8, false},
		{"360 needs minCb 8", 640, 360, false, 8, false},
		{"width alone can force it", 328, 96, false, 8, false},
		{"1088 stays at 16", 1920, 1088, false, 16, false},
		{"8x8 mode always splits to 8", 192, 96, true, 8, true},
		{"8x8 mode at an odd height", 640, 360, true, 8, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lay := chooseCodingLayout(c.width, c.height, c.use8x8)
			if lay.minCbSize != c.wantMinCb {
				t.Errorf("minCbSize = %d, want %d", lay.minCbSize, c.wantMinCb)
			}
			if lay.splitToMin != c.wantSplit {
				t.Errorf("splitToMin = %v, want %v", lay.splitToMin, c.wantSplit)
			}
			if lay.ctuSize != 16 {
				t.Errorf("ctuSize = %d, want 16", lay.ctuSize)
			}
			// The picture must be expressible: both dimensions have to be a
			// whole number of minimum coding blocks.
			if c.width%lay.minCbSize != 0 || c.height%lay.minCbSize != 0 {
				t.Errorf("%dx%d is not a multiple of the chosen minCbSize %d",
					c.width, c.height, lay.minCbSize)
			}
		})
	}
}

// TestValidateFrameDimensions checks that unusable sizes are reported rather
// than panicking deep in the encoder, which is what they used to do.
func TestValidateFrameDimensions(t *testing.T) {
	valid := [][2]int{{16, 16}, {192, 96}, {1920, 1080}, {640, 360}, {128, 72}, {8, 8}}
	for _, d := range valid {
		if err := validateFrameDimensions(d[0], d[1]); err != nil {
			t.Errorf("validateFrameDimensions(%d, %d) = %v, want nil", d[0], d[1], err)
		}
	}

	invalid := [][2]int{{322, 244}, {192, 100}, {0, 96}, {192, 0}, {-16, 16}, {12, 8}}
	for _, d := range invalid {
		err := validateFrameDimensions(d[0], d[1])
		if err == nil {
			t.Errorf("validateFrameDimensions(%d, %d) = nil, want an error", d[0], d[1])
			continue
		}
		// The message should say what to use instead.
		if d[0] > 0 && d[1] > 0 && !strings.Contains(err.Error(), "nearest usable size") {
			t.Errorf("error for %dx%d does not suggest a usable size: %v", d[0], d[1], err)
		}
	}
}

// TestGenerateRejectsBadDimensions checks the public entry points, since that is
// where a caller meets the constraint.
func TestGenerateRejectsBadDimensions(t *testing.T) {
	p := EncodeParams{Width: 322, Height: 244, QP: 26}

	if _, err := GenerateVPSSPSPPS(p); err == nil {
		t.Error("GenerateVPSSPSPPS accepted a 322x244 frame")
	}
	if _, err := GeneratePSkip(p, 1); err == nil {
		t.Error("GeneratePSkip accepted a 322x244 frame")
	}
}
