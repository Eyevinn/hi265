package encode

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/tiles"
)

// segment says which slice segment of a picture is being written. A picture with
// tiles is emitted as one independent slice segment per tile, which is the shape
// that needs no entry point offsets: CABAC initialises and terminates at a
// segment boundary anyway, so each tile's contexts start clean without any
// substream bookkeeping.
//
// The zero value describes a picture that is a single segment covering
// everything, which is what the generated parameter sets always produce.
type segment struct {
	// region is the rectangle of CTBs this segment covers. A zero region means
	// the whole picture; wholePicture fills it in.
	region tiles.Region
	// first is first_slice_segment_in_pic_flag.
	first bool
	// ctbsX is the picture width in CTBs, which turns the region's corner into a
	// raster scan address.
	ctbsX int
	// addressBits is Ceil(Log2(PicSizeInCtbsY)), the width of
	// slice_segment_address.
	addressBits int
	// dependentSlicesEnabled mirrors dependent_slice_segments_enabled_flag: when
	// set, every segment after the first writes a dependent flag of 0 before its
	// address. Nothing here emits dependent segments.
	dependentSlicesEnabled bool
	// entryPointOffsetsPresent mirrors tiles_enabled_flag ||
	// entropy_coding_sync_enabled_flag, which is when the header carries
	// num_entry_point_offsets — zero, for one tile per segment.
	entryPointOffsetsPresent bool
}

// wholePicture returns the single segment of an untiled picture.
func wholePicture(width, height, ctuSize int) segment {
	ctbsX := (width + ctuSize - 1) / ctuSize
	ctbsY := (height + ctuSize - 1) / ctuSize
	return segment{
		region: tiles.Region{ColStart: 0, ColEnd: ctbsX, RowStart: 0, RowEnd: ctbsY},
		first:  true,
		ctbsX:  ctbsX,
	}
}

// tileSegments returns the segments a picture described by these parameter sets
// is emitted as: one per tile, in tile order, or a single one when tiles are off.
func tileSegments(sps *hevc.SPS, pps *hevc.PPS) ([]segment, error) {
	grid, err := tiles.FromPPS(sps, pps)
	if err != nil {
		return nil, err
	}
	ctbsX, ctbsY := tiles.PicSizeInCtbs(sps)
	addressBits := ceilLog2(ctbsX * ctbsY)
	entryPoints := pps.TilesEnabledFlag || pps.EntropyCodingSyncEnabledFlag

	regions := grid.Regions()
	segs := make([]segment, 0, len(regions))
	for i, r := range regions {
		segs = append(segs, segment{
			region:                   r,
			first:                    i == 0,
			ctbsX:                    ctbsX,
			addressBits:              addressBits,
			dependentSlicesEnabled:   pps.DependentSliceSegmentsEnabledFlag,
			entryPointOffsetsPresent: entryPoints,
		})
	}
	return segs, nil
}

// segmentsForGrid returns the segments a generated picture is emitted as: one
// per tile of a uniform cols x rows grid, or a single one when there are no
// tiles. It is the counterpart of tileSegments for the parameter sets this
// package writes itself.
func segmentsForGrid(width, height, ctuSize, cols, rows int) ([]segment, error) {
	ctbsX := (width + ctuSize - 1) / ctuSize
	ctbsY := (height + ctuSize - 1) / ctuSize
	if cols < 2 && rows < 2 {
		return []segment{wholePicture(width, height, ctuSize)}, nil
	}
	grid, err := tiles.New(ctbsX, ctbsY,
		tiles.UniformSizes(ctbsX, cols), tiles.UniformSizes(ctbsY, rows))
	if err != nil {
		return nil, fmt.Errorf("%dx%d tiles over %dx%d CTBs: %w", cols, rows, ctbsX, ctbsY, err)
	}

	addressBits := ceilLog2(ctbsX * ctbsY)
	regions := grid.Regions()
	segs := make([]segment, 0, len(regions))
	for i, r := range regions {
		segs = append(segs, segment{
			region:                   r,
			first:                    i == 0,
			ctbsX:                    ctbsX,
			addressBits:              addressBits,
			entryPointOffsetsPresent: true, // tiles are on
		})
	}
	return segs, nil
}

// writeAddress writes the fields that follow slice_pic_parameter_set_id for a
// segment other than the first of a picture (spec 7.3.6.1).
func (s segment) writeAddress(w *BitWriter) {
	if s.first {
		return
	}
	if s.dependentSlicesEnabled {
		w.WriteBit(0) // dependent_slice_segment_flag = 0
	}
	w.WriteBits(uint32(s.region.FirstCtbRS(s.ctbsX)), s.addressBits)
}

// writeEntryPointOffsets writes num_entry_point_offsets where the header carries
// it. One tile per segment means there is never a second substream.
func (s segment) writeEntryPointOffsets(w *BitWriter) {
	if s.entryPointOffsetsPresent {
		w.WriteUE(0) // num_entry_point_offsets = 0
	}
}

// firstFlag returns first_slice_segment_in_pic_flag as a bit.
func (s segment) firstFlag() uint8 {
	if s.first {
		return 1
	}
	return 0
}

// forEachCtb calls f for every CTB of the segment in raster order within its
// tile, which is the order they are coded in, and reports whether each is the
// segment's last — where end_of_slice_segment_flag is 1.
func (s segment) forEachCtb(ctuSize int, f func(x, y int, last bool)) {
	total := s.region.NumCtbs()
	i := 0
	for row := s.region.RowStart; row < s.region.RowEnd; row++ {
		for col := s.region.ColStart; col < s.region.ColEnd; col++ {
			i++
			f(col*ctuSize, row*ctuSize, i == total)
		}
	}
}
