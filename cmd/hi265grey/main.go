// Command hi265grey generates a grey IDR frame given external VPS, SPS, PPS.
//
// This is meant for bootstrapping a decoder for a Gradual Decode Refresh (GDR) stream
// that lacks IDR frames. The generated frame is uniform mid-grey (Y=Cb=Cr=128 for 8-bit,
// Y=Cb=Cr=512 for 10-bit).
//
// Supports any chroma format (4:2:0, 4:2:2, 4:4:4) and bit depth (8, 10, 12) since
// DC prediction on a uniform surface produces zero residual regardless.
//
// Usage:
//
//	hi265grey -f data/vps_sps_pps_422_10bit.txt -o grey.265
//	hi265grey -vps <hex> -sps <hex> -pps <hex> -o grey.265
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

const appName = "hi265grey"

var usg = `%s - generate a grey HEVC IDR frame from external VPS/SPS/PPS.

Usage:

  %s -f data/vps_sps_pps_422_10bit.txt -o grey.265
  %s -vps <hex> -sps <hex> -pps <hex> -o grey.265

The input file (-f) is a JSON-lines format with parameterSet entries:
  {"parameterSet": "VPS", "hex": "40010c..."}
  {"parameterSet": "SPS", "hex": "4201..."}
  {"parameterSet": "PPS", "hex": "4401..."}

Output is an Annex-B bitstream: VPS + SPS + PPS + grey IDR slice.

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
	fs.BoolVar(&opts.version, "version", false, "Get hi265grey version")
	fs.StringVar(&opts.input, "f", "", "input file with VPS/SPS/PPS in JSON-lines format")
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

// paramSetEntry represents one JSON object in the input file.
type paramSetEntry struct {
	ParameterSet string `json:"parameterSet"`
	Hex          string `json:"hex"`
}

// readParamSetsFromFile reads the first VPS, SPS, PPS hex from a JSON file
// containing an array of objects with "parameterSet" and "hex" fields.
func readParamSetsFromFile(path string) (vps, sps, pps []byte, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}

	var entries []paramSetEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil, nil, fmt.Errorf("parsing JSON: %w", err)
	}

	for _, entry := range entries {
		if entry.Hex == "" || entry.ParameterSet == "" {
			continue
		}
		decoded, err := hex.DecodeString(entry.Hex)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid hex in %s entry: %w", entry.ParameterSet, err)
		}
		switch entry.ParameterSet {
		case "VPS":
			if vps == nil {
				vps = decoded
			}
		case "SPS":
			if sps == nil {
				sps = decoded
			}
		case "PPS":
			if pps == nil {
				pps = decoded
			}
		}
	}
	if vps == nil {
		return nil, nil, nil, fmt.Errorf("no VPS found in %s", path)
	}
	if sps == nil {
		return nil, nil, nil, fmt.Errorf("no SPS found in %s", path)
	}
	if pps == nil {
		return nil, nil, nil, fmt.Errorf("no PPS found in %s", path)
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

	// Generate grey IDR slice
	idrSlice, err := encode.EncodeGreyIDRSliceFromSPSPPS(sps, pps)
	if err != nil {
		return fmt.Errorf("encoding grey IDR: %w", err)
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

	// Write IDR slice (already has start code from EncodeGreyIDRSliceFromSPSPPS)
	if _, err := f.Write(idrSlice); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Wrote grey IDR %dx%d (%s, %d-bit) to %s\n",
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
