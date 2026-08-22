package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

var paramFiles = []string{
	"testdata/vps_sps_pps_422_10bit.json",
	"testdata/vps_sps_pps_420_10bit.json",
	"testdata/vps_sps_pps_420_8bit.json",
	// x265 at its defaults, which means entropy_coding_sync_enabled_flag is set.
	// This is the shape a GDR refresh actually has to be spliced into, and the
	// one the gray encoder used to refuse: the slice data needs one CABAC
	// substream per CTB row with entry point offsets to match.
	"testdata/vps_sps_pps_420_8bit_wpp.json",
}

func TestGrayIDR422_10bit(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_422_10bit.json")
}

func TestGrayIDR420_10bit(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_420_10bit.json")
}

func TestGrayIDR420_8bitWpp(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_420_8bit_wpp.json")
}

func TestGrayIDR420_8bit(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_420_8bit.json")
}

func testGrayIDR(t *testing.T, paramFile string) {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "gray.265")
	err := run([]string{"hi265gray", "-f", paramFile, "-o", outFile})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	nalus := avc.ExtractNalusFromByteStream(data)
	if len(nalus) < 4 {
		t.Fatalf("expected at least 4 NALUs (VPS+SPS+PPS+IDR), got %d", len(nalus))
	}

	want := []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS, hevc.NALU_IDR_W_RADL}
	for i, w := range want {
		got := hevc.GetNaluType(nalus[i][0])
		if got != w {
			t.Errorf("NALU[%d]: got type %d, want %d", i, got, w)
		}
	}
}

// TestGrayCRA checks that -cra emits a CRA slice (nal_unit_type 21) for every
// supported chroma format and bit depth.
func TestGrayCRA(t *testing.T) {
	for _, paramFile := range paramFiles {
		t.Run(paramFile, func(t *testing.T) {
			outFile := filepath.Join(t.TempDir(), "gray_cra.265")
			if err := run([]string{"hi265gray", "-f", paramFile, "-cra", "-poc", "42",
				"-o", outFile}); err != nil {
				t.Fatalf("run: %v", err)
			}

			data, err := os.ReadFile(outFile)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}

			nalus := avc.ExtractNalusFromByteStream(data)
			if len(nalus) < 4 {
				t.Fatalf("expected at least 4 NALUs (VPS+SPS+PPS+CRA), got %d", len(nalus))
			}
			want := []hevc.NaluType{hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS, hevc.NALU_CRA}
			for i, w := range want {
				if got := hevc.GetNaluType(nalus[i][0]); got != w {
					t.Errorf("NALU[%d]: got type %s, want %s", i, got, w)
				}
			}
		})
	}
}

// TestPocRequiresCRA checks that -poc without -cra is rejected: an IDR always has
// POC 0.
func TestPocRequiresCRA(t *testing.T) {
	outFile := filepath.Join(t.TempDir(), "gray.265")
	err := run([]string{"hi265gray", "-f", paramFiles[0], "-poc", "42", "-o", outFile})
	if err == nil {
		t.Fatal("expected an error for -poc without -cra")
	}
	if !strings.Contains(err.Error(), "-cra") {
		t.Errorf("error %q does not mention -cra", err)
	}
}

// TestGrayCRAFFmpeg checks that FFmpeg decodes the generated CRA frame to uniform
// mid-gray for every chroma format and bit depth, and that mp4ff-nallister reports
// the slice as RAP_CRA_21.
func TestGrayCRAFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}

	for _, paramFile := range paramFiles {
		t.Run(paramFile, func(t *testing.T) {
			_, spsNALU, _, err := readParamSetsFromFile(paramFile)
			if err != nil {
				t.Fatalf("readParamSetsFromFile: %v", err)
			}
			sps, err := hevc.ParseSPSNALUnit(spsNALU)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			w := int(sps.PicWidthInLumaSamples)
			h := int(sps.PicHeightInLumaSamples)
			bitDepth := 8 + int(sps.BitDepthLumaMinus8)
			pixFmt, numSamples, err := rawVideoFormat(int(sps.ChromaFormatIDC), bitDepth, w, h)
			if err != nil {
				t.Fatalf("%v", err)
			}

			dir := t.TempDir()
			inPath := filepath.Join(dir, "gray_cra.265")
			outPath := filepath.Join(dir, "gray_cra.yuv")
			if err := run([]string{"hi265gray", "-f", paramFile, "-cra", "-poc", "42",
				"-o", inPath}); err != nil {
				t.Fatalf("run: %v", err)
			}

			cmd := exec.Command(ffmpegPath, "-v", "error", "-i", inPath,
				"-f", "rawvideo", "-pix_fmt", pixFmt, outPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, out)
			}

			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read ffmpeg output: %v", err)
			}
			bytesPerSample := 1
			if bitDepth > 8 {
				bytesPerSample = 2
			}
			if want := numSamples * bytesPerSample; len(data) != want {
				t.Fatalf("ffmpeg output is %d bytes, want %d (one %dx%d %s frame)",
					len(data), want, w, h, pixFmt)
			}
			midGray := 1 << (bitDepth - 1)
			for i := 0; i < len(data); i += bytesPerSample {
				got := int(data[i])
				if bytesPerSample == 2 {
					got = int(binary.LittleEndian.Uint16(data[i : i+2]))
				}
				if got != midGray {
					t.Fatalf("ffmpeg output sample %d = %d, want %d (mid-gray)",
						i/bytesPerSample, got, midGray)
				}
			}

			nallister, err := exec.LookPath("mp4ff-nallister")
			if err != nil {
				t.Log("mp4ff-nallister not in PATH, skipping NALU type check")
				return
			}
			out, err := exec.Command(nallister, "-annexb", "-c", "hevc", inPath).CombinedOutput()
			if err != nil {
				t.Fatalf("mp4ff-nallister: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "RAP_CRA_21") {
				t.Errorf("mp4ff-nallister output does not mention RAP_CRA_21:\n%s", out)
			}
			t.Logf("mp4ff-nallister: %s", strings.TrimSpace(string(out)))
		})
	}
}

// rawVideoFormat returns the FFmpeg rawvideo pixel format and the number of
// samples in one frame for the given chroma format, bit depth and size.
func rawVideoFormat(chromaFormatIDC, bitDepth, w, h int) (pixFmt string, numSamples int, err error) {
	switch chromaFormatIDC {
	case 1:
		pixFmt, numSamples = "yuv420p", w*h*3/2
	case 2:
		pixFmt, numSamples = "yuv422p", w*h*2
	case 3:
		pixFmt, numSamples = "yuv444p", w*h*3
	default:
		return "", 0, fmt.Errorf("unsupported chroma_format_idc %d", chromaFormatIDC)
	}
	if bitDepth > 8 {
		pixFmt = fmt.Sprintf("%s%dle", pixFmt, bitDepth)
	}
	return pixFmt, numSamples, nil
}
