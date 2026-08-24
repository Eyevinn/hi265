// Package retile rewrites HEVC parameter sets and slice headers to stitch
// several independently-coded videos into one tiled picture, copying the
// CABAC slice payloads verbatim. No pixel is re-encoded.
//
// It parses with mp4ff (github.com/Eyevinn/mp4ff/hevc) and writes bits with
// hi265's pkg/encode (BitWriter, InsertEBSP, RemoveEmulationPrevention).
//
// Inputs must be CTB-aligned, share all coding tools (one merged SPS/PPS
// describes every tile), have tiles and WPP disabled, no slice-segment-header
// extension, and be all-intra or motion-constrained (MCTS) for inter frames.
package retile

import (
	"fmt"

	"github.com/Eyevinn/hi265/pkg/encode"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

func b2u(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

// ceilLog2 returns the smallest k with (1<<k) >= n, matching Ceil(Log2(n)).
func ceilLog2(n uint) int {
	k := 0
	for (uint(1) << k) < n {
		k++
	}
	return k
}

func ceilDiv(a, b uint) uint { return (a + b - 1) / b }

// SplitAnnexB parses an Annex-B byte stream into NAL units (2-byte header plus
// EBSP payload), stripping start codes and any trailing zero padding.
//
// mp4ff's avc.ExtractNalusFromByteStream does the splitting; the extra trim
// here covers the final NAL, whose trailing_zero_8bits mp4ff leaves attached.
// That matters because a slice payload is copied verbatim into the output, so
// stray zero bytes would end up inside the restitched NAL.
func SplitAnnexB(data []byte) [][]byte {
	var out [][]byte
	for _, n := range avc.ExtractNalusFromByteStream(data) {
		for len(n) > 0 && n[len(n)-1] == 0 {
			n = n[:len(n)-1]
		}
		if len(n) >= 2 {
			out = append(out, n)
		}
	}
	return out
}

// CtbSizeY returns the luma CTB size for an SPS.
func CtbSizeY(sps *hevc.SPS) uint {
	return uint(1) << (sps.Log2MinLumaCodingBlockSizeMinus3 + 3 + sps.Log2DiffMaxMinLumaCodingBlockSize)
}

// PicSizeInCtbs returns PicWidthInCtbsY * PicHeightInCtbsY for an SPS.
func PicSizeInCtbs(sps *hevc.SPS) uint {
	c := CtbSizeY(sps)
	return ceilDiv(uint(sps.PicWidthInLumaSamples), c) * ceilDiv(uint(sps.PicHeightInLumaSamples), c)
}

// PicWidthInCtbs returns PicWidthInCtbsY for an SPS.
func PicWidthInCtbs(sps *hevc.SPS) uint {
	return ceilDiv(uint(sps.PicWidthInLumaSamples), CtbSizeY(sps))
}

// meaningfulBits returns the number of RBSP bits before rbsp_trailing_bits (or
// before a slice header's byte_alignment), using the rule "the stop-one-bit is
// the lowest set bit of the last non-zero byte".
func meaningfulBits(rbsp []byte) int {
	last := len(rbsp) - 1
	for last >= 0 && rbsp[last] == 0 {
		last--
	}
	if last < 0 {
		return 0
	}
	low := 0
	for (rbsp[last]>>low)&1 == 0 {
		low++
	}
	return last*8 + (7 - low)
}

func copyBits(w *encode.BitWriter, r *encode.BitReader, n int) {
	for range n {
		w.WriteBit(uint8(r.ReadBit()))
	}
}

func writeTrailing(w *encode.BitWriter) {
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}
}

func assemble(srcHeader, rbsp []byte) []byte {
	ebsp := encode.InsertEBSP(rbsp)
	out := make([]byte, 0, 2+len(ebsp))
	out = append(out, srcHeader[0], srcHeader[1])
	out = append(out, ebsp...)
	return out
}

// skipProfileTierLevel advances r past a profile_tier_level(1, maxSubLayersM1).
// general_level_idc (the 8 bits at offset 88 within the PTL) is left in place;
// callers patch it separately since it is always byte-aligned in the SPS.
func skipProfileTierLevel(r *encode.BitReader, maxSubLayersM1 uint) {
	r.SkipBits(88) // general profile_space/tier/profile_idc/compat/constraint
	r.SkipBits(8)  // general_level_idc
	subProfile := make([]bool, maxSubLayersM1)
	subLevel := make([]bool, maxSubLayersM1)
	for i := uint(0); i < maxSubLayersM1; i++ {
		subProfile[i] = r.ReadBit() == 1 // sub_layer_profile_present_flag[i]
		subLevel[i] = r.ReadBit() == 1   // sub_layer_level_present_flag[i]
	}
	if maxSubLayersM1 > 0 {
		for i := maxSubLayersM1; i < 8; i++ {
			r.SkipBits(2) // reserved_zero_2bits[i]
		}
	}
	for i := uint(0); i < maxSubLayersM1; i++ {
		if subProfile[i] {
			r.SkipBits(88) // sub_layer profile_space..constraint
		}
		if subLevel[i] {
			r.SkipBits(8) // sub_layer_level_idc[i]
		}
	}
}

// RewriteSPS returns a new SPS NAL with pic_width/pic_height set to newW/newH
// and general_level_idc set to newLevel.
func RewriteSPS(spsNAL []byte, newW, newH int, newLevel, oldLevel byte) ([]byte, error) {
	rbsp := encode.RemoveEmulationPrevention(spsNAL[2:])

	r := encode.NewBitReader(rbsp)
	r.SkipBits(4) // sps_video_parameter_set_id
	maxSubLayersM1 := r.ReadBits(3)
	r.SkipBits(1) // sps_temporal_id_nesting_flag
	skipProfileTierLevel(r, maxSubLayersM1)
	r.ReadUE() // sps_seq_parameter_set_id
	if chroma := r.ReadUE(); chroma == 3 {
		r.SkipBits(1) // separate_colour_plane_flag
	}
	widthStart := r.Pos()
	r.ReadUE() // old width
	r.ReadUE() // old height
	afterHeight := r.Pos()

	// general_level_idc is byte-aligned at RBSP byte 12 (8 prefix + 8+32+48 bits).
	// That offset holds only because the general part of profile_tier_level is a
	// fixed 96 bits; it is not a general "level lives here" rule.
	if len(rbsp) <= 12 || rbsp[12] != oldLevel {
		got := byte(0)
		if len(rbsp) > 12 {
			got = rbsp[12]
		}
		return nil, fmt.Errorf("RewriteSPS: expected general_level_idc=%d at byte 12, got %d", oldLevel, got)
	}
	rbsp[12] = newLevel

	meaningful := meaningfulBits(rbsp)
	w := encode.NewBitWriter()
	rc := encode.NewBitReader(rbsp)
	copyBits(w, rc, widthStart) // verbatim prefix (incl. patched level)
	w.WriteUE(uint32(newW))     // pic_width_in_luma_samples
	w.WriteUE(uint32(newH))     // pic_height_in_luma_samples
	rc.SkipBits(afterHeight - widthStart)
	copyBits(w, rc, meaningful-afterHeight)
	writeTrailing(w)

	return assemble(spsNAL, w.Bytes()), nil
}

// TileGrid describes a tile layout in units of CTBs. ColWidths has one entry
// per tile column, RowHeights one per tile row.
type TileGrid struct {
	ColWidths  []uint
	RowHeights []uint
}

func allEqual(v []uint) bool {
	for _, x := range v {
		if x != v[0] {
			return false
		}
	}
	return true
}

// RewritePPS returns a new PPS NAL with tiles enabled describing grid, with
// loop_filter_across_tiles_enabled_flag = 0. The input PPS must have tiles
// disabled. Uniform spacing is used when all columns and all rows are equal.
func RewritePPS(ppsNAL []byte, grid TileGrid) ([]byte, error) {
	cols, rows := len(grid.ColWidths), len(grid.RowHeights)
	if cols < 1 || rows < 1 {
		return nil, fmt.Errorf("RewritePPS: empty grid")
	}
	rbsp := encode.RemoveEmulationPrevention(ppsNAL[2:])

	r := encode.NewBitReader(rbsp)
	r.ReadUE()    // pps_pic_parameter_set_id
	r.ReadUE()    // pps_seq_parameter_set_id
	r.SkipBits(1) // dependent_slice_segments_enabled_flag
	r.SkipBits(1) // output_flag_present_flag
	r.SkipBits(3) // num_extra_slice_header_bits
	r.SkipBits(1) // sign_data_hiding_enabled_flag
	r.SkipBits(1) // cabac_init_present_flag
	r.ReadUE()    // num_ref_idx_l0_default_active_minus1
	r.ReadUE()    // num_ref_idx_l1_default_active_minus1
	r.ReadSE()    // init_qp_minus26
	r.SkipBits(1) // constrained_intra_pred_flag
	r.SkipBits(1) // transform_skip_enabled_flag
	if r.ReadBit() == 1 {
		r.ReadUE() // diff_cu_qp_delta_depth
	}
	r.ReadSE()    // pps_cb_qp_offset
	r.ReadSE()    // pps_cr_qp_offset
	r.SkipBits(1) // pps_slice_chroma_qp_offsets_present_flag
	r.SkipBits(1) // weighted_pred_flag
	r.SkipBits(1) // weighted_bipred_flag
	r.SkipBits(1) // transquant_bypass_enabled_flag

	tilesPos := r.Pos()
	if r.ReadBit() != 0 {
		return nil, fmt.Errorf("RewritePPS: input PPS already has tiles enabled")
	}
	entropySync := r.ReadBit()
	resumePos := r.Pos()

	meaningful := meaningfulBits(rbsp)
	w := encode.NewBitWriter()
	rc := encode.NewBitReader(rbsp)
	copyBits(w, rc, tilesPos)
	w.WriteBit(1) // tiles_enabled_flag = 1
	rc.SkipBits(1)
	w.WriteBit(uint8(entropySync))
	rc.SkipBits(1)

	w.WriteUE(uint32(cols - 1)) // num_tile_columns_minus1
	w.WriteUE(uint32(rows - 1)) // num_tile_rows_minus1
	uniform := allEqual(grid.ColWidths) && allEqual(grid.RowHeights)
	w.WriteBit(b2u(uniform)) // uniform_spacing_flag
	if !uniform {
		for i := range cols - 1 {
			w.WriteUE(uint32(grid.ColWidths[i] - 1)) // column_width_minus1[i]
		}
		for i := range rows - 1 {
			w.WriteUE(uint32(grid.RowHeights[i] - 1)) // row_height_minus1[i]
		}
	}
	w.WriteBit(0) // loop_filter_across_tiles_enabled_flag = 0

	copyBits(w, rc, meaningful-resumePos)
	writeTrailing(w)

	return assemble(ppsNAL, w.Bytes()), nil
}

// SliceParams describes how to place one input slice into the merged picture.
type SliceParams struct {
	FirstSlice     bool // first_slice_segment_in_pic_flag
	SegmentAddress uint // slice_segment_address (when !FirstSlice)
}

// BuildSliceNAL rewrites an input slice NAL for the merged tiled picture. The
// slice-type-specific body of the header (slice_type, POC, RPS, ref lists, QP,
// ...) is copied verbatim, so I/P/B all work; only the leading fields
// (first_slice flag, slice_segment_address) are re-emitted and a trailing
// num_entry_point_offsets=0 is appended. The CABAC payload is copied as-is.
//
// The payload is byte-aligned because byte_alignment() ends every slice header,
// so it is copied as bytes and must never be bit-shifted. Copying rather than
// reparsing is what makes I/P/B all work without reimplementing RPS and
// reference-list parsing; do not refactor it into full parsing.
//
// The input slice must have no entry-point offsets (tiles & WPP off) and no
// slice_segment_header_extension.
func BuildSliceNAL(srcNAL []byte, sh *hevc.SliceHeader, mergedSPS *hevc.SPS,
	mergedPPS *hevc.PPS, p SliceParams) ([]byte, error) {
	naluType := hevc.GetNaluType(srcNAL[0])
	isIRAP := naluType >= hevc.NALU_BLA_W_LP && naluType <= hevc.NALU_IRAP_VCL23

	rbsp := encode.RemoveEmulationPrevention(srcNAL[2:])
	// sh.Size is the byte-aligned header size *including* the 2-byte NAL header.
	// De-escape just the header region so the payload boundary stays correct
	// even when an emulation-prevention byte sits inside the header.
	headerLen := len(encode.RemoveEmulationPrevention(srcNAL[2:int(sh.Size)]))
	hdr := rbsp[:headerLen]
	payload := rbsp[headerLen:]

	// P1: bit position just after slice_pic_parameter_set_id in the original.
	rr := encode.NewBitReader(hdr)
	rr.ReadBit() // first_slice_segment_in_pic_flag
	if isIRAP {
		rr.ReadBit() // no_output_of_prior_pics_flag
	}
	rr.ReadUE() // slice_pic_parameter_set_id
	p1 := rr.Pos()
	// P2: end of meaningful header bits (before byte_alignment).
	p2 := meaningfulBits(hdr)

	w := encode.NewBitWriter()
	w.WriteBit(b2u(p.FirstSlice))
	if isIRAP {
		w.WriteBit(b2u(sh.NoOutputOfPriorPicsFlag))
	}
	w.WriteUE(mergedPPS.PicParameterSetID)
	if !p.FirstSlice {
		if mergedPPS.DependentSliceSegmentsEnabledFlag {
			w.WriteBit(0) // dependent_slice_segment_flag = 0
		}
		w.WriteBits(uint32(p.SegmentAddress), ceilLog2(PicSizeInCtbs(mergedSPS)))
	}
	// Copy the slice-type-specific fields verbatim.
	rc := encode.NewBitReader(hdr)
	rc.SkipBits(p1)
	copyBits(w, rc, p2-p1)
	if mergedPPS.TilesEnabledFlag || mergedPPS.EntropyCodingSyncEnabledFlag {
		w.WriteUE(0) // num_entry_point_offsets = 0 (one tile per slice)
	}
	writeTrailing(w)

	newRBSP := append(w.Bytes(), payload...)
	return assemble(srcNAL, newRBSP), nil
}
