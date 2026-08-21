// Command hi265dec decodes HEVC/H.265 video to raw frames or images.
//
// Usage:
//
//	hi265dec [flags] input.265
//	hi265dec input.mp4 -o thumb.png
//
// Flags may appear before or after the input path.
//
// Supported input: Annex-B byte stream (.265, .hevc, .h265) and MP4
// (.mp4, .m4v), both progressive and fragmented.
// Supported output: raw YUV 4:2:0 (.yuv), Y4M (.y4m), PNG (.png), JPEG (.jpg).
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	h264frame "github.com/Eyevinn/hi264/pkg/frame"
	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/Eyevinn/hi265/internal"
	"github.com/Eyevinn/hi265/pkg/decoder"
	"github.com/Eyevinn/hi265/pkg/frame"
)

const appName = "hi265dec"

type options struct {
	outPath    string
	numFrames  int
	jpegQual   int
	colorspace string
	fullRange  bool
	version    bool
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var opts options
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	fs.StringVar(&opts.outPath, "o", "", "output file (.yuv, .y4m, .png, .jpg)")
	fs.IntVar(&opts.numFrames, "n", 0, "number of frames to write (0 = all)")
	fs.IntVar(&opts.jpegQual, "q", 85, "JPEG quality (1-100)")
	fs.StringVar(&opts.colorspace, "colorspace", "bt601", "color space for RGB conversion (bt601, bt709, bt2020)")
	fs.BoolVar(&opts.fullRange, "full-range", false, "treat input as full-range YCbCr (0-255)")
	fs.BoolVar(&opts.version, "version", false, "print version")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s [flags] input.265 [output]\n\nFlags:\n", appName)
		fs.PrintDefaults()
	}

	positional, err := parseInterleaved(fs, args[1:])
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
	// The output path may be given either with -o or as a second positional,
	// matching hi264dec.
	var inPath string
	switch len(positional) {
	case 1:
		inPath = positional[0]
	case 2:
		if opts.outPath != "" {
			return fmt.Errorf("output given twice: -o %s and positional %s",
				opts.outPath, positional[1])
		}
		inPath, opts.outPath = positional[0], positional[1]
	case 0:
		fs.Usage()
		return errors.New("no input file given")
	default:
		return fmt.Errorf("expected at most two paths, got %d: %v", len(positional), positional)
	}

	if opts.numFrames < 0 {
		return fmt.Errorf("-n must be non-negative, got %d", opts.numFrames)
	}
	cs, err := yuv.ParseColorSpace(opts.colorspace)
	if err != nil {
		return err
	}
	rng := yuv.LimitedRange
	if opts.fullRange {
		rng = yuv.FullRange
	}

	var frames []*frame.Frame
	if isMP4(inPath) {
		frames, err = decodeMP4(inPath, opts.numFrames)
	} else {
		frames, err = decodeAnnexB(inPath, opts.numFrames)
	}
	if err != nil {
		return err
	}

	fmt.Printf("Decoded %d frame(s), %dx%d\n", len(frames), frames[0].Width, frames[0].Height)

	out := opts.outPath
	if out == "" {
		out = strings.TrimSuffix(inPath, filepath.Ext(inPath)) + ".yuv"
	}
	return writeFrames(frames, out, &opts, cs, rng)
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

func isMP4(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".m4s", ".cmfv":
		return true
	}
	return false
}

// truncate limits frames to n entries (n == 0 means all).
func truncate(frames []*frame.Frame, n int) []*frame.Frame {
	if n > 0 && len(frames) > n {
		return frames[:n]
	}
	return frames
}

func decodeAnnexB(path string, n int) ([]*frame.Frame, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	dec := decoder.New()
	frames, err := dec.DecodeAnnexB(data)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return truncate(frames, n), nil
}

// decodeMP4 decodes the first video track of a progressive or fragmented MP4.
// Samples are decoded in presentation order so that P-skip frames resolve
// against their reference picture; parameter sets come from the hvcC box.
func decodeMP4(path string, n int) ([]*frame.Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	mp4File, err := mp4.DecodeFile(f, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("decode mp4: %w", err)
	}
	if mp4File.Moov == nil {
		return nil, errors.New("no moov box found")
	}

	var videoTrack *mp4.TrakBox
	for _, trak := range mp4File.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrack = trak
			break
		}
	}
	if videoTrack == nil {
		return nil, errors.New("no video track found")
	}

	paramSets, err := hvcCParamSets(videoTrack)
	if err != nil {
		return nil, err
	}

	dec := decoder.New()
	if nrSamples := videoTrack.GetNrSamples(); nrSamples > 0 {
		return decodeProgressive(mp4File, videoTrack, f, paramSets, dec, n)
	}
	if len(mp4File.Segments) > 0 {
		return decodeFragmented(mp4File, videoTrack, f, paramSets, dec, n)
	}
	return nil, errors.New("no samples found (neither progressive nor fragmented)")
}

// hvcCParamSets returns the VPS, SPS and PPS NALUs carried in the track's hvcC box.
func hvcCParamSets(videoTrack *mp4.TrakBox) ([][]byte, error) {
	stsd := videoTrack.Mdia.Minf.Stbl.Stsd
	if stsd.HvcX == nil {
		return nil, errors.New("no HEVC sample entry found (not an HEVC track)")
	}
	hvcC := stsd.HvcX.HvcC
	if hvcC == nil {
		return nil, errors.New("no hvcC box found")
	}
	var nalus [][]byte
	for _, t := range []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS} {
		nalus = append(nalus, hvcC.GetNalusForType(t)...)
	}
	if len(nalus) == 0 {
		return nil, errors.New("hvcC carries no parameter sets (hev1 with in-band sets is not supported)")
	}
	return nalus, nil
}

func decodeProgressive(mp4File *mp4.File, videoTrack *mp4.TrakBox, f *os.File,
	paramSets [][]byte, dec *decoder.Decoder, n int) ([]*frame.Frame, error) {

	nrSamples := videoTrack.GetNrSamples()
	var frames []*frame.Frame
	for sampleNr := uint32(1); sampleNr <= nrSamples; sampleNr++ {
		ranges, err := videoTrack.GetRangesForSampleInterval(sampleNr, sampleNr)
		if err != nil {
			return nil, fmt.Errorf("sample %d range: %w", sampleNr, err)
		}
		var sampleData []byte
		for _, dr := range ranges {
			data, err := mp4File.Mdat.ReadData(int64(dr.Offset), int64(dr.Size), f)
			if err != nil {
				return nil, fmt.Errorf("read mdat for sample %d: %w", sampleNr, err)
			}
			sampleData = append(sampleData, data...)
		}
		got, err := decodeSample(sampleData, paramSets, dec, sampleNr == 1)
		if err != nil {
			return nil, fmt.Errorf("sample %d: %w", sampleNr, err)
		}
		frames = append(frames, got...)
		if n > 0 && len(frames) >= n {
			break
		}
	}
	if len(frames) == 0 {
		return nil, errors.New("no frames decoded")
	}
	return truncate(frames, n), nil
}

func decodeFragmented(mp4File *mp4.File, videoTrack *mp4.TrakBox, f *os.File,
	paramSets [][]byte, dec *decoder.Decoder, n int) ([]*frame.Frame, error) {

	trackID := videoTrack.Tkhd.TrackID
	var trex *mp4.TrexBox
	if mp4File.Moov.Mvex != nil {
		trex, _ = mp4File.Moov.Mvex.GetTrex(trackID)
	}

	var frames []*frame.Frame
	first := true
	sampleNr := uint32(0)
	for _, seg := range mp4File.Segments {
		for _, frag := range seg.Fragments {
			for _, traf := range frag.Moof.Trafs {
				if traf.Tfhd.TrackID != trackID {
					continue
				}
				for _, trun := range traf.Truns {
					trun.AddSampleDefaultValues(traf.Tfhd, trex)

					baseOffset := frag.Moof.StartPos
					if traf.Tfhd.HasBaseDataOffset() {
						baseOffset = traf.Tfhd.BaseDataOffset
					}
					if trun.HasDataOffset() {
						baseOffset = uint64(int64(baseOffset) + int64(trun.DataOffset))
					}

					sampleOffset := uint64(0)
					for i := uint32(0); i < trun.SampleCount(); i++ {
						sampleNr++
						sample := trun.Samples[i]
						data := make([]byte, sample.Size)
						if _, err := f.ReadAt(data, int64(baseOffset+sampleOffset)); err != nil {
							return nil, fmt.Errorf("read fragment sample %d: %w", sampleNr, err)
						}
						sampleOffset += uint64(sample.Size)

						got, err := decodeSample(data, paramSets, dec, first)
						if err != nil {
							return nil, fmt.Errorf("fragment sample %d: %w", sampleNr, err)
						}
						first = false
						frames = append(frames, got...)
						if n > 0 && len(frames) >= n {
							return truncate(frames, n), nil
						}
					}
				}
			}
		}
	}
	if len(frames) == 0 {
		return nil, errors.New("no frames decoded")
	}
	return truncate(frames, n), nil
}

// decodeSample decodes one length-prefixed MP4 sample. The hvcC parameter sets
// are prepended to the first sample only; the decoder caches them afterwards.
func decodeSample(sampleData []byte, paramSets [][]byte,
	dec *decoder.Decoder, withParamSets bool) ([]*frame.Frame, error) {

	sampleNALUs, err := avc.GetNalusFromSample(sampleData)
	if err != nil {
		return nil, fmt.Errorf("split sample into NALUs: %w", err)
	}
	nalus := sampleNALUs
	if withParamSets {
		nalus = append(append([][]byte{}, paramSets...), sampleNALUs...)
	}
	return dec.DecodeNALUs(nalus)
}

func writeFrames(frames []*frame.Frame, out string, opts *options,
	cs yuv.ColorSpace, rng yuv.Range) error {

	switch strings.ToLower(filepath.Ext(out)) {
	case ".yuv":
		return writeYUV(frames, out)
	case ".y4m":
		return writeY4M(frames, out, cs, rng)
	case ".png", ".jpg", ".jpeg":
		return writeImages(frames, out, opts, cs, rng)
	default:
		return fmt.Errorf("unsupported output format %q (use .yuv, .y4m, .png or .jpg)",
			filepath.Ext(out))
	}
}

func writeYUV(frames []*frame.Frame, out string) error {
	var data []byte
	for _, fr := range frames {
		data = append(data, fr.YUV420Bytes()...)
	}
	if err := os.WriteFile(out, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	fmt.Printf("Written %s (%d frame(s))\n", out, len(frames))
	return nil
}

func writeY4M(frames []*frame.Frame, out string, cs yuv.ColorSpace, rng yuv.Range) error {
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create %s: %w", out, err)
	}
	defer f.Close()

	if err := yuv.WriteY4MHeaderCS(f, frames[0].Width, frames[0].Height, cs, rng); err != nil {
		return fmt.Errorf("write y4m header: %w", err)
	}
	for i, fr := range frames {
		if err := yuv.WriteY4MFrame(f, toYUVFrame(fr)); err != nil {
			return fmt.Errorf("write y4m frame %d: %w", i, err)
		}
	}
	fmt.Printf("Written %s (%d frame(s))\n", out, len(frames))
	return nil
}

// writeImages writes one file per frame. A single frame keeps the given path;
// several frames get a zero-padded index inserted before the extension.
func writeImages(frames []*frame.Frame, out string, opts *options,
	cs yuv.ColorSpace, rng yuv.Range) error {

	isJPEG := strings.EqualFold(filepath.Ext(out), ".jpg") ||
		strings.EqualFold(filepath.Ext(out), ".jpeg")

	for i, fr := range frames {
		path := out
		if len(frames) > 1 {
			path = numberedPath(out, i)
		}
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if isJPEG {
			err = yuv.WriteJPEGCS(f, toYUVFrame(fr), opts.jpegQual, cs, rng)
		} else {
			err = yuv.WritePNGCS(f, toYUVFrame(fr), cs, rng)
		}
		closeErr := f.Close()
		if err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", path, closeErr)
		}
		fmt.Printf("Written %s\n", path)
	}
	return nil
}

// numberedPath turns "frames.png" into "frames_0000.png".
func numberedPath(base string, index int) string {
	ext := filepath.Ext(base)
	return fmt.Sprintf("%s_%04d%s", strings.TrimSuffix(base, ext), index, ext)
}

// toYUVFrame adapts a decoded hi265 frame to the frame type used by hi264's
// yuv writers. The planes are shared, not copied.
func toYUVFrame(f *frame.Frame) *h264frame.Frame {
	return &h264frame.Frame{
		Width:   f.Width,
		Height:  f.Height,
		Y:       f.Y,
		Cb:      f.Cb,
		Cr:      f.Cr,
		StrideY: f.StrideY,
		StrideC: f.StrideC,
	}
}
