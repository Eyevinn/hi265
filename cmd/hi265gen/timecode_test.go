package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/Eyevinn/mp4ff/sei"
)

const naluPrefixSEI = 39

// seiTimecodes returns the Time Code SEI timecode (HH:MM:SS:FF) for each
// PREFIX_SEI NALU in an HEVC Annex-B stream, in bitstream order.
func seiTimecodes(t *testing.T, annexB []byte) []string {
	t.Helper()
	var out []string
	for _, nalu := range avc.ExtractNalusFromByteStream(annexB) {
		if len(nalu) < 3 || (nalu[0]>>1)&0x3F != naluPrefixSEI {
			continue
		}
		sd, err := sei.ExtractSEIData(bytes.NewReader(nalu[2:]))
		if err != nil {
			t.Fatalf("ExtractSEIData: %v", err)
		}
		for i := range sd {
			if sd[i].Type() != sei.SEITimeCodeType {
				continue
			}
			msg, err := sei.DecodeTimeCodeSEI(&sd[i])
			if err != nil {
				t.Fatalf("DecodeTimeCodeSEI: %v", err)
			}
			c := msg.(*sei.TimeCodeSEI).Clocks[0]
			out = append(out, fmt.Sprintf("%02d:%02d:%02d:%02d", c.Hours, c.Minutes, c.Seconds, c.NFrames))
		}
	}
	return out
}

// firstSEIClock returns the first Time Code SEI clock timestamp in an HEVC
// Annex-B stream.
func firstSEIClock(t *testing.T, annexB []byte) sei.ClockTS {
	t.Helper()
	for _, nalu := range avc.ExtractNalusFromByteStream(annexB) {
		if len(nalu) < 3 || (nalu[0]>>1)&0x3F != naluPrefixSEI {
			continue
		}
		sd, err := sei.ExtractSEIData(bytes.NewReader(nalu[2:]))
		if err != nil {
			t.Fatalf("ExtractSEIData: %v", err)
		}
		for i := range sd {
			if sd[i].Type() != sei.SEITimeCodeType {
				continue
			}
			msg, err := sei.DecodeTimeCodeSEI(&sd[i])
			if err != nil {
				t.Fatalf("DecodeTimeCodeSEI: %v", err)
			}
			return msg.(*sei.TimeCodeSEI).Clocks[0]
		}
	}
	t.Fatal("no Time Code SEI found")
	return sei.ClockTS{}
}

// ffprobeTimecodes returns the per-frame timecode tag that ffprobe reads back
// from a stream, skipping the test when ffprobe is not installed.
func ffprobeTimecodes(t *testing.T, path string) []string {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v",
		"-show_entries", "frame_tags=timecode", "-of", "csv=nk=1:p=0", path)
	outBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	// csv=p=0 still prints a trailing separator for the frame section, so parse
	// the CSV and keep the first field of each non-empty record.
	recs, err := csv.NewReader(bytes.NewReader(outBytes)).ReadAll()
	if err != nil {
		t.Fatalf("parsing ffprobe CSV output %q: %v", outBytes, err)
	}
	var out []string
	for _, rec := range recs {
		if len(rec) > 0 && rec[0] != "" {
			out = append(out, rec[0])
		}
	}
	return out
}

// TestTimeCodeSEIStartFrame verifies -timecode + -start-frame: frame 76 at
// 25 fps is 00:00:03:01, advancing one frame at a time.
func TestTimeCodeSEIStartFrame(t *testing.T) {
	out := filepath.Join(t.TempDir(), "sf.265")
	if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "3",
		"-fps", "25", "-timecode", "-start-frame", "76", "-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"00:00:03:01", "00:00:03:02", "00:00:03:03"}
	got := seiTimecodes(t, data)
	if len(got) != len(want) {
		t.Fatalf("got %d SEI timecodes %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d SEI timecode = %s, want %s", i, got[i], want[i])
		}
	}
	// counting_type 1 = no dropping, and no frame is a dropped label.
	clock := firstSEIClock(t, data)
	if clock.CountingType != 1 {
		t.Errorf("counting_type = %d, want 1", clock.CountingType)
	}
	if clock.CntDroppedFlag {
		t.Error("cnt_dropped_flag = true, want false")
	}
	if probed := ffprobeTimecodes(t, out); !equalStrings(probed, want) {
		t.Errorf("ffprobe timecodes = %v, want %v", probed, want)
	}
}

// TestTimeCodeSEIPerPicture verifies every picture carries a Time Code SEI,
// P-skip pictures included, and that ffprobe agrees.
func TestTimeCodeSEIPerPicture(t *testing.T) {
	out := filepath.Join(t.TempDir(), "pskip.265")
	if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "6",
		"-fps", "25", "-idr-interval", "3", "-timecode", "-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"00:00:00:00", "00:00:00:01", "00:00:00:02",
		"00:00:00:03", "00:00:00:04", "00:00:00:05",
	}
	if got := seiTimecodes(t, data); !equalStrings(got, want) {
		t.Errorf("SEI timecodes = %v, want %v", got, want)
	}
	if probed := ffprobeTimecodes(t, out); !equalStrings(probed, want) {
		t.Errorf("ffprobe timecodes = %v, want %v", probed, want)
	}
}

// TestTimeCodeSEISegmentsConcatenate verifies that two segments generated with
// adjacent -start-frame offsets form one continuous timecode sequence.
func TestTimeCodeSEISegmentsConcatenate(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, start int) []byte {
		path := filepath.Join(dir, name)
		if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "48",
			"-fps", "25", "-timecode", "-start-frame", fmt.Sprint(start), "-o", path}); err != nil {
			t.Fatalf("run start=%d: %v", start, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	joined := append(mk("s0.265", 0), mk("s1.265", 48)...)

	tc := seiTimecodes(t, joined)
	if len(tc) != 96 {
		t.Fatalf("got %d timecodes, want 96", len(tc))
	}
	// The join: frame 47 -> 00:00:01:22, frame 48 -> 00:00:01:23 (continuous).
	if tc[47] != "00:00:01:22" || tc[48] != "00:00:01:23" {
		t.Errorf("join timecodes = %s,%s, want 00:00:01:22,00:00:01:23", tc[47], tc[48])
	}
}

// TestTimeCodeSEIInMP4 verifies the SEI survives MP4 sample assembly and that
// -start-frame offsets the media timeline and fragment sequence numbers.
func TestTimeCodeSEIInMP4(t *testing.T) {
	out := filepath.Join(t.TempDir(), "tc.mp4")
	if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "4",
		"-fps", "25", "-frag-dur", "2", "-timecode", "-start-frame", "50", "-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}

	var wantSeq uint32 = 50/2 + 1
	wantDecodeTime := uint64(50)
	var tcs []string
	for _, seg := range parsed.Segments {
		for _, frag := range seg.Fragments {
			if got := frag.Moof.Mfhd.SequenceNumber; got != wantSeq {
				t.Errorf("fragment sequence_number = %d, want %d", got, wantSeq)
			}
			if got := frag.Moof.Traf.Tfdt.BaseMediaDecodeTime(); got != wantDecodeTime {
				t.Errorf("tfdt baseMediaDecodeTime = %d, want %d", got, wantDecodeTime)
			}
			samples, err := frag.GetFullSamples(nil)
			if err != nil {
				t.Fatalf("GetFullSamples: %v", err)
			}
			for _, s := range samples {
				tcs = append(tcs, seiTimecodes(t, naluSampleToByteStream(s.Data))...)
			}
			wantSeq++
			wantDecodeTime += uint64(len(samples))
		}
	}
	want := []string{"00:00:02:00", "00:00:02:01", "00:00:02:02", "00:00:02:03"}
	if !equalStrings(tcs, want) {
		t.Errorf("MP4 sample timecodes = %v, want %v", tcs, want)
	}
	if probed := ffprobeTimecodes(t, out); !equalStrings(probed, want) {
		t.Errorf("ffprobe timecodes = %v, want %v", probed, want)
	}
}

// TestFractionalFPSMP4Timescale verifies that -fps 30000/1001 gives an MP4 with
// media timescale 30000 and per-sample duration 1001, while the timecode counts
// at the nominal integer rate of 30.
func TestFractionalFPSMP4Timescale(t *testing.T) {
	out := filepath.Join(t.TempDir(), "f.mp4")
	if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "31",
		"-fps", "30000/1001", "-timecode", "-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if ts := parsed.Init.Moov.Trak.Mdia.Mdhd.Timescale; ts != 30000 {
		t.Errorf("media timescale = %d, want 30000", ts)
	}
	var tcs []string
	for _, seg := range parsed.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(nil)
			if err != nil {
				t.Fatalf("GetFullSamples: %v", err)
			}
			for i, s := range samples {
				if s.Dur != 1001 {
					t.Errorf("sample %d dur = %d, want 1001", i, s.Dur)
				}
				tcs = append(tcs, seiTimecodes(t, naluSampleToByteStream(s.Data))...)
			}
		}
	}
	if len(tcs) != 31 {
		t.Fatalf("got %d SEI timecodes, want 31", len(tcs))
	}
	// Nominal rate 30: frame 29 is the last of second 0, frame 30 starts second 1.
	if tcs[0] != "00:00:00:00" || tcs[29] != "00:00:00:29" || tcs[30] != "00:00:01:00" {
		t.Errorf("timecodes[0,29,30] = %s,%s,%s, want 00:00:00:00,00:00:00:29,00:00:01:00",
			tcs[0], tcs[29], tcs[30])
	}
}

// TestTimeCodeDropFrame verifies 29.97 drop-frame counting across a minute
// boundary: labels ;00 and ;01 are skipped, the SEI signals counting_type 4,
// and cnt_dropped_flag marks the frame where the skip happens (which is why
// ffprobe renders that one frame with a ';' separator).
func TestTimeCodeDropFrame(t *testing.T) {
	out := filepath.Join(t.TempDir(), "df.265")
	if err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "5",
		"-fps", "29.97", "-drop-frame", "-timecode", "-start-frame", "1798",
		"-o", out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"00:00:59:28", "00:00:59:29", "00:01:00:02", "00:01:00:03", "00:01:00:04"}
	if got := seiTimecodes(t, data); !equalStrings(got, want) {
		t.Errorf("SEI timecodes = %v, want %v", got, want)
	}
	clock := firstSEIClock(t, data)
	if clock.CountingType != 4 {
		t.Errorf("counting_type = %d, want 4 (drop-frame)", clock.CountingType)
	}
	// ffprobe writes drop-frame timecodes with a ';' before the frame field;
	// normalise it so the comparison is about the counting, not the notation.
	probed := ffprobeTimecodes(t, out)
	for i := range probed {
		probed[i] = strings.ReplaceAll(probed[i], ";", ":")
	}
	if !equalStrings(probed, want) {
		t.Errorf("ffprobe timecodes = %v, want %v", probed, want)
	}
}

// TestDropFrameRejectsNonNTSCRate verifies -drop-frame is rejected outside the
// NTSC rates it is defined for.
func TestDropFrameRejectsNonNTSCRate(t *testing.T) {
	for _, fps := range []string{"25", "30", "24000/1001"} {
		out := filepath.Join(t.TempDir(), "x.265")
		err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "2",
			"-fps", fps, "-drop-frame", "-timecode", "-o", out})
		if err == nil || !strings.Contains(err.Error(), "drop-frame") {
			t.Errorf("-fps %s: expected drop-frame rejection error, got %v", fps, err)
		}
	}
}

func TestParseFPS(t *testing.T) {
	ok := []struct {
		in       string
		num, den int
	}{
		{"25", 25, 1},
		{"30", 30, 1},
		{"30000/1001", 30000, 1001},
		{"29.97", 30000, 1001},
		{"59.94", 60000, 1001},
		{"23.976", 24000, 1001},
		{"119.88", 120000, 1001},
	}
	for _, c := range ok {
		n, d, err := parseFPS(c.in)
		if err != nil || n != c.num || d != c.den {
			t.Errorf("parseFPS(%q) = %d/%d, %v; want %d/%d", c.in, n, d, err, c.num, c.den)
		}
	}
	for _, bad := range []string{"abc", "30/0", "30/-1", "24.5", ""} {
		if _, _, err := parseFPS(bad); err == nil {
			t.Errorf("parseFPS(%q) expected error", bad)
		}
	}
}

// TestTimeCodeRejectsRawFormats verifies -timecode is rejected for the raw
// (non-bitstream) output formats.
func TestTimeCodeRejectsRawFormats(t *testing.T) {
	out := filepath.Join(t.TempDir(), "x.y4m")
	err := run([]string{appName, "-smpte", "-w", "176", "-h", "80", "-n", "2",
		"-timecode", "-o", out})
	if err == nil || !strings.Contains(err.Error(), "timecode") {
		t.Fatalf("expected -timecode rejection error, got %v", err)
	}
}

// TestStartFrameNegativeRejected verifies a negative -start-frame is rejected.
func TestStartFrameNegativeRejected(t *testing.T) {
	out := filepath.Join(t.TempDir(), "x.265")
	err := run([]string{appName, "-smpte", "-w", "176", "-h", "80",
		"-start-frame", "-1", "-o", out})
	if err == nil || !strings.Contains(err.Error(), "start-frame") {
		t.Fatalf("expected start-frame rejection error, got %v", err)
	}
}

// naluSampleToByteStream converts a length-prefixed MP4 NALU sample back to an
// Annex-B byte stream.
func naluSampleToByteStream(sample []byte) []byte {
	var out []byte
	for pos := 0; pos+4 <= len(sample); {
		size := int(sample[pos])<<24 | int(sample[pos+1])<<16 |
			int(sample[pos+2])<<8 | int(sample[pos+3])
		pos += 4
		if size < 0 || pos+size > len(sample) {
			break
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, sample[pos:pos+size]...)
		pos += size
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
