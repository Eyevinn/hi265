package encode

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// parseParamSets extracts the VPS/SPS/PPS NALUs from an Annex-B stream and
// parses the SPS and PPS.
func parseParamSets(t *testing.T, annexB []byte) (vpsNALU, spsNALU, ppsNALU []byte,
	sps *hevc.SPS, pps *hevc.PPS) {
	t.Helper()

	spsMap := make(map[uint32]*hevc.SPS)
	for _, nalu := range avc.ExtractNalusFromByteStream(annexB) {
		if len(nalu) < 2 {
			continue
		}
		var err error
		switch hevc.GetNaluType(nalu[0]) {
		case hevc.NALU_VPS:
			vpsNALU = nalu
		case hevc.NALU_SPS:
			spsNALU = nalu
			sps, err = hevc.ParseSPSNALUnit(nalu)
			if err != nil {
				t.Fatalf("ParseSPSNALUnit: %v", err)
			}
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			ppsNALU = nalu
			pps, err = hevc.ParsePPSNALUnit(nalu, spsMap)
			if err != nil {
				t.Fatalf("ParsePPSNALUnit: %v", err)
			}
		}
	}
	if sps == nil || pps == nil {
		t.Fatal("no SPS/PPS in stream")
	}
	return vpsNALU, spsNALU, ppsNALU, sps, pps
}

// checkCRASliceHeader parses back the slice header of a single Annex-B framed CRA
// slice and verifies that it is an I-slice at the expected POC LSB.
func checkCRASliceHeader(t *testing.T, craSlice []byte, sps *hevc.SPS, pps *hevc.PPS, poc int) {
	t.Helper()

	nalus := avc.ExtractNalusFromByteStream(craSlice)
	if len(nalus) != 1 {
		t.Fatalf("expected 1 NALU in CRA slice, got %d", len(nalus))
	}
	if got := hevc.GetNaluType(nalus[0][0]); got != hevc.NALU_CRA {
		t.Errorf("NALU type = %s, want RAP_CRA_21", got)
	}

	spsMap := map[uint32]*hevc.SPS{uint32(sps.SpsID): sps}
	ppsMap := map[uint32]*hevc.PPS{pps.PicParameterSetID: pps}
	sh, err := hevc.ParseSliceHeader(nalus[0], spsMap, ppsMap)
	if err != nil {
		t.Fatalf("ParseSliceHeader: %v", err)
	}

	if sh.SliceType != hevc.SLICE_I {
		t.Errorf("SliceType = %s, want I", sh.SliceType)
	}
	pocMask := (1 << (int(sps.Log2MaxPicOrderCntLsbMinus4) + 4)) - 1
	if want := uint16(poc & pocMask); sh.PicOrderCntLsb != want {
		t.Errorf("PicOrderCntLsb = %d, want %d", sh.PicOrderCntLsb, want)
	}
	if sh.ShortTermRefPicSetSpsFlag {
		t.Error("ShortTermRefPicSetSpsFlag = true, want false (empty inline RPS)")
	}
	if sh.ShortTermRefPicSet.NumNegativePics != 0 || sh.ShortTermRefPicSet.NumPositivePics != 0 {
		t.Errorf("short-term RPS = %d negative/%d positive pics, want 0/0",
			sh.ShortTermRefPicSet.NumNegativePics, sh.ShortTermRefPicSet.NumPositivePics)
	}
	if sh.NumLongTermSps != 0 || sh.NumLongTermPics != 0 {
		t.Errorf("long-term RPS = %d sps/%d pics, want 0/0", sh.NumLongTermSps, sh.NumLongTermPics)
	}
	// Spec 7.4.7.1: slice_temporal_mvp_enabled_flag shall be 0 for a CRA picture.
	if sh.TemporalMvpEnabledFlag {
		t.Error("TemporalMvpEnabledFlag = true, want false for a CRA picture")
	}
}

// craHeaderCases are real-world 1920x1080 parameter sets used to check the CRA
// slice header against mp4ff's parser. The 4:2:2 set is the interesting one: it
// has num_short_term_ref_pic_sets = 1 (so the inline RPS carries
// inter_ref_pic_set_prediction_flag), long_term_ref_pics_present_flag = 1 with
// num_long_term_ref_pics_sps = 0 (so num_long_term_sps is absent) and only 4 POC
// LSB bits, while the 4:2:0 set has 8 POC LSB bits and temporal MVP enabled.
var craHeaderCases = []struct {
	name string
	sps  string
	pps  string
}{
	{
		name: "420_10bit",
		sps:  "4201010408000003009db8000003000078a003c08010e4d96566924cae0100000303e8000061a808",
		pps:  "4401c171a112",
	},
	{
		name: "422_10bit",
		sps: "4201012408000003009d0800000300007bb003c08010e4dcb9246d92f980a1010101" +
			"14000003000400000300c98277bdf800017d7840000bebc320",
		pps: "4401c0764c0ed9",
	},
}

// TestGrayCRASliceHeader checks the CRA slice header against mp4ff's parser at a
// range of POCs, including POCs that wrap the POC LSB field.
func TestGrayCRASliceHeader(t *testing.T) {
	for _, tc := range craHeaderCases {
		sps, pps := parseGrayBenchSPSPPS(t, tc.sps, tc.pps)
		for _, poc := range []int{0, 1, 9, 42, 255, 300} {
			t.Run(fmt.Sprintf("%s/poc%d", tc.name, poc), func(t *testing.T) {
				craSlice, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, poc)
				if err != nil {
					t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
				}
				checkCRASliceHeader(t, craSlice, sps, pps, poc)
			})
		}
	}
}

// TestGrayCRARoundTrip decodes a gray CRA with the hi265 decoder and checks that
// the whole picture is mid-gray.
func TestGrayCRARoundTrip(t *testing.T) {
	p := EncodeParams{Width: 64, Height: 32, QP: 26}
	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	_, _, _, sps, pps := parseParamSets(t, vpsSPSPPS)

	const poc = 137
	craSlice, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, poc)
	if err != nil {
		t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
	}
	checkCRASliceHeader(t, craSlice, sps, pps, poc)

	frames, err := decoder.New().DecodeAnnexB(append(vpsSPSPPS, craSlice...))
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i, v := range f.Y {
		if v != 128 {
			t.Fatalf("Y[%d] = %d, want 128", i, v)
		}
	}
	for i, v := range f.Cb {
		if v != 128 {
			t.Fatalf("Cb[%d] = %d, want 128", i, v)
		}
	}
	for i, v := range f.Cr {
		if v != 128 {
			t.Fatalf("Cr[%d] = %d, want 128", i, v)
		}
	}
}

// TestEncodeCRAFromSPSPPS checks the grid variant: a CRA carrying a two-colour
// pattern decodes to that pattern.
func TestEncodeCRAFromSPSPPS(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26}
	grid, err := yuv.ParseGrid("AB,AB")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	colors := yuv.ColorMap{
		'A': yuv.Color{Y: 235, Cb: 128, Cr: 128},
		'B': yuv.Color{Y: 16, Cb: 128, Cr: 128},
	}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	_, _, _, sps, pps := parseParamSets(t, vpsSPSPPS)

	const poc = 9
	craSlice, err := EncodeCRASliceFromSPSPPS(sps, pps, grid, colors, poc)
	if err != nil {
		t.Fatalf("EncodeCRASliceFromSPSPPS: %v", err)
	}
	checkCRASliceHeader(t, craSlice, sps, pps, poc)

	frames, err := decoder.New().DecodeAnnexB(append(vpsSPSPPS, craSlice...))
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(frames))
	}

	f := frames[0]
	for i := range 32 * 32 {
		want := 235
		if i%32 >= 16 {
			want = 16
		}
		if diff := abs(int(f.Y[i]) - want); diff > 2 {
			t.Fatalf("Y[%d] = %d, want %d (±2)", i, f.Y[i], want)
		}
	}
}

// TestCRASpliceKeepsPOC splices a gray CRA into a running stream of P-skip
// pictures and checks that the POC continues across the splice (which is what an
// IDR cannot do) and that the CRA becomes the reference for the pictures after it.
func TestCRASpliceKeepsPOC(t *testing.T) {
	p := EncodeParams{Width: 32, Height: 32, QP: 26}
	grid, err := yuv.ParseGrid("AA,AA")
	if err != nil {
		t.Fatalf("ParseGrid: %v", err)
	}
	colors := yuv.ColorMap{'A': yuv.Color{Y: 16, Cb: 128, Cr: 128}}

	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	_, _, _, sps, pps := parseParamSets(t, vpsSPSPPS)

	stream := append([]byte{}, vpsSPSPPS...)
	idr, err := EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
	if err != nil {
		t.Fatalf("EncodeIDRSliceFromSPSPPS: %v", err)
	}
	stream = append(stream, idr...)

	// POC 1 and 2: freeze frames, POC 3: gray CRA refresh, POC 4: freeze
	const craPOC = 3
	for _, poc := range []int{1, 2} {
		pSkip, err := EncodePSkipSliceFromSPSPPS(sps, pps, poc)
		if err != nil {
			t.Fatalf("EncodePSkipSliceFromSPSPPS(%d): %v", poc, err)
		}
		stream = append(stream, pSkip...)
	}
	cra, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, craPOC)
	if err != nil {
		t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
	}
	stream = append(stream, cra...)
	pSkip, err := EncodePSkipSliceFromSPSPPS(sps, pps, craPOC+1)
	if err != nil {
		t.Fatalf("EncodePSkipSliceFromSPSPPS: %v", err)
	}
	stream = append(stream, pSkip...)

	// The POCs of all non-IDR slices must run continuously across the CRA splice.
	spsMap := map[uint32]*hevc.SPS{uint32(sps.SpsID): sps}
	ppsMap := map[uint32]*hevc.PPS{pps.PicParameterSetID: pps}
	var gotPOCs []int
	var gotTypes []hevc.NaluType
	for _, nalu := range avc.ExtractNalusFromByteStream(stream) {
		naluType := hevc.GetNaluType(nalu[0])
		switch naluType {
		case hevc.NALU_IDR_W_RADL, hevc.NALU_CRA, hevc.NALU_TRAIL_R:
			gotTypes = append(gotTypes, naluType)
			if naluType == hevc.NALU_IDR_W_RADL {
				gotPOCs = append(gotPOCs, 0)
				continue
			}
			sh, err := hevc.ParseSliceHeader(nalu, spsMap, ppsMap)
			if err != nil {
				t.Fatalf("ParseSliceHeader(%s): %v", naluType, err)
			}
			gotPOCs = append(gotPOCs, int(sh.PicOrderCntLsb))
		}
	}

	wantTypes := []hevc.NaluType{hevc.NALU_IDR_W_RADL, hevc.NALU_TRAIL_R, hevc.NALU_TRAIL_R,
		hevc.NALU_CRA, hevc.NALU_TRAIL_R}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("slice NALU types = %v, want %v", gotTypes, wantTypes)
	}
	for i, want := range wantTypes {
		if gotTypes[i] != want {
			t.Errorf("slice %d: type %s, want %s", i, gotTypes[i], want)
		}
	}
	for i, want := range []int{0, 1, 2, 3, 4} {
		if gotPOCs[i] != want {
			t.Errorf("slice %d: POC %d, want %d", i, gotPOCs[i], want)
		}
	}

	frames, err := decoder.New().DecodeAnnexB(stream)
	if err != nil {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if len(frames) != 5 {
		t.Fatalf("expected 5 frames, got %d", len(frames))
	}
	// Frames 0-2 are the black IDR content, frame 3 the gray CRA, frame 4 a
	// freeze of the CRA.
	for i, v := range frames[2].Y {
		if diff := abs(int(v) - 16); diff > 2 {
			t.Fatalf("frame 2 Y[%d] = %d, want 16 (±2)", i, v)
		}
	}
	for i, v := range frames[3].Y {
		if v != 128 {
			t.Fatalf("CRA frame Y[%d] = %d, want 128", i, v)
		}
	}
	for i, v := range frames[4].Y {
		if v != frames[3].Y[i] {
			t.Fatalf("post-CRA freeze Y[%d] = %d, want %d", i, v, frames[3].Y[i])
		}
	}
}

// TestGrayCRAFFmpeg checks that FFmpeg decodes a VPS+SPS+PPS+gray-CRA stream to
// uniform mid-gray, and that mp4ff-nallister reports the slice as RAP_CRA_21.
func TestGrayCRAFFmpeg(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not in PATH")
	}

	const w, h, poc = 64, 32, 42
	p := EncodeParams{Width: w, Height: h, QP: 26}
	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		t.Fatalf("GenerateVPSSPSPPS: %v", err)
	}
	_, _, _, sps, pps := parseParamSets(t, vpsSPSPPS)
	craSlice, err := EncodeGrayCRASliceFromSPSPPS(sps, pps, poc)
	if err != nil {
		t.Fatalf("EncodeGrayCRASliceFromSPSPPS: %v", err)
	}

	dir := t.TempDir()
	inPath := filepath.Join(dir, "gray_cra.265")
	outPath := filepath.Join(dir, "gray_cra.yuv")
	if err := os.WriteFile(inPath, append(vpsSPSPPS, craSlice...), 0o644); err != nil {
		t.Fatalf("write bitstream: %v", err)
	}

	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", inPath,
		"-f", "rawvideo", "-pix_fmt", "yuv420p", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}

	yuvData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read ffmpeg output: %v", err)
	}
	if want := w * h * 3 / 2; len(yuvData) != want {
		t.Fatalf("ffmpeg output is %d bytes, want %d (one %dx%d 4:2:0 frame)",
			len(yuvData), want, w, h)
	}
	for i, v := range yuvData {
		if v != 128 {
			t.Fatalf("ffmpeg output byte %d = %d, want 128 (mid-gray)", i, v)
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
}
