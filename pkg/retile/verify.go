// Verification of a stitched stream: decode it (and each input) and compare
// every tile region pixel-for-pixel. A pass proves the CABAC payloads decode
// identically in their tile positions — i.e. that the inputs were genuinely
// tileable (CTB-aligned, and motion-constrained for inter frames). MCTS is not
// visible in the bitstream, so this comparison is the only way to establish it.

package retile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/Eyevinn/hi265/pkg/decoder"
)

// Tile is one input placed at pixel (X,Y) with size W x H in the merged picture.
type Tile struct {
	Name       string // label used in messages, typically the input's file path
	Data       []byte // the standalone stream, decoded again for the comparison
	X, Y, W, H int
}

// DecoderKind selects the decoder used for verification.
type DecoderKind string

const (
	// DecoderAuto uses the in-process decoder where it can decode the stream
	// and falls back to ffmpeg where it cannot.
	DecoderAuto DecoderKind = "auto"
	// DecoderHi265 uses this repository's pkg/decoder, in-process. It needs no
	// external binary, and it refuses rather than guesses: a stream it cannot
	// decode fails verification instead of passing it.
	DecoderHi265 DecoderKind = "hi265"
	// DecoderFFmpeg shells out to ffmpeg. It is the cross-check that would
	// catch a bug shared by the stitcher and the in-process decoder, and the
	// only option for inter content.
	DecoderFFmpeg DecoderKind = "ffmpeg"
)

// interLimitation is why pkg/decoder cannot check a stitch of inter pictures.
const interLimitation = "the in-process decoder reconstructs only zero-motion skip CUs, so a picture with " +
	"real motion vectors cannot be checked; re-run with the ffmpeg decoder"

// decodeError marks a failure to decode, as opposed to a tile mismatch. Only
// the former is worth retrying with another decoder.
type decodeError struct{ err error }

func (e *decodeError) Error() string { return e.err.Error() }
func (e *decodeError) Unwrap() error { return e.err }

// decodeFunc decodes an Annex-B stream to frames concatenated yuv420p.
type decodeFunc func(annexB []byte, label string, w, h, frames int) ([]byte, error)

// Verify decodes the stitched stream and each tile's source, then compares the
// tile sub-rectangles across all frames. It returns nil only if every tile of
// every frame matches its standalone decode — never because a decoder declined
// to look. Progress is written to w, which may be nil.
func Verify(res *Result, kind DecoderKind, w io.Writer) error {
	switch kind {
	case DecoderFFmpeg:
		if _, err := ffmpegPath(); err != nil {
			return err
		}
		return verifyWith(res, decodeWithFFmpeg, "ffmpeg", w)

	case DecoderHi265:
		if res.InterSlices {
			return fmt.Errorf("cannot verify: %s", interLimitation)
		}
		return verifyWith(res, decodeWithHi265, "hi265", w)

	case DecoderAuto, "":
		if res.InterSlices {
			// Not a fallback but the only choice: refusing to verify at all
			// would be more honest than a pass nobody checked.
			if _, err := ffmpegPath(); err != nil {
				return fmt.Errorf("cannot verify a stitch of inter pictures: %s, and %w", interLimitation, err)
			}
			return verifyWith(res, decodeWithFFmpeg, "ffmpeg", w)
		}
		err := verifyWith(res, decodeWithHi265, "hi265", w)
		var de *decodeError
		if err == nil || !errors.As(err, &de) {
			return err // a pass, or a genuine tile mismatch: not ffmpeg's business
		}
		if _, ffErr := ffmpegPath(); ffErr != nil {
			return err
		}
		if w != nil {
			fmt.Fprintf(w, "  note: in-process decode failed (%v); retrying with ffmpeg\n", err)
		}
		return verifyWith(res, decodeWithFFmpeg, "ffmpeg", w)

	default:
		return fmt.Errorf("unknown decoder %q, want auto, hi265 or ffmpeg", kind)
	}
}

// verifyWith runs the whole comparison through one decoder. Both sides use the
// same one, so a decoder quirk cannot show up as a tile mismatch.
func verifyWith(res *Result, decode decodeFunc, name string, w io.Writer) error {
	merged, err := decode(res.Data, "merged stream", res.Width, res.Height, res.Frames)
	if err != nil {
		return err
	}
	for i, t := range res.Tiles {
		if t.X%2 != 0 || t.Y%2 != 0 || t.W%2 != 0 || t.H%2 != 0 {
			return fmt.Errorf("tile %d: position/size must be even for 4:2:0", i)
		}
		src, err := decode(t.Data, t.Name, t.W, t.H, res.Frames)
		if err != nil {
			return err
		}
		if f, ok := firstFrameMismatch(merged, res.Width, res.Height, src, t, res.Frames); !ok {
			return fmt.Errorf("MISMATCH tile @%d,%d %dx%d (%s) at frame %d",
				t.X, t.Y, t.W, t.H, t.Name, f)
		}
		if w != nil {
			fmt.Fprintf(w, "  verify OK  tile @%d,%d %dx%d == %s (%d frames, %s)\n",
				t.X, t.Y, t.W, t.H, t.Name, res.Frames, name)
		}
	}
	return nil
}

// firstFrameMismatch returns (frame, false) at the first differing frame, or
// (0, true) if all frames match.
func firstFrameMismatch(merged []byte, mw, mh int, src []byte, t Tile, frames int) (int, bool) {
	mFrame := mw * mh * 3 / 2
	tFrame := t.W * t.H * 3 / 2
	for f := range frames {
		m := merged[f*mFrame : (f+1)*mFrame]
		s := src[f*tFrame : (f+1)*tFrame]
		if !planeEqual(m, mw, mh, s, t.W, t.H, t.X, t.Y) {
			return f, false
		}
	}
	return 0, true
}

// planeEqual compares the W x H sub-rectangle at (x,y) of a yuv420p merged
// frame against a full yuv420p tile frame (Y, then U, then V planes).
func planeEqual(m []byte, mw, mh int, s []byte, w, h, x, y int) bool {
	// Y plane
	if !rectEqual(m, 0, mw, s, 0, w, x, y, w, h) {
		return false
	}
	// Chroma planes are half resolution in both dimensions.
	mcw, mch := mw/2, mh/2
	cw, ch, cx, cy := w/2, h/2, x/2, y/2
	mU, sU := mw*mh, w*h
	if !rectEqual(m, mU, mcw, s, sU, cw, cx, cy, cw, ch) {
		return false
	}
	mV, sV := mU+mcw*mch, sU+cw*ch
	return rectEqual(m, mV, mcw, s, sV, cw, cx, cy, cw, ch)
}

// rectEqual compares a w x h rectangle at (x,y) in a plane of stride mStride
// (offset mOff in m) against a full w x h plane of stride sStride (offset sOff).
func rectEqual(m []byte, mOff, mStride int, s []byte, sOff, sStride int, x, y, w, h int) bool {
	for row := range h {
		mi := mOff + (y+row)*mStride + x
		si := sOff + row*sStride
		if !bytes.Equal(m[mi:mi+w], s[si:si+w]) {
			return false
		}
	}
	return true
}

// decodeWithHi265 decodes in-process. This is the payoff of having tiles in
// pkg/decoder: verification needs no external binary, so it runs anywhere the
// tests run.
func decodeWithHi265(annexB []byte, label string, w, h, frames int) ([]byte, error) {
	decoded, err := decoder.New().DecodeAnnexB(annexB)
	if err != nil {
		return nil, &decodeError{fmt.Errorf("hi265 decode %s: %w", label, err)}
	}
	if len(decoded) != frames {
		return nil, &decodeError{fmt.Errorf("hi265 decode %s: got %d frames, want %d",
			label, len(decoded), frames)}
	}
	out := make([]byte, 0, w*h*3/2*frames)
	for i, f := range decoded {
		if f.Width != w || f.Height != h {
			return nil, &decodeError{fmt.Errorf("hi265 decode %s: frame %d is %dx%d, want %dx%d",
				label, i, f.Width, f.Height, w, h)}
		}
		out = append(out, f.YUV420Bytes()...)
	}
	return out, nil
}

// ffmpegPath returns the ffmpeg binary to use. HI265_FFMPEG overrides the one
// found on PATH, matching the convention the conformance tests use.
func ffmpegPath() (string, error) {
	if p := os.Getenv("HI265_FFMPEG"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", fmt.Errorf("verify needs ffmpeg in PATH: %w", err)
	}
	return p, nil
}

// decodeWithFFmpeg decodes with ffmpeg, which reads the Annex-B stream from
// stdin so nothing has to be written to disk first.
func decodeWithFFmpeg(annexB []byte, label string, w, h, frames int) ([]byte, error) {
	bin, err := ffmpegPath()
	if err != nil {
		return nil, err
	}
	var out, errb bytes.Buffer
	cmd := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-f", "hevc", "-i", "-", "-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
	cmd.Stdin = bytes.NewReader(annexB)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, &decodeError{fmt.Errorf("ffmpeg decode %s: %v: %s", label, err, errb.String())}
	}
	want := w * h * 3 / 2 * frames
	if out.Len() != want {
		return nil, &decodeError{fmt.Errorf("ffmpeg decode %s: got %d bytes, want %d (%dx%d, %d frames)",
			label, out.Len(), want, w, h, frames)}
	}
	return out.Bytes(), nil
}
