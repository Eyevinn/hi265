// Command hi265retile stitches several HEVC Annex-B videos into one tiled
// picture by bitstream editing: a merged SPS/PPS, one rewritten slice header
// per tile, and the CABAC payloads copied verbatim. Nothing is re-encoded, so
// the output is bit-for-bit the same picture data as the inputs.
//
// Inputs are placed in a tile grid in row-major order. By default they are
// stacked vertically (Nx1); use -grid RxC for an R-row by C-column grid.
// All inputs must share coding parameters and CTB size, be CTB-aligned, code
// one picture per slice segment, have tiles/WPP disabled and no
// slice-segment-header extension, and be all-intra or motion-constrained
// (MCTS) for inter frames. Everything but MCTS is checked and refused with a
// message naming the offending property; MCTS is not visible in the bitstream,
// which is what -verify is for.
//
// Usage:
//
//	hi265retile -o merged.265 top.265 bottom.265            # vertical (2x1)
//	hi265retile -grid 2x2 -o merged.265 a.265 b.265 c.265 d.265
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Eyevinn/hi265/pkg/retile"
)

func main() {
	out := flag.String("o", "merged.265", "output Annex-B file")
	grid := flag.String("grid", "", "tile grid RxC (rows x cols), inputs row-major; default vertical Nx1")
	doVerify := flag.Bool("verify", false, "after writing, decode and compare each tile to its source")
	dec := flag.String("decoder", string(retile.DecoderAuto),
		"verification decoder: auto, hi265 (in-process) or ffmpeg")
	flag.Parse()
	inputs := flag.Args()
	if len(inputs) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hi265retile [-grid RxC] -o merged.265 in0.265 in1.265 [...]")
		os.Exit(1)
	}

	rows, cols, err := retile.ParseGrid(*grid, len(inputs))
	check(err)

	in, err := retile.ReadInputs(inputs)
	check(err)

	res, err := retile.Stitch(in, rows, cols)
	check(err)

	for _, n := range res.Notes {
		fmt.Fprintln(os.Stderr, "note:", n)
	}

	check(os.WriteFile(*out, res.Data, 0o644))
	fmt.Printf("wrote %s: %dx%d, %d frames, grid %dx%d (%d tiles), level %.1f, %d bytes\n",
		*out, res.Width, res.Height, res.Frames, res.Rows, res.Cols, res.Rows*res.Cols,
		float64(res.LevelIDC)/30, len(res.Data))

	if *doVerify {
		check(retile.Verify(res, retile.DecoderKind(*dec), os.Stdout))
		fmt.Println("verify: all tiles pixel-perfect")
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
