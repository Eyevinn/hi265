package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// TestParseInterleaved covers flags on either side of the positional argument.
// Go's flag package stops at the first non-flag argument, so "in.265 -o out.yuv"
// used to parse as a single positional with -o silently ignored.
func TestParseInterleaved(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantOut string
		wantPos []string
	}{
		{"flag first", []string{"-o", "out.yuv", "in.265"}, "out.yuv", []string{"in.265"}},
		{"flag last", []string{"in.265", "-o", "out.yuv"}, "out.yuv", []string{"in.265"}},
		{"no flag", []string{"in.265"}, "", []string{"in.265"}},
		{"joined flag last", []string{"in.265", "-o=out.yuv"}, "out.yuv", []string{"in.265"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			out := fs.String("o", "", "output")
			pos, err := parseInterleaved(fs, c.args)
			if err != nil {
				t.Fatalf("parseInterleaved: %v", err)
			}
			if *out != c.wantOut {
				t.Errorf("-o = %q, want %q", *out, c.wantOut)
			}
			if len(pos) != len(c.wantPos) {
				t.Fatalf("positionals = %v, want %v", pos, c.wantPos)
			}
			for i := range pos {
				if pos[i] != c.wantPos[i] {
					t.Errorf("positional %d = %q, want %q", i, pos[i], c.wantPos[i])
				}
			}
		})
	}
}

// TestRunWritesToFlagPath decodes a golden bitstream with -o after the input
// path and checks the output lands where the flag says.
func TestRunWritesToFlagPath(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "black_16x16.265")
	if _, err := os.Stat(in); err != nil {
		t.Skipf("test bitstream not available: %v", err)
	}
	out := filepath.Join(t.TempDir(), "decoded.yuv")

	if err := run([]string{appName, in, "-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("output not written to -o path: %v", err)
	}
	if fi.Size() == 0 {
		t.Error("output file is empty")
	}
}

func TestRunNoInput(t *testing.T) {
	if err := run([]string{appName}); err == nil {
		t.Error("expected an error when no input file is given")
	}
}

// TestRunPositionalOutput covers the hi264dec-style form where the output path
// is the second positional argument rather than -o.
func TestRunPositionalOutput(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "black_16x16.265")
	if _, err := os.Stat(in); err != nil {
		t.Skipf("test bitstream not available: %v", err)
	}
	out := filepath.Join(t.TempDir(), "positional.yuv")

	if err := run([]string{appName, in, out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("output not written to the positional path: %v", err)
	}
}

func TestRunRejectsDoubleOutput(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "black_16x16.265")
	if err := run([]string{appName, in, "a.yuv", "-o", "b.yuv"}); err == nil {
		t.Error("expected an error when the output is given both ways")
	}
}
