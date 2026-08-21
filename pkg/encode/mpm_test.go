package encode

import "testing"

// TestDeriveMPMCTBBoundary pins HEVC spec 8.4.2: intra mode prediction does not
// cross a horizontal CTB boundary, so candIntraPredModeB is INTRA_DC whenever
// the above CU lies in a different CTB. Dropping the rule leaves the encoder and
// decoder self-consistent but makes every conforming decoder disagree.
func TestDeriveMPMCTBBoundary(t *testing.T) {
	const dc = 1

	cases := []struct {
		name        string
		x0, y0      int
		cuSize      int
		log2CtbSize int
		modeMap     map[[2]int]int
		want        [3]int
	}{
		{
			// CTB = CU = 16: the above CU is always in the CTB above, so its
			// mode 26 must be ignored in favour of DC. Left is unavailable
			// (x0 == 0) and also becomes DC, so both candidates match.
			name:        "above outside CTB is ignored",
			x0:          0,
			y0:          16,
			cuSize:      16,
			log2CtbSize: 4,
			modeMap:     map[[2]int]int{{0, 0}: 26},
			want:        [3]int{0, dc, 26},
		},
		{
			// Same geometry with CTB = 32: the above CU is now inside the
			// current CTB, so mode 26 is a genuine candidate.
			name:        "above inside CTB is used",
			x0:          0,
			y0:          16,
			cuSize:      16,
			log2CtbSize: 5,
			modeMap:     map[[2]int]int{{0, 0}: 26},
			want:        [3]int{dc, 26, 0},
		},
		{
			// Left neighbour is available in the same CTB row and must still be
			// used; only the above candidate is replaced by DC. A regression
			// that kept the above mode would yield {10, 26, 0} here.
			name:        "left kept while above is replaced",
			x0:          16,
			y0:          16,
			cuSize:      16,
			log2CtbSize: 4,
			modeMap:     map[[2]int]int{{1, 0}: 26, {0, 1}: 10},
			want:        [3]int{10, dc, 0},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveMPM(c.x0, c.y0, c.cuSize, 64, c.log2CtbSize, c.modeMap)
			if got != c.want {
				t.Errorf("deriveMPM = %v, want %v", got, c.want)
			}
		})
	}
}
