package decoder

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/deblock"
	"github.com/Eyevinn/hi265/internal/loopfilter"
	"github.com/Eyevinn/hi265/internal/sao"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/tiles"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// picture is a coded picture under construction. A picture is one or more slice
// segments (spec 7.4.7.1): tiled streams put one segment per tile, and plain
// multi-slice streams cut the CTB raster into runs. Reconstruction happens
// segment by segment into the same buffer, but the loop filters, the reference
// frame update and the conformance window crop all belong to the finished
// picture, so they wait until the last segment is in.
type picture struct {
	f           *frame.Frame
	sps         *hevc.SPS
	grid        *tiles.Grid
	log2CtbSize int
	// bounds knows which tile and which slice every CTB belongs to, and each
	// slice's loop filter parameters, so the two filters can stop where the
	// spec says they must.
	bounds *loopfilter.Boundaries

	// cus accumulates every CU of the picture, which is what the deblocking
	// filter needs: it works on a picture-wide 4x4 edge grid.
	cus []slice.CodingUnit
	// saoParams is picture-sized and shared with every segment, so a segment
	// can merge SAO parameters from a CTB an earlier one decoded.
	saoParams   []slice.SaoParams
	ctbsDecoded int

	// slice, sliceIdx and sliceState describe the slice currently being decoded,
	// which a dependent slice segment continues: its header fields, its index in
	// bounds, and the CABAC contexts, QP prediction and neighbour maps it has
	// built up so far.
	slice      *sliceSegment
	sliceIdx   int
	sliceState *slice.State

	// sliceQPY is the first segment's, and is only the value the deblocking
	// filter prefills its QP map with; every block a CU covers overrides it
	// with that CU's own QP.
	sliceQPY int
	// saoEnabled is set when any slice of the picture enabled SAO.
	saoEnabled bool
}

// startPicture opens a new picture for a segment carrying
// first_slice_segment_in_pic_flag.
func (d *Decoder) startPicture(seg *sliceSegment) error {
	sps := seg.sps
	grid, err := tiles.FromPPS(sps, seg.pps)
	if err != nil {
		return err
	}
	// With tiles disabled there are no tile boundaries to stop at, and the PPS
	// flag is absent; a single-tile grid makes the question moot either way.
	acrossTiles := !seg.pps.TilesEnabledFlag || seg.pps.LoopFilterAcrossTilesEnabledFlag

	d.pic = &picture{
		f:           frame.NewFrame(int(sps.PicWidthInLumaSamples), int(sps.PicHeightInLumaSamples)),
		sps:         sps,
		grid:        grid,
		log2CtbSize: log2CtbSize(sps),
		bounds:      loopfilter.New(grid, log2CtbSize(sps), acrossTiles),
		saoParams:   make([]slice.SaoParams, grid.NumCtbs()),
		sliceQPY:    seg.params.SliceQPY,
	}
	return nil
}

// startSegment prepares the picture for the next slice segment.
//
// Clearing the reconstruction map is what keeps neighbour availability
// per-segment. Spec 6.4.1 makes a neighbour unavailable when it lies in another
// slice or another tile, and the map is what the intra prediction availability
// test consults; a sample reconstructed by an earlier segment must not be
// predicted from even though it sits in this same buffer. Reconstruction clears
// it again at every tile boundary inside the segment, for the same reason.
func (p *picture) startSegment() {
	clearAvailability(p.f)
}

// clearAvailability marks every sample of the picture as not-yet-reconstructed
// for the purpose of neighbour availability. The samples themselves stay put.
func clearAvailability(f *frame.Frame) {
	for i := range f.LumaDecoded {
		f.LumaDecoded[i] = false
	}
}

// finishPicture runs the picture-level loop filters, publishes the result and
// makes it the reference for what follows. It is a no-op when no picture is
// open, so callers can use it to close whatever came before.
func (d *Decoder) finishPicture(frames *[]*frame.Frame) error {
	pic := d.pic
	if pic == nil {
		return nil
	}
	d.pic = nil

	if want := pic.grid.NumCtbs(); pic.ctbsDecoded != want {
		return fmt.Errorf("picture covered by %d CTBs, expected %d: slice segments do not tile the picture",
			pic.ctbsDecoded, want)
	}

	if pic.bounds.AnyDeblocking() {
		deblock.Apply(pic.f, pic.cus, pic.sliceQPY, pic.bounds)
	}
	if pic.saoEnabled {
		sao.Apply(pic.f, pic.saoParams, pic.log2CtbSize, pic.bounds)
	}

	// Later pictures predict from the full coded picture, but what comes out of
	// the decoder is the conformance window.
	d.refFrame = pic.f
	*frames = append(*frames, cropToConformanceWindow(pic.f, pic.sps))
	return nil
}

// log2CtbSize and picSizeInCtbs are the parameter set derivations, which live in
// internal/tiles so the encoder reads the same syntax the same way.
func log2CtbSize(sps *hevc.SPS) int { return tiles.Log2CtbSize(sps) }

func picSizeInCtbs(sps *hevc.SPS) (ctbsX, ctbsY int) { return tiles.PicSizeInCtbs(sps) }
