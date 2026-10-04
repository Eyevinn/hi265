package retile

import "testing"

// BenchmarkStitch stitches four two-picture inputs into a 2x2 grid: it splits
// them into NAL units, parses their parameter sets and slice headers with
// mp4ff, and rewrites the headers around slice payloads copied verbatim.
func BenchmarkStitch(b *testing.B) {
	inputs, err := ReadInputs([]string{inA, inB, inB, inA})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Stitch(inputs, 2, 2); err != nil {
			b.Fatal(err)
		}
	}
}
