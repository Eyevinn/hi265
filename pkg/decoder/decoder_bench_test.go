package decoder

import (
	"os"
	"testing"
)

// BenchmarkDecodeAnnexB decodes whole streams, parameter sets and slice headers
// included. The pictures are small, so the header parsing done with mp4ff is a
// larger share of the time than it is for pictures of real size.
func BenchmarkDecodeAnnexB(b *testing.B) {
	for _, name := range []string{
		"sincos_128x64.265",
		"slices_wpp_2slices_256x128.265",
		"tiles_multi_2x2_128x128.265",
	} {
		data, err := os.ReadFile("../../testdata/" + name)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := New().DecodeAnnexB(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
