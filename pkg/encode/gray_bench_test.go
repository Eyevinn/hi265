package encode

import (
	"encoding/hex"
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

// These 1920x1080 parameter sets come from x265 at its defaults, which means
// wavefront parallel processing is on: the slice data is one CABAC substream per
// CTB row, each ended by an end_of_subset_one_bit and byte alignment, with entry
// point offsets in the header to match.
//
// The x265 sizes are what a real encoder spends on the same flat grey picture,
// and coding one CU per CTB with no residual has to beat them. It is a weak
// bound, deliberately: the assertion that the stream is *correct* is
// TestGrayIDRWppDecodesFlat, since a size on its own says nothing. This used to
// be a refusal test, and before that it asserted a byte length for a stream that
// decoded to garbage — 73 % of the 1280x720 case came out zeros rather than
// mid-grey — because nothing ever looked at the pixels.
func TestGrayIDRWpp1920x1080(t *testing.T) {
	x265Sizes := map[string]int{
		"420_8bit":  610,
		"420_10bit": 610,
		"422_10bit": 637,
	}

	for _, tc := range grayBenchCases {
		t.Run(tc.name, func(t *testing.T) {
			sps, pps := parseGrayBenchSPSPPS(t, tc.sps, tc.pps)
			if !pps.EntropyCodingSyncEnabledFlag {
				t.Skip("this parameter set has WPP off")
			}
			idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if len(idr) >= x265Sizes[tc.name] {
				t.Errorf("gray IDR is %d bytes, x265 spends %d on the same picture",
					len(idr), x265Sizes[tc.name])
			}
			t.Logf("%s: %d bytes (x265: %d)", tc.name, len(idr), x265Sizes[tc.name])
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
		"4401c172b06240", 233},
	// CTU=64, height remainder only
	{"1280x720",
		"4201010408000003009fa800000300005da00280802d165ba924caf0168080000003008000000c84",
		"4401c172b06240", 159},
	{"1920x1080",
		"4201010408000003009fa8000003000078a003c08010e596ea4932bc05a02000000300200000030321",
		"4401c172b06240", 344},
	{"320x240",
		"4201010408000003009fa800000300003ca00a080f165ba924caf0168080000003008000000c84",
		"4401c172b06240", 45},
	// CTU=64, both width and height remainder
	{"960x540",
		"4201010408000003009fa800000300005aa0078200887de5ba924caf016808000003000800000300c840",
		"4401c172b06240", 103},
	{"640x360",
		"4201010408000003009fa800000300003fa00502016965ba924caf016808000003000800000300c840",
		"4401c172b06240", 89},
	{"1000x500",
		"4201010408000003009fa800000300005aa007d201f9f796ea4932bc05a02000000300200000030321",
		"4401c172b06240", 191},
	{"100x100",
		"4201010408000003009fa800000300001ea03481a77796ea4932bc05a02000000300200000030321",
		"4401c172b02240", 28},
	// CTU=32
	{"48x48",
		"4201010408000003009fa800000300001ea0620c596eae4caf016808000003000800000300c840",
		"4401c173c089", 15},
	{"72x40",
		"4201010408000003009fa800000300001ea02482965bab932bc05a020000030002000003003210",
		"4401c173c189", 24},
}

// TestGrayIDRMultiResolution validates gray IDR encoding at various resolutions
// by checking the output length matches the expected golden value. Two thirds of
// these parameter sets enable wavefront parallel processing, so their goldens
// cover the substream splitting and the entry point offsets as well; they grew
// when WPP emission landed, since the ones recorded before were lengths of
// streams that decoded to garbage.
//
// TestGrayIDRWppDecodesFlat is the assertion that the bytes mean something.
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
			t.Logf("%4dx%-4d CTU=%d rem=%dx%d wpp=%v IDR=%d bytes",
				w, h, ctu, w%ctu, h%ctu, pps.EntropyCodingSyncEnabledFlag, len(idr))
		})
	}
}

// TestGrayIDRWppDecodesFlat is what the length goldens above cannot say: that a
// gray IDR written against a wavefront parameter set decodes back to the flat
// mid-grey it is supposed to be, at every resolution and CTU size in the table.
// A single wrong entry point offset, or a context snapshot taken from the wrong
// CTB, desyncs CABAC and the picture falls apart — which is exactly what used to
// happen, unnoticed, because only the byte count was ever checked.
func TestGrayIDRWppDecodesFlat(t *testing.T) {
	for _, tc := range grayResolutionCases {
		t.Run(tc.name, func(t *testing.T) {
			sps, pps := parseGrayBenchSPSPPS(t, tc.sps, tc.pps)
			if !pps.EntropyCodingSyncEnabledFlag {
				t.Skip("this parameter set has WPP off")
			}
			idr, err := EncodeGrayIDRSliceFromSPSPPS(sps, pps)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			spsBytes, _ := hex.DecodeString(tc.sps)
			ppsBytes, _ := hex.DecodeString(tc.pps)
			stream := []byte{0, 0, 0, 1}
			stream = append(stream, spsBytes...)
			stream = append(stream, 0, 0, 0, 1)
			stream = append(stream, ppsBytes...)
			stream = append(stream, idr...)

			// The decoder outputs the visible picture, so compare against the
			// conformance window rather than the coded size: two of these x265
			// parameter sets code a taller picture than they display.
			w, h := sps.ImageSize()
			decodeGray(t, stream, int(w), int(h))
		})
	}
}
