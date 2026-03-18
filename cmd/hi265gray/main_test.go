package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

func TestGrayIDR422_10bit(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_422_10bit.json")
}

func TestGrayIDR420_10bit(t *testing.T) {
	testGrayIDR(t, "testdata/vps_sps_pps_420_10bit.json")
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
