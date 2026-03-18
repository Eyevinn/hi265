// Command hi265gray generates a gray IDR frame given external VPS, SPS, PPS.
//
// This is meant for bootstrapping a decoder for a Gradual Decode Refresh (GDR) stream
// that lacks IDR frames. The generated frame is uniform mid-gray (Y=Cb=Cr=128 for 8-bit,
// Y=Cb=Cr=512 for 10-bit).
//
// Supports any chroma format (4:2:0, 4:2:2, 4:4:4) and bit depth (8, 10, 12) since
// DC prediction on a uniform surface produces zero residual regardless.
//
// Usage:
//
//	hi265gray -f testdata/vps_sps_pps_422_10bit.json -o gray.265
//	hi265gray -vps <hex> -sps <hex> -pps <hex> -o gray.265
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Eyevinn/hi265/internal"
	"github.com/Eyevinn/hi265/pkg/encode"
	"github.com/Eyevinn/mp4ff/hevc"
)

const appName = "hi265gray"

var usg = `%s - generate a gray HEVC IDR frame from external VPS/SPS/PPS.

Usage:

  %s -f testdata/vps_sps_pps_422_10bit.json -o gray.265
  %s -vps <hex> -sps <hex> -pps <hex> -o gray.265

The input file (-f) is a JSON object with vps/sps/pps hex strings:
  {"vps": "40010c...", "sps": "4201...", "pps": "4401..."}

Output is an Annex-B bitstream: VPS + SPS + PPS + gray IDR slice.

Options:
`

type options struct {
	version bool
	input   string
	vpsHex  string
	spsHex  string
	ppsHex  string
	output  string
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	var opts options
	fs.BoolVar(&opts.version, "version", false, "Get hi265gray version")
	fs.StringVar(&opts.input, "f", "", "input JSON file with vps/sps/pps hex strings")
	fs.StringVar(&opts.vpsHex, "vps", "", "VPS NALU as hex string (including 2-byte NAL header)")
	fs.StringVar(&opts.spsHex, "sps", "", "SPS NALU as hex string (including 2-byte NAL header)")
	fs.StringVar(&opts.ppsHex, "pps", "", "PPS NALU as hex string (including 2-byte NAL header)")
	fs.StringVar(&opts.output, "o", "", "output Annex-B file (required)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName, appName)
		fs.PrintDefaults()
	}
	err := fs.Parse(args[1:])
	return &opts, err
}

// paramSetFile represents the JSON input file with VPS/SPS/PPS hex strings.
type paramSetFile struct {
	VPS string `json:"vps"`
	SPS string `json:"sps"`
	PPS string `json:"pps"`
}

// readParamSetsFromFile reads VPS, SPS, PPS hex from a JSON file.
func readParamSetsFromFile(path string) (vps, sps, pps []byte, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}

	var f paramSetFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing JSON: %w", err)
	}

	if f.VPS == "" {
		return nil, nil, nil, fmt.Errorf("no vps found in %s", path)
	}
	if f.SPS == "" {
		return nil, nil, nil, fmt.Errorf("no sps found in %s", path)
	}
	if f.PPS == "" {
		return nil, nil, nil, fmt.Errorf("no pps found in %s", path)
	}

	vps, err = hex.DecodeString(f.VPS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid vps hex: %w", err)
	}
	sps, err = hex.DecodeString(f.SPS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid sps hex: %w", err)
	}
	pps, err = hex.DecodeString(f.PPS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid pps hex: %w", err)
	}
	return vps, sps, pps, nil
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}

	if opts.output == "" {
		fs.Usage()
		return fmt.Errorf("-o output file is required")
	}

	// Get VPS/SPS/PPS NALUs
	var vpsNALU, spsNALU, ppsNALU []byte

	if opts.input != "" {
		if opts.vpsHex != "" || opts.spsHex != "" || opts.ppsHex != "" {
			return fmt.Errorf("-f and -vps/-sps/-pps are mutually exclusive")
		}
		vpsNALU, spsNALU, ppsNALU, err = readParamSetsFromFile(opts.input)
		if err != nil {
			return fmt.Errorf("reading parameter sets: %w", err)
		}
	} else {
		if opts.vpsHex == "" || opts.spsHex == "" || opts.ppsHex == "" {
			fs.Usage()
			return fmt.Errorf("either -f or all of -vps/-sps/-pps are required")
		}
		vpsNALU, err = hex.DecodeString(opts.vpsHex)
		if err != nil {
			return fmt.Errorf("invalid VPS hex: %w", err)
		}
		spsNALU, err = hex.DecodeString(opts.spsHex)
		if err != nil {
			return fmt.Errorf("invalid SPS hex: %w", err)
		}
		ppsNALU, err = hex.DecodeString(opts.ppsHex)
		if err != nil {
			return fmt.Errorf("invalid PPS hex: %w", err)
		}
	}

	// Parse SPS and PPS
	sps, err := hevc.ParseSPSNALUnit(spsNALU)
	if err != nil {
		return fmt.Errorf("parsing SPS: %w", err)
	}
	spsMap := map[uint32]*hevc.SPS{uint32(sps.SpsID): sps}
	pps, err := hevc.ParsePPSNALUnit(ppsNALU, spsMap)
	if err != nil {
		return fmt.Errorf("parsing PPS: %w", err)
	}

	// Log parsed parameters
	chromaFmt := "4:2:0"
	switch sps.ChromaFormatIDC {
	case 2:
		chromaFmt = "4:2:2"
	case 3:
		chromaFmt = "4:4:4"
	}
	bitDepth := 8 + int(sps.BitDepthLumaMinus8)
	log2MinCb := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
	ctuLog2 := log2MinCb + int(sps.Log2DiffMaxMinLumaCodingBlockSize)
	fmt.Fprintf(os.Stderr, "SPS: %dx%d, %s, %d-bit, CTU=%d, minCB=%d\n",
		sps.PicWidthInLumaSamples, sps.PicHeightInLumaSamples,
		chromaFmt, bitDepth, 1<<ctuLog2, 1<<log2MinCb)

	// Generate gray IDR slice
	idrSlice, err := encode.EncodeGrayIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		return fmt.Errorf("encoding gray IDR: %w", err)
	}

	// Write output: VPS + SPS + PPS + IDR in Annex-B format
	f, err := os.Create(opts.output)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write VPS NALU with start code
	var buf bytes.Buffer
	writeRawNALU(&buf, vpsNALU)
	writeRawNALU(&buf, spsNALU)
	writeRawNALU(&buf, ppsNALU)
	if _, err := f.Write(buf.Bytes()); err != nil {
		return err
	}

	// Write IDR slice (already has start code from EncodeGrayIDRSliceFromSPSPPS)
	if _, err := f.Write(idrSlice); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Wrote gray IDR %dx%d (%s, %d-bit) to %s\n",
		sps.PicWidthInLumaSamples, sps.PicHeightInLumaSamples,
		chromaFmt, bitDepth, opts.output)
	return nil
}

// writeRawNALU writes a NALU (with 2-byte header, as from hex) with Annex-B start code.
// The NALU bytes are already in EBSP form (no need for emulation prevention insertion).
func writeRawNALU(buf *bytes.Buffer, nalu []byte) {
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(nalu)
}
