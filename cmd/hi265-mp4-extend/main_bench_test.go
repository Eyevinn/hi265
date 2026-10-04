package main

import (
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkExtendSegment appends 100 frozen pictures to a 100-picture media
// segment: it reads both segments, parses every slice header of the input and
// writes the extended segment.
func BenchmarkExtendSegment(b *testing.B) {
	dir := b.TempDir()
	initPath, segPath := makeInitAndSegment(b, dir, 100)
	outPath := filepath.Join(dir, "out.m4s")

	// extendSegment reports every call on stdout.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = devNull
	b.Cleanup(func() {
		os.Stdout = stdout
		devNull.Close()
	})

	b.ReportAllocs()
	for b.Loop() {
		if err := extendSegment(initPath, segPath, outPath, &options{frames: 100}); err != nil {
			b.Fatal(err)
		}
	}
}
