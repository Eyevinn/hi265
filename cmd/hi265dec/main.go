// Command hi265dec decodes HEVC/H.265 video to raw frames.
//
// Usage:
//
//	hi265dec [-o output.yuv] input.265
//	hi265dec input.265 -o output.yuv
//
// Flags may appear before or after the input path.
//
// Supported input: Annex-B byte stream (.265, .hevc, .h265)
// Supported output: raw YUV 4:2:0 (.yuv), PNG (.png)
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

const appName = "hi265dec"

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	outPath := fs.String("o", "", "output file path (.yuv or .png)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [-o output] input.265\n\nFlags:\n", appName)
		fs.PrintDefaults()
	}

	positional, err := parseInterleaved(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	switch len(positional) {
	case 1:
	case 0:
		fs.Usage()
		return errors.New("no input file given")
	default:
		return fmt.Errorf("expected one input file, got %d: %v", len(positional), positional)
	}
	inPath := positional[0]

	data, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}

	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(data)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	fmt.Printf("Decoded %d frame(s)\n", len(frames))
	f := frames[len(frames)-1] // use last frame for output
	fmt.Printf("Last frame: %dx%d\n", f.Width, f.Height)

	out := *outPath
	if out == "" {
		// Default: replace extension with .yuv
		out = strings.TrimSuffix(inPath, filepath.Ext(inPath)) + ".yuv"
	}

	ext := strings.ToLower(filepath.Ext(out))
	switch ext {
	case ".yuv":
		// Write all frames concatenated
		var yuvData []byte
		for _, fr := range frames {
			yuvData = append(yuvData, fr.YUV420Bytes()...)
		}
		err = os.WriteFile(out, yuvData, 0644)
	case ".png":
		err = writePNG(out, f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	default:
		return fmt.Errorf("unsupported output format %q (use .yuv or .png)", ext)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Printf("Written %s\n", out)
	return nil
}

// parseInterleaved parses flags that appear before or after positional
// arguments and returns the positionals. Go's flag package stops at the first
// non-flag argument, which silently ignored a trailing "-o out.yuv".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	for fs.NArg() > 0 {
		rest := fs.Args()
		positional = append(positional, rest[0])
		if err := fs.Parse(rest[1:]); err != nil {
			return nil, err
		}
	}
	return positional, nil
}

// writePNG converts YUV 4:2:0 to RGB and writes a PNG.
func writePNG(path string, y, cb, cr []uint8, width, height, strideY, strideC int) error {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	for py := range height {
		for px := range width {
			yy := float64(y[py*strideY+px])
			uu := float64(cb[(py/2)*strideC+px/2]) - 128
			vv := float64(cr[(py/2)*strideC+px/2]) - 128

			// BT.601 YUV to RGB
			r := yy + 1.402*vv
			g := yy - 0.344136*uu - 0.714136*vv
			b := yy + 1.772*uu

			img.SetRGBA(px, py, color.RGBA{
				R: clampU8(r),
				G: clampU8(g),
				B: clampU8(b),
				A: 255,
			})
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func clampU8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}
