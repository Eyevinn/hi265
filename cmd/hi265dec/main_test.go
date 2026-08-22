package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
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

// A real encoder's P picture needs motion compensation, which this decoder does
// not implement — it reconstructs only zero-motion skip CUs. Decoding one must
// fail saying so rather than return a picture that looks plausible and is wrong.
// The first picture of the same file is an IDR and decodes normally, which is
// what -n 1 asks for.
//
// This also pins the pred_weight_table parse: x265 enables weighted prediction
// by default, and skipping that table left the reader mid-header, so the failure
// used to come from garbage entry point offsets in the very first CTU rather
// than from the inter CU that actually stops us.
func TestRunRefusesRealInterPicture(t *testing.T) {
	in := filepath.Join("..", "..", "testdata", "hevc_1idr_1p.mp4")
	if _, err := os.Stat(in); err != nil {
		t.Skipf("test bitstream not available: %v", err)
	}
	dir := t.TempDir()

	err := run([]string{appName, in, "-o", filepath.Join(dir, "all.yuv")})
	if err == nil {
		t.Fatal("expected the P picture to be refused")
	}
	if !strings.Contains(err.Error(), "motion compensation is not implemented") {
		t.Errorf("error should name the limitation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "inter CU at (304,120)") {
		t.Errorf("error should name the CU that stopped it, which also shows the "+
			"slice header parsed correctly, got: %v", err)
	}

	if err := run([]string{appName, "-n", "1", in, "-o", filepath.Join(dir, "first.yuv")}); err != nil {
		t.Errorf("the IDR picture on its own should decode: %v", err)
	}
}
