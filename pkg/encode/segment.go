package encode

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/tiles"
)

// segment says which slice segment of a picture is being written, and how its
// slice data is cut into CABAC substreams.
//
// A picture with tiles is emitted as one independent slice segment per tile,
// which is the shape that needs no entry point offsets: CABAC initialises and
// terminates at a segment boundary anyway, so each tile's contexts start clean
// without any substream bookkeeping. Wavefront parallel processing is the other
// case, and the one that does need them — see the wpp field.
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
	// num_entry_point_offsets. It is zero for one tile per segment, and one per
	// CTB row after the first under wpp.
	entryPointOffsetsPresent bool
	// wpp mirrors entropy_coding_sync_enabled_flag: every CTB row of the segment
	// is its own CABAC substream, ended by an end_of_subset_one_bit and byte
	// alignment, and its contexts come from a snapshot taken after the second CTB
	// of the row above rather than from where that row left off (spec 9.3.1).
	//
	// Tiles are never set alongside it: no HEVC profile permits the combination,
	// and the decoder refuses it too.
	wpp bool
}

// wholePicture returns the single segment of an untiled picture.
func wholePicture(width, height, ctuSize int, wpp bool) segment {
	ctbsX := (width + ctuSize - 1) / ctuSize
	ctbsY := (height + ctuSize - 1) / ctuSize
	return segment{
		region:                   tiles.Region{ColStart: 0, ColEnd: ctbsX, RowStart: 0, RowEnd: ctbsY},
		first:                    true,
		ctbsX:                    ctbsX,
		entryPointOffsetsPresent: wpp,
		wpp:                      wpp,
	}
}

// tileSegments returns the segments a picture described by these parameter sets
// is emitted as: one per tile, in tile order, or a single one when tiles are off.
// With entropy_coding_sync_enabled_flag that single segment is cut into one
// substream per CTB row instead — the callers refuse the two together.
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
			wpp:                      pps.EntropyCodingSyncEnabledFlag,
		})
	}
	return segs, nil
}

// segmentsForGrid returns the segments a generated picture is emitted as: one
// per tile of a uniform cols x rows grid, or a single one when there are no
// tiles. With wpp that single segment carries one substream per CTB row. It is
// the counterpart of tileSegments for the parameter sets this package writes
// itself.
func segmentsForGrid(width, height, ctuSize, cols, rows int, wpp bool) ([]segment, error) {
	ctbsX := (width + ctuSize - 1) / ctuSize
	ctbsY := (height + ctuSize - 1) / ctuSize
	if cols < 2 && rows < 2 {
		return []segment{wholePicture(width, height, ctuSize, wpp)}, nil
	}
	if wpp {
		// EncodeParams.validateParallelism catches this before either the
		// parameter sets or the slice data are written; this is the backstop for
		// a caller reaching segmentsForGrid directly.
		return nil, fmt.Errorf("tiles combined with wavefront parallel processing is not supported")
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

// writeEntryPointOffsets writes num_entry_point_offsets and the offsets
// themselves where the header carries them (spec 7.3.6.1). subs is the slice
// data this segment turned out to be, one entry per substream, so the header has
// to be written after the data it describes.
//
// Each coded value is the length of one substream rather than a position:
// entry_point_offset_minus1[i] plus one is the size of subset i, and the last
// subset needs no offset since it runs to the end of the segment. The lengths are
// counted after emulation prevention, which spec 7.4.7.1 includes in them. That
// can be done per substream because every one but the last ends with the one bit
// of its byte alignment, so no zero run ever crosses a substream boundary.
func (s segment) writeEntryPointOffsets(w *BitWriter, subs [][]byte) {
	if !s.entryPointOffsetsPresent {
		return
	}
	w.WriteUE(uint32(len(subs) - 1)) // num_entry_point_offsets
	if len(subs) < 2 {
		return
	}

	offsets := make([]uint32, 0, len(subs)-1)
	var maxOffset uint32
	for _, sub := range subs[:len(subs)-1] {
		v := uint32(ebspLen(sub) - 1)
		offsets = append(offsets, v)
		maxOffset = max(maxOffset, v)
	}
	offsetLen := max(ceilLog2(int(maxOffset)+1), 1)

	w.WriteUE(uint32(offsetLen - 1)) // offset_len_minus1
	for _, v := range offsets {
		w.WriteBits(v, offsetLen) // entry_point_offset_minus1[i]
	}
}

// ebspLen returns how long b becomes once emulation prevention bytes are
// inserted, which is the unit spec 7.4.7.1 measures entry point offsets in. It
// counts exactly what InsertEBSP would add, without building the result.
func ebspLen(b []byte) int {
	n := len(b)
	zeros := 0
	for _, v := range b {
		if zeros >= 2 && v <= 0x03 {
			n++
			zeros = 0
		}
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return n
}

// appendSubstreams joins the slice segment header to the slice data that follows
// it. Concatenated, the substreams are the slice data; the header's entry point
// offsets say where each one starts.
func appendSubstreams(header []byte, subs [][]byte) []byte {
	n := 0
	for _, sub := range subs {
		n += len(sub)
	}
	rbsp := make([]byte, 0, len(header)+n)
	rbsp = append(rbsp, header...)
	for _, sub := range subs {
		rbsp = append(rbsp, sub...)
	}
	return rbsp
}

// firstFlag returns first_slice_segment_in_pic_flag as a bit.
func (s segment) firstFlag() uint8 {
	if s.first {
		return 1
	}
	return 0
}

// encodeCtbs codes every CTB of the segment in the order they appear in the
// bitstream — raster order within the segment's own rectangle — calling body for
// each with the arithmetic coder and the context models it must use, and writes
// the syntax that separates them: end_of_slice_segment_flag after every CTB, and
// under wpp an end_of_subset_one_bit and byte alignment at the end of each row.
//
// It returns the substreams the slice data is made of, which is one without wpp
// and one per CTB row with it. Concatenated they are the slice data; their
// lengths are what writeEntryPointOffsets puts in the header.
func (s segment) encodeCtbs(ctuSize, sliceType, qp int,
	body func(enc *cabac.Encoder, models []cabac.CtxState, x, y int)) [][]byte {

	var subs [][]byte
	enc := cabac.NewEncoder()
	models := context.InitModels(sliceType, qp)
	// sync is the snapshot taken after the second CTB of a row, which is what the
	// first CTB of the next row starts from (spec 9.3.1). A segment one CTB wide
	// never takes one, and every row then starts from the initial values instead —
	// the same fallback the decoder makes when the above-right CTB is unavailable.
	var sync []cabac.CtxState

	total := s.region.NumCtbs()
	coded := 0
	for row := s.region.RowStart; row < s.region.RowEnd; row++ {
		for col := s.region.ColStart; col < s.region.ColEnd; col++ {
			body(enc, models, col*ctuSize, row*ctuSize)
			coded++
			if s.wpp && col == s.region.ColStart+1 {
				sync = append(sync[:0], models...)
			}
			// end_of_slice_segment_flag
			enc.EncodeTerminate(b2u(coded == total))
		}

		// A row under wpp ends its substream here, unless the segment ended with
		// it. The terminating bin flushes the arithmetic coder, whose last bit is
		// the one byte_alignment() asks for, so Flush's zero padding completes the
		// alignment the next entry point sits on. The next row then starts a fresh
		// engine seeded from the row above's snapshot.
		if s.wpp && coded < total {
			enc.EncodeTerminate(1) // end_of_subset_one_bit
			subs = append(subs, enc.Flush())

			enc = cabac.NewEncoder()
			if sync != nil {
				models = append(models[:0], sync...)
			} else {
				models = context.InitModels(sliceType, qp)
			}
		}
	}
	return append(subs, enc.Flush())
}
