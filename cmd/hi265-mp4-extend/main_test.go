package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/Eyevinn/hi265/pkg/decoder"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// makeInitAndSegment generates a fragmented MP4 with hi265gen and splits it
// into an init segment and a media segment, the two inputs this tool takes.
func makeInitAndSegment(t *testing.T, dir string, frames int) (initPath, segPath string) {
	t.Helper()

	src := filepath.Join(dir, "src.mp4")
	cmd := exec.Command("go", "run", "../hi265gen",
		"-smpte", "-w", "192", "-h", "96", "-n", strconv.Itoa(frames), "-o", src)
	cmd.Dir = mustWD(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not generate source mp4 (%v): %s", err, out)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read generated mp4: %v", err)
	}
	parsed, err := mp4.DecodeFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse generated mp4: %v", err)
	}
	if parsed.Init == nil || len(parsed.Segments) == 0 {
		t.Fatalf("generated mp4 is not fragmented (init=%v segments=%d)",
			parsed.Init != nil, len(parsed.Segments))
	}

	initPath = filepath.Join(dir, "init.mp4")
	initFile, err := os.Create(initPath)
	if err != nil {
		t.Fatalf("create init: %v", err)
	}
	if err := parsed.Init.Encode(initFile); err != nil {
		t.Fatalf("write init: %v", err)
	}
	initFile.Close()

	segPath = filepath.Join(dir, "in.m4s")
	segFile, err := os.Create(segPath)
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}
	if err := parsed.Segments[0].Encode(segFile); err != nil {
		t.Fatalf("write segment: %v", err)
	}
	segFile.Close()

	return initPath, segPath
}

func mustWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// decodeSegment decodes init + media segment into frames.
func decodeSegment(t *testing.T, initPath, segPath string) []*frame.Frame {
	t.Helper()

	initData, err := os.ReadFile(initPath)
	if err != nil {
		t.Fatalf("read init: %v", err)
	}
	initParsed, err := mp4.DecodeFile(bytes.NewReader(initData))
	if err != nil {
		t.Fatalf("parse init: %v", err)
	}
	paramSets, err := hvcCParamSets(initParsed.Init)
	if err != nil {
		t.Fatalf("param sets: %v", err)
	}

	segData, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	segParsed, err := mp4.DecodeFile(bytes.NewReader(segData))
	if err != nil {
		t.Fatalf("parse segment: %v", err)
	}
	samples, err := allSamples(segParsed)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}

	var annexB bytes.Buffer
	annexB.Write(annexBParameterSets(paramSets))
	for _, s := range samples {
		annexB.Write(avc.ConvertSampleToByteStream(append([]byte(nil), s.Data...)))
	}
	frames, err := decoder.New().DecodeAnnexB(annexB.Bytes())
	if err != nil {
		t.Fatalf("decode extended segment: %v", err)
	}
	return frames
}

func TestExtendSegmentFreezes(t *testing.T) {
	dir := t.TempDir()
	const srcFrames = 3
	const appended = 4

	initPath, segPath := makeInitAndSegment(t, dir, srcFrames)
	outPath := filepath.Join(dir, "out.m4s")

	if err := extendSegment(initPath, segPath, outPath,
		&options{frames: appended}); err != nil {
		t.Fatalf("extendSegment: %v", err)
	}

	frames := decodeSegment(t, initPath, outPath)
	if len(frames) != srcFrames+appended {
		t.Fatalf("decoded %d frames, want %d", len(frames), srcFrames+appended)
	}
	// Every appended frame must equal the source's last picture.
	want := frames[srcFrames-1].YUV420Bytes()
	for i := srcFrames; i < len(frames); i++ {
		if !bytes.Equal(frames[i].YUV420Bytes(), want) {
			t.Errorf("appended frame %d is not a copy of the source's last picture", i)
		}
	}
}

func TestExtendSegmentGrayIDR(t *testing.T) {
	dir := t.TempDir()
	const srcFrames = 2
	const appended = 3

	initPath, segPath := makeInitAndSegment(t, dir, srcFrames)
	outPath := filepath.Join(dir, "out.m4s")

	if err := extendSegment(initPath, segPath, outPath,
		&options{frames: appended, grayIDR: true}); err != nil {
		t.Fatalf("extendSegment: %v", err)
	}

	frames := decodeSegment(t, initPath, outPath)
	if len(frames) != srcFrames+appended {
		t.Fatalf("decoded %d frames, want %d", len(frames), srcFrames+appended)
	}
	// The gray IDR and the P-skip copies after it are all mid-gray.
	for i := srcFrames; i < len(frames); i++ {
		f := frames[i]
		for y := 0; y < f.Height; y++ {
			for x := 0; x < f.Width; x++ {
				if got := f.Y[y*f.StrideY+x]; got != 128 {
					t.Fatalf("frame %d luma at (%d,%d) = %d, want 128", i, x, y, got)
				}
			}
		}
	}

	// The output must mark the gray IDR as a sync sample.
	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	outParsed, err := mp4.DecodeFile(bytes.NewReader(outData))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	samples, err := allSamples(outParsed)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if len(samples) != srcFrames+appended {
		t.Fatalf("output has %d samples, want %d", len(samples), srcFrames+appended)
	}
	if samples[srcFrames].Flags != mp4.SyncSampleFlags {
		t.Errorf("appended IDR flags = %#x, want SyncSampleFlags %#x",
			samples[srcFrames].Flags, mp4.SyncSampleFlags)
	}
}

func TestExtendSegmentTimingContinues(t *testing.T) {
	dir := t.TempDir()
	initPath, segPath := makeInitAndSegment(t, dir, 3)
	outPath := filepath.Join(dir, "out.m4s")

	if err := extendSegment(initPath, segPath, outPath,
		&options{frames: 2}); err != nil {
		t.Fatalf("extendSegment: %v", err)
	}

	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	outParsed, err := mp4.DecodeFile(bytes.NewReader(outData))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	samples, err := allSamples(outParsed)
	if err != nil {
		t.Fatalf("samples: %v", err)
	}

	// Decode times must be contiguous at a constant duration.
	for i := 1; i < len(samples); i++ {
		wantDT := samples[i-1].DecodeTime + uint64(samples[i-1].Dur)
		if samples[i].DecodeTime != wantDT {
			t.Errorf("sample %d decode time = %d, want %d",
				i, samples[i].DecodeTime, wantDT)
		}
		if samples[i].Dur != samples[0].Dur {
			t.Errorf("sample %d duration = %d, want %d",
				i, samples[i].Dur, samples[0].Dur)
		}
	}
}

func TestRunArgValidation(t *testing.T) {
	if err := run([]string{appName, "-frames", "0", "a", "b", "c"}); err == nil {
		t.Error("expected an error for -frames 0")
	}
	if err := run([]string{appName, "-frames", "2", "only-one-arg"}); err == nil {
		t.Error("expected an error for too few positional arguments")
	}
}
