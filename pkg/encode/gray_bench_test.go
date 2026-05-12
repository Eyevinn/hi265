package encode

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
)

// Real-world VPS/SPS/PPS from x265 for 1920x1080 in three formats.
var grayBenchCases = []struct {
	name string
	sps  string
	pps  string
}{
	{
		name: "420_8bit",
		sps:  "4201010408000003009fa8000003000078a003c08010e596ea4932bc05a02000000300200000030321",
		pps:  "4401c172b06240",
	},
	{
		name: "420_10bit",
		sps:  "4201010408000003009da8000003000078a003c08010e4d96ea4932bc05a020000030002000003003210",
		pps:  "4401c172b06240",
	},
	{
		name: "422_10bit",
		sps:  "4201010408000003009d28000003000078b003c08010e4d96ea4932bc05a020000030002000003003210",
		pps:  "4401c172b06240",
	},
}

func parseGrayBenchSPSPPS(tb testing.TB, spsHex, ppsHex string) (*hevc.SPS, *hevc.PPS) {
	tb.Helper()
	spsBytes, _ := hex.DecodeString(spsHex)
	ppsBytes, _ := hex.DecodeString(ppsHex)
	sps, err := hevc.ParseSPSNALUnit(spsBytes)
	if err != nil {
		tb.Fatal(err)
	}
	pps, err := hevc.ParsePPSNALUnit(ppsBytes, map[uint32]*hevc.SPS{uint32(sps.SpsID): sps})
	if err != nil {
		tb.Fatal(err)
	}
	return sps, pps
}

func BenchmarkGrayIDR1920x1080(b *testing.B) {
	for _, tc := range grayBenchCases {
		sps, pps := parseGrayBenchSPSPPS(b, tc.sps, tc.pps)
		b.Run(tc.name, func(b *testing.B) {
			var out []byte
			for i := 0; i < b.N; i++ {
				out, _ = EncodeGrayIDRSliceFromSPSPPS(sps, pps)
			}
			b.ReportMetric(float64(len(out)), "bytes")
		})
	}
}

func TestGrayIDRSize1920x1080(t *testing.T) {
	// x265 IDR slice sizes (from x265 with --no-sao --no-deblock --no-signhide)
	x265Sizes := map[string]int{
		"420_8bit":  610,
		"420_10bit": 610,
		"422_10bit": 637,
	}

	for _, tc := range grayBenchCases {
		t.Run(tc.name, func(t *testing.T) {
			sps, pps := parseGrayBenchSPSPPS(t, tc.sps, tc.pps)
			idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
			if err != nil {
				t.Fatal(err)
			}
			x265Size := x265Sizes[tc.name]
			fmt.Printf("  %-12s hi265gray: %5d bytes   x265: %5d bytes   ratio: %.1fx\n",
				tc.name, len(idr), x265Size, float64(len(idr))/float64(x265Size))
		})
	}
}

// x265-generated SPS/PPS for various resolutions testing boundary CTU handling.
var grayResolutionCases = []struct {
	name    string
	sps     string
	pps     string
	wantLen int
}{
	// CTU=64, no edge remainder
	{"64x64",
		"4201010408000003009fa800000300001ea020810596ea4932bc05a02000000300200000030321",
		"4401c172b02240", 11},
	{"128x128",
		"4201010408000003009fa800000300001ea0102020596ea4932bc05a020000030002000003003210",
		"4401c172b02240", 15},
	{"1920x1088",
		"4201010408000003009fa8000003000078a003c080110596ea4932bc05a02000000300200000030321",
		"4401c172b06240", 164},
	// CTU=64, height remainder only
	{"1280x720",
		"4201010408000003009fa800000300005da00280802d165ba924caf0168080000003008000000c84",
		"4401c172b06240", 107},
	{"1920x1080",
		"4201010408000003009fa8000003000078a003c08010e596ea4932bc05a02000000300200000030321",
		"4401c172b06240", 275},
	{"320x240",
		"4201010408000003009fa800000300003ca00a080f165ba924caf0168080000003008000000c84",
		"4401c172b06240", 35},
	// CTU=64, both width and height remainder
	{"960x540",
		"4201010408000003009fa800000300005aa0078200887de5ba924caf016808000003000800000300c840",
		"4401c172b06240", 64},
	{"640x360",
		"4201010408000003009fa800000300003fa00502016965ba924caf016808000003000800000300c840",
		"4401c172b06240", 66},
	{"1000x500",
		"4201010408000003009fa800000300005aa007d201f9f796ea4932bc05a02000000300200000030321",
		"4401c172b06240", 134},
	{"100x100",
		"4201010408000003009fa800000300001ea03481a77796ea4932bc05a02000000300200000030321",
		"4401c172b02240", 28},
	// CTU=32
	{"48x48",
		"4201010408000003009fa800000300001ea0620c596eae4caf016808000003000800000300c840",
		"4401c173c089", 15},
	{"72x40",
		"4201010408000003009fa800000300001ea02482965bab932bc05a020000030002000003003210",
		"4401c173c189", 20},
}

// TestGrayIDRMultiResolution validates gray IDR encoding at various resolutions
// by checking the output length matches the expected golden value.
// Golden values verified pixel-perfect against FFmpeg decode (all Y=128).
func TestGrayIDRMultiResolution(t *testing.T) {
	for _, tc := range grayResolutionCases {
		t.Run(tc.name, func(t *testing.T) {
			sps, pps := parseGrayBenchSPSPPS(t, tc.sps, tc.pps)
			w := int(sps.PicWidthInLumaSamples)
			h := int(sps.PicHeightInLumaSamples)
			log2MinCb := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
			ctu := 1 << (log2MinCb + int(sps.Log2DiffMaxMinLumaCodingBlockSize))

			idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if len(idr) != tc.wantLen {
				t.Errorf("IDR length = %d, want %d", len(idr), tc.wantLen)
			}
			t.Logf("%4dx%-4d CTU=%d rem=%dx%d IDR=%d bytes",
				w, h, ctu, w%ctu, h%ctu, len(idr))
		})
	}
}
