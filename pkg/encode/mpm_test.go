package encode

import "testing"

// TestDeriveMPMCTBBoundary pins HEVC spec 8.4.2: intra mode prediction does not
// cross a horizontal CTB boundary, so candIntraPredModeB is INTRA_DC whenever
// the above CU lies in a different CTB. Dropping the rule leaves the encoder and
// decoder self-consistent but makes every conforming decoder disagree.
func TestDeriveMPMCTBBoundary(t *testing.T) {
	const dc = 1

	// coded is a block already coded before the one under test: its pixel
	// origin, size and intra mode.
	type coded struct {
		x, y, size, mode int
	}
	cases := []struct {
		name        string
		x0, y0      int
		log2CtbSize int
		before      []coded
		want        [3]int
	}{
		{
			// CTB = CU = 16: the above CU is always in the CTB above, so its
			// mode 26 must be ignored in favour of DC. Left is unavailable
			// (x0 == 0) and also becomes DC, so both candidates match.
			name:        "above outside CTB is ignored",
			x0:          0,
			y0:          16,
			log2CtbSize: 4,
			before:      []coded{{0, 0, 16, 26}},
			want:        [3]int{0, dc, 26},
		},
		{
			// Same geometry with CTB = 32: the above CU is now inside the
			// current CTB, so mode 26 is a genuine candidate.
			name:        "above inside CTB is used",
			x0:          0,
			y0:          16,
			log2CtbSize: 5,
			before:      []coded{{0, 0, 16, 26}},
			want:        [3]int{dc, 26, 0},
		},
		{
			// Left neighbour is available in the same CTB row and must still be
			// used; only the above candidate is replaced by DC. A regression
			// that kept the above mode would yield {10, 26, 0} here.
			name:        "left kept while above is replaced",
			x0:          16,
			y0:          16,
			log2CtbSize: 4,
			before:      []coded{{16, 0, 16, 26}, {0, 16, 16, 10}},
			want:        [3]int{10, dc, 0},
		},
		{
			// Mixed CU sizes, which a partial bottom CTU row produces: an 8x8 CU
			// at (8,24) whose left neighbour is another 8x8 CU inside the same
			// CTB. Addressing by pixel picks up that 8x8 CU's mode 10; keying by
			// CU index would have divided 8 by the wrong size and looked
			// somewhere else entirely. The above candidate at y=23 is in the same
			// CTB (which spans y 16..31), so it is used rather than forced to DC.
			name:        "mixed CU sizes resolve by pixel",
			x0:          8,
			y0:          24,
			log2CtbSize: 4,
			before:      []coded{{0, 16, 16, 26}, {0, 24, 8, 10}},
			want:        [3]int{10, 26, 0},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			modes := newIntraModes()
			for _, b := range c.before {
				modes.set(b.x, b.y, b.size, b.mode)
			}
			got := deriveMPM(c.x0, c.y0, c.log2CtbSize, modes)
			if got != c.want {
				t.Errorf("deriveMPM = %v, want %v", got, c.want)
			}
		})
	}
}
