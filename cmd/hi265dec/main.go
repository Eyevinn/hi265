// Command hi265dec decodes HEVC/H.265 video to raw frames.
//
// Usage:
//
//	hi265dec [-o output.yuv] input.265
//
// Supported input: Annex-B byte stream (.265, .hevc, .h265)
// Supported output: raw YUV 4:2:0 (.yuv), PNG (.png)
package main

import (
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

func main() {
	outPath := flag.String("o", "", "output file path (.yuv or .png)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: hi265dec [-o output] input.265\n")
		os.Exit(1)
	}
	inPath := flag.Arg(0)

	data, err := os.ReadFile(inPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", inPath, err)
		os.Exit(1)
	}

	dec := decoder.New()
	f, err := dec.DecodeAnnexB(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Decode error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Decoded %dx%d frame\n", f.Width, f.Height)

	if *outPath == "" {
		// Default: replace extension with .yuv
		*outPath = strings.TrimSuffix(inPath, filepath.Ext(inPath)) + ".yuv"
	}

	ext := strings.ToLower(filepath.Ext(*outPath))
	switch ext {
	case ".yuv":
		err = os.WriteFile(*outPath, f.YUV420Bytes(), 0644)
	case ".png":
		err = writePNG(*outPath, f.Y, f.Cb, f.Cr, f.Width, f.Height, f.StrideY, f.StrideC)
	default:
		fmt.Fprintf(os.Stderr, "Unsupported output format: %s (use .yuv or .png)\n", ext)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	fmt.Printf("Written %s\n", *outPath)
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
