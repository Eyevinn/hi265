// Verification of a stitched stream: decode it (and each input) and compare
// every tile region pixel-for-pixel. A pass proves the CABAC payloads decode
// identically in their tile positions — i.e. that the inputs were genuinely
// tileable (CTB-aligned, and motion-constrained for inter frames).

package retile

import (
	"bytes"
	"fmt"
	"os/exec"
)

// Tile is one input placed at pixel (X,Y) with size W x H in the merged picture.
type Tile struct {
	Path       string
	X, Y, W, H int
}

// Verify decodes mergedPath and each tile's source to yuv420p and compares the
// tile sub-rectangles across all frames. It returns nil only if every tile of
// every frame matches its standalone decode.
func Verify(mergedPath string, mergedW, mergedH, frames int, tiles []Tile) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("verify needs ffmpeg in PATH: %w", err)
	}
	merged, err := decodeYUV420(mergedPath, mergedW, mergedH, frames)
	if err != nil {
		return err
	}
	for i, t := range tiles {
		if t.X%2 != 0 || t.Y%2 != 0 || t.W%2 != 0 || t.H%2 != 0 {
			return fmt.Errorf("tile %d: position/size must be even for 4:2:0", i)
		}
		src, err := decodeYUV420(t.Path, t.W, t.H, frames)
		if err != nil {
			return err
		}
		if f, ok := firstFrameMismatch(merged, mergedW, mergedH, src, t, frames); !ok {
			return fmt.Errorf("MISMATCH tile @%d,%d %dx%d (%s) at frame %d",
				t.X, t.Y, t.W, t.H, t.Path, f)
		}
		fmt.Printf("  verify OK  tile @%d,%d %dx%d == %s (%d frames)\n",
			t.X, t.Y, t.W, t.H, t.Path, frames)
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

func decodeYUV420(path string, w, h, frames int) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-f", "rawvideo", "-pix_fmt", "yuv420p", "-")
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s: %v: %s", path, err, errb.String())
	}
	want := w * h * 3 / 2 * frames
	if out.Len() != want {
		return nil, fmt.Errorf("decode %s: got %d bytes, want %d (%dx%d, %d frames)",
			path, out.Len(), want, w, h, frames)
	}
	return out.Bytes(), nil
}
