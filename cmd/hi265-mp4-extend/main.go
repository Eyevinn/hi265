// Command hi265-mp4-extend extends a fragmented MP4 (CMAF) media segment with
// empty frames, reusing the source's parameter sets verbatim so the result
// splices cleanly.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/Eyevinn/hi265/internal"
	"github.com/Eyevinn/hi265/pkg/encode"
	"github.com/Eyevinn/hi265/pkg/timecode"
)

const appName = "hi265-mp4-extend"

const usage = `%s — extend a fragmented MP4 (CMAF) media segment with empty frames.

Reads <init.mp4> for the parameter sets and frame dimensions, reads <in.m4s>
for the existing samples and timing, and writes <out.m4s> as a single fragment
holding all input samples followed by N appended frames at the same per-sample
duration.

By default the appended frames are P-skip copies of the source's last
reference picture (a freeze). With -gray-idr the first appended frame is a
mid-gray IDR and the rest are P-skip copies of it; note that an IDR resets POC
to 0 for everything after it. With -gray-cra the refresh picture is a CRA
instead, which carries slice_pic_order_cnt_lsb and so continues the source's
POC — the right choice for splicing a refresh point into a running stream, and
something H.264 cannot express at all.

The appended slice headers reuse the source's SPS and PPS verbatim, so the
output decodes with no parameter-set change — which a normal encoder run
cannot do. The result is a self-contained media segment to be played next to
the init segment:

    cat init.mp4 out.m4s | ffplay -i -

Usage:
  %s -frames N [-gray-idr|-gray-cra] [-timecode] <init.mp4> <in.m4s> <out.m4s>

Flags:
`

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	frames   int
	grayIDR  bool
	grayCRA  bool
	timeCode bool
	version  bool
}

func run(args []string) error {
	var opts options
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	fs.IntVar(&opts.frames, "frames", 0, "number of frames to append (required)")
	fs.BoolVar(&opts.grayIDR, "gray-idr", false,
		"start the appended span with a mid-gray IDR (resets POC to 0)")
	fs.BoolVar(&opts.grayCRA, "gray-cra", false,
		"start the appended span with a mid-gray CRA, continuing the source POC")
	fs.BoolVar(&opts.timeCode, "timecode", false,
		"attach a Time Code SEI (payload type 136) to each appended frame")
	fs.BoolVar(&opts.version, "version", false, "print version")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), usage, appName, appName)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.version {
		fmt.Printf("%s %s\n", appName, internal.GetVersion())
		return nil
	}
	if fs.NArg() != 3 {
		fs.Usage()
		return fmt.Errorf("expected 3 positional arguments, got %d", fs.NArg())
	}
	if opts.frames <= 0 {
		return errors.New("-frames must be a positive integer")
	}
	if opts.grayIDR && opts.grayCRA {
		return errors.New("-gray-idr and -gray-cra are mutually exclusive")
	}
	return extendSegment(fs.Arg(0), fs.Arg(1), fs.Arg(2), &opts)
}

func extendSegment(initPath, inSegPath, outSegPath string, opts *options) error {
	initParsed, err := decodeFile(initPath)
	if err != nil {
		return fmt.Errorf("read init segment: %w", err)
	}
	if initParsed.Init == nil {
		return errors.New("init segment missing (no ftyp+moov)")
	}
	paramSets, err := hvcCParamSets(initParsed.Init)
	if err != nil {
		return fmt.Errorf("read parameter sets from init: %w", err)
	}

	segParsed, err := decodeFile(inSegPath)
	if err != nil {
		return fmt.Errorf("read media segment: %w", err)
	}
	if len(segParsed.Segments) == 0 {
		return errors.New("input segment has no fragments (no moof+mdat)")
	}

	inputSamples, err := allSamples(segParsed)
	if err != nil {
		return err
	}
	if len(inputSamples) == 0 {
		return errors.New("input segment has no samples")
	}

	// Reassemble the input as Annex-B so LastFrameState can resolve slice
	// headers. ConvertSampleToByteStream rewrites the 4-byte length prefixes
	// in place, so it must be given a copy — otherwise it corrupts the parsed
	// mdat buffers we copy back into the output.
	var annexB bytes.Buffer
	annexB.Write(annexBParameterSets(paramSets))
	for _, s := range inputSamples {
		annexB.Write(avc.ConvertSampleToByteStream(append([]byte(nil), s.Data...)))
	}

	state, err := encode.LastFrameState(annexB.Bytes())
	if err != nil {
		return fmt.Errorf("inspect input tail: %w", err)
	}

	sampleDur := lastSampleDuration(inputSamples)
	if sampleDur == 0 {
		return errors.New("could not determine sample duration from input segment")
	}
	timescale := mediaTimescale(initParsed.Init)
	nextDecodeTime := nextDecodeTimeAfter(inputSamples)

	newSamples := make([]mp4.FullSample, 0, opts.frames)
	poc := state.POC
	remaining := opts.frames

	if opts.grayIDR || opts.grayCRA {
		// The gray encoder is chroma-format and bit-depth agnostic, so this
		// works for 4:2:0/4:2:2/4:4:4 at 8/10/12 bits.
		var refresh []byte
		if opts.grayCRA {
			// A CRA carries slice_pic_order_cnt_lsb, so it continues the
			// source's POC instead of resetting it.
			refresh, err = encode.EncodeGrayCRASliceFromSPSPPS(state.SPS, state.PPS, poc+1)
		} else {
			refresh, err = encode.EncodeGrayIDRSliceFromSPSPPS(state.SPS, state.PPS)
		}
		if err != nil {
			return fmt.Errorf("encode gray refresh picture: %w", err)
		}
		if refresh, err = maybePrependTimeCode(opts, refresh, nextDecodeTime, sampleDur, timescale); err != nil {
			return fmt.Errorf("gray refresh timecode: %w", err)
		}
		newSamples = append(newSamples,
			fullSample(refresh, mp4.SyncSampleFlags, sampleDur, nextDecodeTime))
		nextDecodeTime += uint64(sampleDur)
		if opts.grayCRA {
			poc++ // the CRA occupies the next POC; trailing pictures continue from it
		} else {
			poc = 0 // an IDR resets POC
		}
		remaining--
	}

	for i := 1; i <= remaining; i++ {
		// HEVC counts pictures, so POC advances by one per appended picture.
		slice, err := encode.EncodePSkipSliceFromSPSPPS(state.SPS, state.PPS, poc+i)
		if err != nil {
			return fmt.Errorf("encode P-skip %d: %w", i, err)
		}
		if slice, err = maybePrependTimeCode(opts, slice, nextDecodeTime, sampleDur, timescale); err != nil {
			return fmt.Errorf("timecode for P-skip %d: %w", i, err)
		}
		newSamples = append(newSamples,
			fullSample(slice, mp4.NonSyncSampleFlags, sampleDur, nextDecodeTime))
		nextDecodeTime += uint64(sampleDur)
	}

	if err := writeSegment(outSegPath, initParsed.Init, segParsed, inputSamples, newSamples); err != nil {
		return err
	}

	mode := "P-skip"
	switch {
	case opts.grayIDR:
		mode = "gray IDR + P-skip"
	case opts.grayCRA:
		mode = "gray CRA + P-skip"
	}
	fmt.Printf("appended %d sample(s) (%s, dur=%d each) to %d input sample(s) -> %s\n",
		len(newSamples), mode, sampleDur, len(inputSamples), outSegPath)
	return nil
}

func fullSample(annexB []byte, flags, dur uint32, decodeTime uint64) mp4.FullSample {
	nalu := avc.ConvertByteStreamToNaluSample(annexB)
	return mp4.FullSample{
		Sample: mp4.Sample{
			Flags: flags,
			Dur:   dur,
			Size:  uint32(len(nalu)),
		},
		DecodeTime: decodeTime,
		Data:       nalu,
	}
}

func writeSegment(outPath string, init *mp4.InitSegment, segParsed *mp4.File,
	inputSamples, newSamples []mp4.FullSample) error {

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer out.Close()

	frag, err := mp4.CreateFragment(firstSeqNum(segParsed), defaultTrackID(init))
	if err != nil {
		return fmt.Errorf("create fragment: %w", err)
	}
	for _, s := range inputSamples {
		frag.AddFullSample(s)
	}
	for _, s := range newSamples {
		frag.AddFullSample(s)
	}
	seg := mp4.NewMediaSegment()
	seg.AddFragment(frag)
	if err := seg.Encode(out); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

// maybePrependTimeCode attaches a Time Code SEI derived from the sample's
// decode time. HEVC carries the timecode in SEI 136, which — unlike H.264
// pic_timing — has no VUI prerequisite, so this works against any source.
func maybePrependTimeCode(opts *options, sliceAnnexB []byte,
	decodeTime uint64, sampleDur, timescale uint32) ([]byte, error) {

	if !opts.timeCode || sampleDur == 0 || timescale == 0 {
		return sliceAnnexB, nil
	}
	fps := int((timescale + sampleDur/2) / sampleDur)
	if fps <= 0 {
		return sliceAnnexB, nil
	}
	frameIdx := int64(decodeTime / uint64(sampleDur))
	h, m, s, f, _ := timecode.Components(frameIdx, fps, false)
	sei, err := encode.GenerateTimeCodeSEI(encode.TimeCode{
		Hours:   uint8(h),
		Minutes: uint8(m),
		Seconds: uint8(s),
		Frames:  uint16(f),
	})
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), sei...), sliceAnnexB...), nil
}

// decodeFile reads an MP4 fully into memory before parsing, so later walks of
// the parsed structure never touch a closed file.
func decodeFile(path string) (*mp4.File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return mp4.DecodeFile(bytes.NewReader(data))
}

// hvcCParamSets returns the VPS, SPS and PPS NALUs of the first HEVC track.
func hvcCParamSets(init *mp4.InitSegment) ([][]byte, error) {
	if init.Moov == nil {
		return nil, errors.New("no moov in init segment")
	}
	for _, trak := range init.Moov.Traks {
		stsd := trak.Mdia.Minf.Stbl.Stsd
		for _, child := range stsd.Children {
			e, ok := child.(*mp4.VisualSampleEntryBox)
			if !ok || e.HvcC == nil {
				continue
			}
			var nalus [][]byte
			for _, t := range []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS} {
				nalus = append(nalus, e.HvcC.GetNalusForType(t)...)
			}
			if len(nalus) == 0 {
				return nil, errors.New("hvcC carries no parameter sets")
			}
			return nalus, nil
		}
	}
	return nil, errors.New("no HEVC video track found")
}

func annexBParameterSets(paramSets [][]byte) []byte {
	var out bytes.Buffer
	for _, n := range paramSets {
		out.Write([]byte{0, 0, 0, 1})
		out.Write(n)
	}
	return out.Bytes()
}

func allSamples(file *mp4.File) ([]mp4.FullSample, error) {
	var samples []mp4.FullSample
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			got, err := frag.GetFullSamples(nil)
			if err != nil {
				return nil, fmt.Errorf("read input samples: %w", err)
			}
			samples = append(samples, got...)
		}
	}
	return samples, nil
}

func lastSampleDuration(samples []mp4.FullSample) uint32 {
	var dur uint32
	for _, s := range samples {
		if s.Dur != 0 {
			dur = s.Dur
		}
	}
	return dur
}

func nextDecodeTimeAfter(samples []mp4.FullSample) uint64 {
	if len(samples) == 0 {
		return 0
	}
	last := samples[len(samples)-1]
	return last.DecodeTime + uint64(last.Dur)
}

// firstSeqNum keeps the output's fragment identity the same as the input's.
func firstSeqNum(file *mp4.File) uint32 {
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof != nil && frag.Moof.Mfhd != nil {
				return frag.Moof.Mfhd.SequenceNumber
			}
		}
	}
	return 1
}

func defaultTrackID(init *mp4.InitSegment) uint32 {
	if init == nil || init.Moov == nil {
		return 1
	}
	for _, t := range init.Moov.Traks {
		return t.Tkhd.TrackID
	}
	return 1
}

func mediaTimescale(init *mp4.InitSegment) uint32 {
	if init == nil || init.Moov == nil {
		return 0
	}
	for _, t := range init.Moov.Traks {
		if t.Mdia != nil && t.Mdia.Mdhd != nil {
			return t.Mdia.Mdhd.Timescale
		}
	}
	return 0
}
