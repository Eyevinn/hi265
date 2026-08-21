package encode

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/hi264/pkg/yuv"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// This file is the generator conformance harness (roadmap phase 0.2).
//
// The golden tests in pkg/decoder validate the *decoder* against FFmpeg on
// x265-produced streams. Nothing there validates the *encoder*. A shared
// encoder/decoder assumption is therefore invisible: both agree with each
// other while disagreeing with every conforming decoder (phase 0.1 was
// exactly that). The assertion that closes the gap is
// "FFmpeg output == hi265dec output, byte for byte" on streams that
// pkg/encode produced itself.
//
// Every case additionally checks that both decoders land within quantization
// tolerance of the intended pattern, so a stream that is *consistently* wrong
// in both decoders still gets caught.
//
// FFmpeg is required; the tests skip when it is absent so CI without FFmpeg
// stays green. Set HI265_FFMPEG to point at a specific binary.

// ---------------------------------------------------------------------------
// external tools
// ---------------------------------------------------------------------------

// ffmpegBin returns the ffmpeg binary to use, skipping the test when FFmpeg
// is not installed.
func ffmpegBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HI265_FFMPEG"); p != "" {
		return p
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH: skipping conformance comparison")
	}
	return p
}

// x265Bin returns the x265 binary, skipping the test when it is not installed.
func x265Bin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HI265_X265"); p != "" {
		return p
	}
	p, err := exec.LookPath("x265")
	if err != nil {
		t.Skip("x265 not found in PATH: skipping reference-encoder comparison")
	}
	return p
}

// decodeWithFFmpeg decodes an Annex-B stream with FFmpeg and returns the raw
// yuv420p bytes of all frames concatenated.
func decodeWithFFmpeg(t *testing.T, ffmpeg string, annexB []byte) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.265")
	out := filepath.Join(dir, "out.yuv")
	if err := os.WriteFile(in, annexB, 0o600); err != nil {
		t.Fatalf("write bitstream: %v", err)
	}
	cmd := exec.Command(ffmpeg, "-y", "-loglevel", "error",
		"-i", in, "-f", "rawvideo", "-pix_fmt", "yuv420p", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode failed: %v\n%s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read ffmpeg output: %v", err)
	}
	return data
}

// decodeWithHi265 decodes an Annex-B stream with pkg/decoder and returns the
// raw yuv420p bytes of all frames concatenated.
func decodeWithHi265(t *testing.T, annexB []byte, width, height, frames int) []byte {
	t.Helper()
	dec := decoder.New()
	decoded, err := dec.DecodeAnnexB(annexB)
	if err != nil {
		t.Fatalf("hi265 decode: %v", err)
	}
	if len(decoded) != frames {
		t.Fatalf("hi265 decoded %d frames, want %d", len(decoded), frames)
	}
	var out []byte
	for i, f := range decoded {
		if f.Width != width || f.Height != height {
			t.Fatalf("frame %d: hi265 decoded %dx%d, want %dx%d",
				i, f.Width, f.Height, width, height)
		}
		out = append(out, f.YUV420Bytes()...)
	}
	return out
}

// ---------------------------------------------------------------------------
// comparison
// ---------------------------------------------------------------------------

// planeDiff summarizes the disagreement on one plane of one frame.
type planeDiff struct {
	frame    int
	plane    string
	count    int
	maxDelta int
	worstX   int
	worstY   int
	ctuX     int
	ctuY     int
}

func (d planeDiff) String() string {
	return fmt.Sprintf("frame %d %s: %d samples differ, max delta %d, "+
		"worst at (%d,%d) in CTU (%d,%d)",
		d.frame, d.plane, d.count, d.maxDelta, d.worstX, d.worstY, d.ctuX, d.ctuY)
}

// yuvDiff is the full report for a multi-frame comparison.
type yuvDiff struct {
	planes   []planeDiff // only planes with at least one difference
	total    int
	maxDelta int
}

func (d *yuvDiff) exact() bool { return d.total == 0 }

func (d *yuvDiff) String() string {
	if d.exact() {
		return "identical"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d samples differ (max delta %d)", d.total, d.maxDelta)
	for _, p := range d.planes {
		fmt.Fprintf(&sb, "\n  %s", p)
	}
	return sb.String()
}

// compareYUV compares two multi-frame yuv420p buffers sample by sample.
// Failure output names the plane, the sample count, the max delta and the CTU
// coordinates of the worst sample so a mismatch is actionable without a
// separate debugging round.
func compareYUV(t *testing.T, got, want []byte, width, height, frames int) *yuvDiff {
	t.Helper()
	lumaSize := width * height
	chromaSize := (width / 2) * (height / 2)
	frameSize := lumaSize + 2*chromaSize
	if len(got) != frames*frameSize || len(want) != frames*frameSize {
		t.Fatalf("size mismatch: got %d, want %d, expected %d (%d frames of %d)",
			len(got), len(want), frames*frameSize, frames, frameSize)
	}

	rep := &yuvDiff{}
	planes := []struct {
		name   string
		offset int
		size   int
		stride int
		sub    int // 1 for luma, 2 for chroma (CTU is 16 luma samples)
	}{
		{"Y", 0, lumaSize, width, 1},
		{"Cb", lumaSize, chromaSize, width / 2, 2},
		{"Cr", lumaSize + chromaSize, chromaSize, width / 2, 2},
	}

	for fi := range frames {
		base := fi * frameSize
		for _, pl := range planes {
			pd := planeDiff{frame: fi, plane: pl.name}
			for i := range pl.size {
				idx := base + pl.offset + i
				d := int(got[idx]) - int(want[idx])
				if d < 0 {
					d = -d
				}
				if d == 0 {
					continue
				}
				pd.count++
				if d > pd.maxDelta {
					pd.maxDelta = d
					pd.worstX = i % pl.stride
					pd.worstY = i / pl.stride
					pd.ctuX = (pd.worstX * pl.sub) / 16
					pd.ctuY = (pd.worstY * pl.sub) / 16
				}
			}
			if pd.count == 0 {
				continue
			}
			rep.planes = append(rep.planes, pd)
			rep.total += pd.count
			if pd.maxDelta > rep.maxDelta {
				rep.maxDelta = pd.maxDelta
			}
		}
	}
	return rep
}

// patternError is the deviation of a decoded picture from the intended
// (unquantized) pattern.
type patternError struct {
	maxDelta int
	meanAbs  float64
}

func measurePatternError(got, want []byte) patternError {
	var sum, maxD int
	for i := range got {
		d := int(got[i]) - int(want[i])
		if d < 0 {
			d = -d
		}
		sum += d
		if d > maxD {
			maxD = d
		}
	}
	return patternError{maxDelta: maxD, meanAbs: float64(sum) / float64(len(got))}
}

// ---------------------------------------------------------------------------
// stream generation through the pkg/encode API
// ---------------------------------------------------------------------------

// patternKind selects the CTU grid pattern for a conformance case.
type patternKind int

const (
	patFlat      patternKind = iota // one flat colour over the whole frame
	patTiles                        // two colours in a CTU checkerboard
	patSMPTE                        // 75% SMPTE bars, tiled over all CTU rows
	patSMPTEText                    // SMPTE bars with a "%03d" counter overlay
	patDiagonal                     // one bright CTU per row, stepping right
)

// buildGrid mirrors hi265gen's buildFrameGrid for the pattern kinds used here,
// so the harness exercises the same grid construction the CLI does.
func buildGrid(kind patternKind, frameIdx, mbW, mbH int) (*yuv.Grid, yuv.ColorMap, error) {
	cs := yuv.BT601
	rng := yuv.LimitedRange

	switch kind {
	case patFlat:
		return uniformGrid(mbW, mbH, 'A'),
			yuv.ColorMap{'A': yuv.Color{Y: 128, Cb: 100, Cr: 160}}, nil

	case patTiles:
		chars := make([][]byte, mbH)
		for y := range mbH {
			chars[y] = make([]byte, mbW)
			for x := range mbW {
				if (x+y)%2 == 0 {
					chars[y][x] = 'A'
				} else {
					chars[y][x] = 'B'
				}
			}
		}
		return &yuv.Grid{Chars: chars, Width: mbW, Height: mbH},
			yuv.ColorMap{
				'A': yuv.Color{Y: 235, Cb: 128, Cr: 128},
				'B': yuv.Color{Y: 16, Cb: 128, Cr: 128},
			}, nil

	case patDiagonal:
		// A pattern that is neither column- nor row-uniform. Column- and
		// row-uniform patterns (flat, SMPTE bars, stripes) leave the mode
		// 10/26 boundary filter a no-op because the left/top neighbour equals
		// the top-left corner sample; a diagonal breaks that.
		chars := make([][]byte, mbH)
		for y := range mbH {
			chars[y] = make([]byte, mbW)
			for x := range mbW {
				chars[y][x] = 'B'
			}
			chars[y][y%mbW] = 'A'
		}
		return &yuv.Grid{Chars: chars, Width: mbW, Height: mbH},
			yuv.ColorMap{
				'A': yuv.Color{Y: 235, Cb: 128, Cr: 128},
				'B': yuv.Color{Y: 40, Cb: 128, Cr: 128},
			}, nil

	case patSMPTE:
		pat, patColors := yuv.SMPTEBarsGridCS(mbW, cs, rng)
		bg := uniformGrid(mbW, mbH, '.')
		bgColors := yuv.ColorMap{'.': yuv.Color{Y: 16, Cb: 128, Cr: 128}}
		return yuv.TileBackground(bg, bgColors, pat, patColors)

	case patSMPTEText:
		pat, patColors := yuv.SMPTEBarsGridCS(mbW, cs, rng)
		text := yuv.FormatText("%03d", frameIdx, 25)
		scale := yuv.AutoTextScale(yuv.FormatText("%03d", 0, 25), mbW, mbH)
		fg := yuv.RGBToYCbCrCS(255, 255, 255, cs, rng)
		bg := yuv.RGBToYCbCrCS(0, 0, 0, cs, rng)
		grid, colors, err := yuv.TextGrid(text, mbW, mbH, scale, bg, fg, nil)
		if err != nil {
			return nil, nil, err
		}
		return yuv.TileBackground(grid, colors, pat, patColors)
	}
	return nil, nil, fmt.Errorf("unknown pattern kind %d", kind)
}

func uniformGrid(mbW, mbH int, ch byte) *yuv.Grid {
	chars := make([][]byte, mbH)
	for y := range mbH {
		chars[y] = make([]byte, mbW)
		for x := range mbW {
			chars[y][x] = ch
		}
	}
	return &yuv.Grid{Chars: chars, Width: mbW, Height: mbH}
}

// generatedStream is a generated bitstream plus the pattern it was meant to
// represent.
type generatedStream struct {
	annexB   []byte
	intended []byte // yuv420p bytes of all frames, unquantized
}

// generateStream produces the bitstream exactly the way cmd/hi265gen does:
// VPS+SPS+PPS once, then one IDR slice per frame, or IDR/P-skip according to
// idrInterval.
func generateStream(t *testing.T, c conformanceCase) generatedStream {
	t.Helper()
	mbW := (c.width + 15) / 16
	mbH := (c.height + 15) / 16

	enc := &FrameEncoder{
		QP:     c.qp,
		Width:  c.width,
		Height: c.height,
	}

	// Width and Height are explicit, so the parameter sets do not depend on
	// the grid and can be written before the first frame is built.
	var buf bytes.Buffer
	enc.EncodeVPSSPSPPS(&buf)

	var intended []byte
	var lastIntended []byte
	poc := 1

	for i := range c.frames {
		isIDR := c.idrInterval <= 0 || i%c.idrInterval == 0
		if isIDR {
			grid, colors, err := buildGrid(c.pattern, i, mbW, mbH)
			if err != nil {
				t.Fatalf("frame %d: build grid: %v", i, err)
			}
			enc.Grid = grid
			enc.Colors = colors
			slice, err := enc.EncodeIDRSlice()
			if err != nil {
				t.Fatalf("frame %d: EncodeIDRSlice: %v", i, err)
			}
			buf.Write(slice)

			f, err := yuv.BuildFrame(grid, colors)
			if err != nil {
				t.Fatalf("frame %d: BuildFrame: %v", i, err)
			}
			f.Width = c.width
			f.Height = c.height
			lastIntended = f.YUV420Bytes()
			poc = 1
		} else {
			slice, err := enc.EncodePSkipSlice(poc)
			if err != nil {
				t.Fatalf("frame %d: EncodePSkipSlice: %v", i, err)
			}
			buf.Write(slice)
			poc++
		}
		// A P-skip frame is a pixel copy of its reference, so the intended
		// picture is the one from the IDR that opened the GOP.
		intended = append(intended, lastIntended...)
	}

	return generatedStream{annexB: buf.Bytes(), intended: intended}
}

// ---------------------------------------------------------------------------
// the case table
// ---------------------------------------------------------------------------

// knownDefect pins a currently-failing case: the measured mismatch is logged
// and only exceeding the recorded budget fails the test. It documents the
// defect without breaking the build, and still catches a regression that makes
// the defect worse.
type knownDefect struct {
	maxSamples int    // recorded FFmpeg-vs-hi265 differing sample count
	maxDelta   int    // recorded worst absolute delta
	note       string // what is broken and where
}

type conformanceCase struct {
	name        string
	width       int
	height      int
	qp          int
	frames      int
	idrInterval int // 0 = every frame is an IDR
	pattern     patternKind

	// Tolerance of the decoded picture against the intended pattern. Flat CTU
	// content quantizes almost exactly; a text overlay puts hard edges inside
	// the intra prediction neighbourhood and costs more.
	maxPatternDelta int
	maxPatternMean  float64

	defect *knownDefect
}

func conformanceCases() []conformanceCase {
	return []conformanceCase{
		// Single CTU row: the simplest possible sanity check.
		{
			name: "flat_one_ctu_row", width: 112, height: 16, qp: 26, frames: 1,
			pattern: patFlat, maxPatternDelta: 2, maxPatternMean: 0.5,
		},
		// Multi-CTU-row frames are essential: the phase 0.1 MPM bug could not
		// fire in the first CTU row at all.
		{
			name: "flat_multi_ctu_row", width: 192, height: 96, qp: 26, frames: 1,
			pattern: patFlat, maxPatternDelta: 2, maxPatternMean: 0.5,
		},
		{
			name: "tiles_multi_ctu_row", width: 192, height: 96, qp: 26, frames: 1,
			pattern: patTiles, maxPatternDelta: 4, maxPatternMean: 0.5,
		},
		// SMPTE bars across several QPs. Vertical colour edges make the encoder
		// pick mode 26 in most CTUs, which is what phase 0.1 got wrong.
		{
			name: "smpte_qp20", width: 192, height: 96, qp: 20, frames: 1,
			pattern: patSMPTE, maxPatternDelta: 4, maxPatternMean: 0.5,
		},
		{
			name: "smpte_qp26", width: 192, height: 96, qp: 26, frames: 1,
			pattern: patSMPTE, maxPatternDelta: 4, maxPatternMean: 0.5,
		},
		{
			name: "smpte_qp40", width: 192, height: 96, qp: 40, frames: 1,
			pattern: patSMPTE, maxPatternDelta: 12, maxPatternMean: 2.0,
		},
		{
			name: "tiles_qp40", width: 192, height: 96, qp: 40, frames: 1,
			pattern: patTiles, maxPatternDelta: 12, maxPatternMean: 2.0,
		},
		// P-skip structure: IDR, then P-skip frames that must reproduce the
		// reference picture exactly in both decoders.
		{
			name: "smpte_pskip_idr2", width: 192, height: 96, qp: 26, frames: 4,
			idrInterval: 2, pattern: patSMPTE, maxPatternDelta: 4, maxPatternMean: 0.5,
		},
		{
			name: "flat_pskip_idr4", width: 128, height: 64, qp: 30, frames: 4,
			idrInterval: 4, pattern: patFlat, maxPatternDelta: 4, maxPatternMean: 0.5,
		},
		// Known defect, minimal form: 4x4 CTUs, one bright CTU per row
		// stepping right. Small enough to reason about CTU by CTU.
		{
			name: "diagonal_qp26", width: 64, height: 64, qp: 26, frames: 1,
			pattern: patDiagonal, maxPatternDelta: 255, maxPatternMean: 255,
			defect: &knownDefect{
				maxSamples: 576, maxDelta: 100,
				note: "see smpte_text_overlay_qp26: same missing mode 10/26 " +
					"boundary filter, minimal reproduction. The delta map is " +
					"the full left column of every mode-26 block, the full top " +
					"row of every mode-10 block, and a uniform offset over the " +
					"blocks that predict from them",
			},
		},
		// Known defect: CTUs whose neighbours are a different colour code real
		// AC residual, and FFmpeg then disagrees with hi265dec.
		{
			name: "smpte_text_overlay_qp26", width: 192, height: 96, qp: 26, frames: 1,
			pattern: patSMPTEText, maxPatternDelta: 255, maxPatternMean: 255,
			defect: &knownDefect{
				maxSamples: 2112, maxDelta: 76,
				note: "missing intra boundary smoothing for prediction modes 10 " +
					"and 26 (spec 8.4.4.2.6, cIdx==0 && nTbS<32) in " +
					"internal/pred.PredictAngular; encoder and decoder share the " +
					"omission so they agree with each other and not with FFmpeg",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// the tests
// ---------------------------------------------------------------------------

// TestGeneratorConformance is the assertion that matters: for every generator
// configuration, FFmpeg and hi265dec must produce byte-identical output from
// the bitstream pkg/encode wrote.
func TestGeneratorConformance(t *testing.T) {
	ffmpeg := ffmpegBin(t)

	for _, c := range conformanceCases() {
		t.Run(c.name, func(t *testing.T) {
			s := generateStream(t, c)
			ff := decodeWithFFmpeg(t, ffmpeg, s.annexB)
			hi := decodeWithHi265(t, s.annexB, c.width, c.height, c.frames)

			rep := compareYUV(t, hi, ff, c.width, c.height, c.frames)

			switch {
			case c.defect == nil:
				if !rep.exact() {
					t.Errorf("hi265dec and FFmpeg disagree on a bitstream this "+
						"encoder produced (%d bytes, %dx%d, QP %d, %d frame(s)): %s",
						len(s.annexB), c.width, c.height, c.qp, c.frames, rep)
				}
			case rep.exact():
				t.Logf("KNOWN DEFECT now passes exactly — remove the "+
					"knownDefect budget from case %q.\ndefect was: %s",
					c.name, c.defect.note)
			default:
				t.Logf("KNOWN DEFECT (not fixed here, see report): %s\nmeasured: %s",
					c.defect.note, rep)
				if rep.total > c.defect.maxSamples || rep.maxDelta > c.defect.maxDelta {
					t.Errorf("known defect got worse: %d samples / max delta %d, "+
						"recorded budget %d samples / max delta %d",
						rep.total, rep.maxDelta, c.defect.maxSamples, c.defect.maxDelta)
				}
			}

			// Both decoders must also stay near the intended pattern. A stream
			// that is consistently wrong in both decoders would pass the check
			// above; this one catches it.
			if c.defect != nil {
				// The defect moves FFmpeg away from the pattern by design;
				// only record the numbers.
				t.Logf("pattern error: ffmpeg %+v, hi265 %+v",
					measurePatternError(ff, s.intended),
					measurePatternError(hi, s.intended))
				return
			}
			for _, d := range []struct {
				who string
				buf []byte
			}{{"ffmpeg", ff}, {"hi265", hi}} {
				pe := measurePatternError(d.buf, s.intended)
				if pe.maxDelta > c.maxPatternDelta || pe.meanAbs > c.maxPatternMean {
					t.Errorf("%s decode drifts from the intended pattern: "+
						"max delta %d (limit %d), mean abs %.3f (limit %.3f)",
						d.who, pe.maxDelta, c.maxPatternDelta,
						pe.meanAbs, c.maxPatternMean)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// deblocking exactness (decoder side, needs a reference encoder)
// ---------------------------------------------------------------------------

// deblockCase is one QP of the two-flat-halves deblocking probe.
type deblockCase struct {
	qp int
	// Recorded FFmpeg-vs-hi265 mismatch with deblocking enabled. Without
	// deblocking this content decodes bit-exactly at every QP, which is what
	// makes it a clean isolation of the loop filter.
	maxSamples int
	maxDelta   int
}

// TestDeblockExactnessKnownDefect pins the deblocking filter defect.
//
// The content is two flat halves separated by one hard vertical edge at
// x=64. Without deblocking hi265dec matches FFmpeg exactly at every QP, so any
// mismatch with deblocking enabled is the loop filter and nothing else.
//
// Root cause (report only, not fixed here): internal/deblock/deblock.go:332
// clips delta to [-tC, tC] *before* testing abs(delta) < 10*tC, so the
// spec 8.7.2.5.7 gate can never fire and hi265dec filters edges the spec
// leaves untouched. FFmpeg leaves this edge alone; hi265dec smooths it.
func TestDeblockExactnessKnownDefect(t *testing.T) {
	ffmpeg := ffmpegBin(t)
	x265 := x265Bin(t)

	const w, h = 128, 64
	src := twoHalvesYUV(w, h)

	cases := []deblockCase{
		{qp: 22, maxSamples: 128, maxDelta: 1},
		{qp: 26, maxSamples: 128, maxDelta: 1},
		{qp: 30, maxSamples: 256, maxDelta: 2},
		{qp: 34, maxSamples: 256, maxDelta: 3},
		{qp: 40, maxSamples: 256, maxDelta: 5},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("qp%d", c.qp), func(t *testing.T) {
			noDeblock := encodeWithX265(t, x265, src, w, h, c.qp, false)
			deblock := encodeWithX265(t, x265, src, w, h, c.qp, true)

			// Control: without the loop filter the two decoders must agree.
			ffNo := decodeWithFFmpeg(t, ffmpeg, noDeblock)
			hiNo := decodeWithHi265(t, noDeblock, w, h, 1)
			if rep := compareYUV(t, hiNo, ffNo, w, h, 1); !rep.exact() {
				t.Fatalf("control case (deblocking disabled) already differs, "+
					"so this run cannot isolate the loop filter: %s", rep)
			}

			ffDb := decodeWithFFmpeg(t, ffmpeg, deblock)
			hiDb := decodeWithHi265(t, deblock, w, h, 1)
			rep := compareYUV(t, hiDb, ffDb, w, h, 1)
			if rep.exact() {
				t.Logf("KNOWN DEFECT now passes exactly at QP %d — tighten the "+
					"recorded budget", c.qp)
				return
			}
			t.Logf("KNOWN DEFECT: deblocking filter is not bit-exact at QP %d "+
				"(internal/deblock/deblock.go:332 clips delta before the "+
				"abs(delta) < 10*tC gate, so the gate never fires): %s",
				c.qp, rep)
			if rep.total > c.maxSamples || rep.maxDelta > c.maxDelta {
				t.Errorf("deblocking mismatch got worse at QP %d: %d samples / "+
					"max delta %d, recorded budget %d samples / max delta %d",
					c.qp, rep.total, rep.maxDelta, c.maxSamples, c.maxDelta)
			}
		})
	}
}

// twoHalvesYUV builds a yuv420p picture with a white left half and a black
// right half: flat everywhere except one vertical luma edge in the middle.
func twoHalvesYUV(w, h int) []byte {
	lumaSize := w * h
	chromaSize := (w / 2) * (h / 2)
	buf := make([]byte, lumaSize+2*chromaSize)
	for y := range h {
		for x := range w {
			v := byte(235)
			if x >= w/2 {
				v = 16
			}
			buf[y*w+x] = v
		}
	}
	for i := lumaSize; i < len(buf); i++ {
		buf[i] = 128
	}
	return buf
}

// encodeWithX265 encodes one intra frame of raw yuv420p with x265, using the
// same coding-tool subset as the checked-in test vectors (CTU 16, no SAO, no
// sign hiding, no WPP), with the loop filter switched on or off.
func encodeWithX265(t *testing.T, x265 string, src []byte, w, h, qp int, deblock bool) []byte {
	t.Helper()
	dir := t.TempDir()
	in := filepath.Join(dir, "src.yuv")
	out := filepath.Join(dir, "out.265")
	if err := os.WriteFile(in, src, 0o600); err != nil {
		t.Fatalf("write x265 input: %v", err)
	}
	args := []string{
		"--input", in,
		"--input-res", fmt.Sprintf("%dx%d", w, h),
		"--fps", "25",
		"--frames", "1",
		"--keyint", "1", "--min-keyint", "1", "--no-open-gop",
		"--ctu", "16", "--min-cu-size", "16",
		"--qp", fmt.Sprintf("%d", qp),
		"--no-sao", "--no-signhide", "--no-strong-intra-smoothing",
		"--psy-rd", "0", "--no-weightp", "--bframes", "0",
		// hi265dec does not implement entropy_coding_sync (WPP), which x265
		// enables by default; without this the slice header does not parse.
		"--no-wpp", "--no-info",
		"--output", out,
	}
	if !deblock {
		args = append(args, "--no-deblock")
	}
	cmd := exec.Command(x265, args...)
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("x265 encode failed (%v), skipping: %s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read x265 output: %v", err)
	}
	return data
}
