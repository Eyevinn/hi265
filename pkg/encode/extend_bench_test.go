package encode

import (
	"os"
	"testing"
)

// BenchmarkLastFrameState finds the last picture of a 100-picture stream,
// which parses every slice header with mp4ff's hevc.ParseSliceHeader.
func BenchmarkLastFrameState(b *testing.B) {
	src, err := os.ReadFile("../../testdata/sincos_128x64.265")
	if err != nil {
		b.Fatal(err)
	}
	stream, err := AppendEmptyFrames(src, 99)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := LastFrameState(stream); err != nil {
			b.Fatal(err)
		}
	}
}
