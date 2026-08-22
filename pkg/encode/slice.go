package encode

import (
	"fmt"

	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/pred"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/internal/transform"
	"github.com/Eyevinn/hi265/pkg/frame"
	"github.com/Eyevinn/mp4ff/hevc"
)

// intraModes records the intra luma mode of coded blocks at 4x4 granularity,
// mirroring the decoder's map. Keying by CU index instead would break as soon as
// CUs of two sizes appear in one picture, which is exactly what a partial CTU
// row at the bottom edge produces.
type intraModes struct {
	m map[[2]int]int
}

func newIntraModes() *intraModes {
	return &intraModes{m: make(map[[2]int]int)}
}

func (t *intraModes) set(x0, y0, size, mode int) {
	for y := y0; y < y0+size; y += 4 {
		for x := x0; x < x0+size; x += 4 {
			t.m[[2]int{x / 4, y / 4}] = mode
		}
	}
}

// get returns the mode covering pixel (x, y), or -1 when nothing was coded there.
func (t *intraModes) get(x, y int) int {
	if x < 0 || y < 0 {
		return -1
	}
	if m, ok := t.m[[2]int{x / 4, y / 4}]; ok {
		return m
	}
	return -1
}

// ctuSizeLuma is the coding tree block size the generator always uses.
const ctuSizeLuma = 16

// codingLayout is the coding block geometry chosen for a picture. It is derived
// in exactly one place so the SPS and the slice data cannot disagree about it.
type codingLayout struct {
	ctuSize       int
	minCbSize     int
	log2CtuSize   int
	log2MinCbSize int
	// splitToMin means every CU that fits is split all the way to minCbSize,
	// which is what 8x8 mode asks for. Boundary CUs split regardless.
	splitToMin bool
}

// chooseCodingLayout picks the coding block sizes for a picture.
//
// pic_width_in_luma_samples and pic_height_in_luma_samples must each be an
// integer multiple of MinCbSizeY, so a 16x16 minimum CB can only express
// dimensions that are multiples of 16. Common heights are not — 360 and 1080
// are both 8 mod 16 — so those pictures use a minimum CB of 8, which makes them
// legal and costs one split_cu_flag per CTU. Dimensions that are multiples of 16
// keep the 16x16 minimum, so their bitstreams are unchanged.
func chooseCodingLayout(width, height int, use8x8CU bool) codingLayout {
	minCb := 16
	if use8x8CU || width%16 != 0 || height%16 != 0 {
		minCb = 8
	}
	return codingLayout{
		ctuSize:       ctuSizeLuma,
		minCbSize:     minCb,
		log2CtuSize:   ceilLog2(ctuSizeLuma),
		log2MinCbSize: ceilLog2(minCb),
		splitToMin:    use8x8CU,
	}
}

// validateFrameDimensions reports whether a picture size can be coded at all.
// The smallest legal MinCbSizeY is 8 and the picture dimensions must be a
// multiple of it, so 8 is the granularity floor; 4:2:0 chroma needs even
// dimensions anyway. Sizes that are neither would need a conformance window,
// which the generator does not emit.
func validateFrameDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("frame size %dx%d: width and height must be positive", width, height)
	}
	if width%8 != 0 || height%8 != 0 {
		return fmt.Errorf("frame size %dx%d: width and height must be multiples of 8 "+
			"(HEVC requires a multiple of the minimum coding block size, and 8 is the "+
			"smallest one); nearest usable size is %dx%d",
			width, height, roundUpTo8(width), roundUpTo8(height))
	}
	return nil
}

func roundUpTo8(v int) int {
	if v <= 0 {
		return 8
	}
	return (v + 7) / 8 * 8
}

// cuDepths records the quadtree depth of each coded CU at minCb granularity, so
// split_cu_flag can be given the context the spec asks for.
type cuDepths struct {
	depths     []int
	minCbSize  int
	widthMinCb int
}

func newCuDepths(picW, picH, minCbSize int) *cuDepths {
	w := (picW + minCbSize - 1) / minCbSize
	h := (picH + minCbSize - 1) / minCbSize
	d := make([]int, w*h)
	for i := range d {
		d[i] = -1
	}
	return &cuDepths{depths: d, minCbSize: minCbSize, widthMinCb: w}
}

func (m *cuDepths) set(x0, y0, cuSize, depth int) {
	h := len(m.depths) / m.widthMinCb
	for y := y0 / m.minCbSize; y < (y0+cuSize)/m.minCbSize; y++ {
		if y < 0 || y >= h {
			continue
		}
		for x := x0 / m.minCbSize; x < (x0+cuSize)/m.minCbSize; x++ {
			if x < 0 || x >= m.widthMinCb {
				continue
			}
			m.depths[y*m.widthMinCb+x] = depth
		}
	}
}

func (m *cuDepths) get(x, y int) int {
	if x < 0 || y < 0 {
		return -1
	}
	bx, by := x/m.minCbSize, y/m.minCbSize
	if bx >= m.widthMinCb || by*m.widthMinCb+bx >= len(m.depths) {
		return -1
	}
	return m.depths[by*m.widthMinCb+bx]
}

// splitCuCtxInc is ctxInc for split_cu_flag per spec 9.3.4.2.2: one for each of
// the left and above neighbours that sits deeper in the quadtree than this CU.
func splitCuCtxInc(depths *cuDepths, x0, y0, depth int) int {
	ctxInc := 0
	if d := depths.get(x0-1, y0); d >= 0 && d > depth {
		ctxInc++
	}
	if d := depths.get(x0, y0-1); d >= 0 && d > depth {
		ctxInc++
	}
	return ctxInc
}

// quantGroups tracks where cu_qp_delta_abs has to be written. A quantization
// group is the area that shares one cu_qp_delta: spec 7.3.8.4 clears
// IsCuQpDeltaCoded at every coding quadtree node whose block is at least
// Log2MinCuQpDeltaSize, and spec 7.3.8.10 codes the element at the first
// transform unit within the group that carries any coefficient at all.
//
// A nil *quantGroups is a PPS with cu_qp_delta_enabled_flag clear: nothing is
// tracked and nothing is written. Both methods take a nil receiver so the call
// sites need no guard.
//
// Nothing has to be carried across a tile or a wavefront row, which is where
// spec 8.6.1 restarts QP prediction: the group is never larger than a CTB, so
// every CTB begins one and clears the flag anyway.
type quantGroups struct {
	// size is the quantization group size in luma samples, CtbSizeY >>
	// diff_cu_qp_delta_depth.
	size int
	// coded is IsCuQpDeltaCoded for the group being written.
	coded bool
}

// newQuantGroups returns the tracker for a PPS whose quantization groups are
// size luma samples across, or nil when cu_qp_delta is disabled.
func newQuantGroups(size int) *quantGroups {
	if size <= 0 {
		return nil
	}
	return &quantGroups{size: size}
}

// startGroup clears the coded flag if this quadtree node begins a quantization
// group. A node bigger than the group size begins one too; each child that is
// itself a group simply clears the flag again.
func (q *quantGroups) startGroup(size int) {
	if q != nil && size >= q.size {
		q.coded = false
	}
}

// writeDelta writes this group's cu_qp_delta_abs if the transform unit about to
// be written is the first in the group to code a coefficient. cbf says whether it
// codes any luma or chroma coefficient; a group made only of all-zero transform
// units codes no delta at all, which is why the flat areas of a picture cost
// nothing here.
func (q *quantGroups) writeDelta(enc *cabac.Encoder, models []cabac.CtxState, cbf bool) {
	if q == nil || q.coded || !cbf {
		return
	}
	// cu_qp_delta_abs = 0, which is the whole element: spec 9.3.3.10 binarises it
	// as a truncated Rice prefix of up to five context-coded bins, so zero is the
	// single bin that ends the prefix immediately, and cu_qp_delta_sign_flag is
	// only present when the magnitude is non-zero.
	//
	// Zero is the only delta this encoder needs. Every CU it writes is quantized
	// at SliceQpY, so qPY_PRED — an average of the QPs left of and above the group
	// origin, each falling back to the previous group's (spec 8.6.1) — is SliceQpY
	// as well, and QpY = qPY_PRED + 0 is exactly the QP the residual was
	// quantized with. Coding something else would mean choosing a QP per group,
	// which is a rate control decision this generator does not make.
	enc.EncodeDecision(0, &models[context.CtxCuQpDeltaAbs])
	q.coded = true
}

// encodeCodingQuadtree walks one coding quadtree, writing split_cu_flag exactly
// where spec 7.3.8.4 codes it and calling code for every CU a decoder will parse.
//
// The flag is only coded when the CU lies entirely inside the picture. A CU that
// crosses the right or bottom edge is split with no flag at all, the split being
// inferred as long as the size stays above MinCbSizeY, and the children that
// fall wholly outside the picture are not coded either. That is what lets a
// 16x16 CTU cover a height like 1080 or 360.
func encodeCodingQuadtree(enc *cabac.Encoder, models []cabac.CtxState,
	x0, y0, size, depth int, lay codingLayout, picW, picH int,
	depths *cuDepths, qg *quantGroups, code func(x0, y0, size int)) {

	if x0 >= picW || y0 >= picH {
		return // wholly outside the picture: nothing is coded
	}

	// Quantization group boundary (spec 7.3.8.4), after the bounds check because
	// spec 7.3.8.4 does not recurse into a node that starts outside the picture,
	// so such a node begins no group.
	qg.startGroup(size)

	fits := x0+size <= picW && y0+size <= picH
	canSplit := size > lay.minCbSize

	if canSplit {
		switch {
		case !fits:
			// Implicit split: no flag is coded.
		case lay.splitToMin:
			enc.EncodeDecision(1, &models[context.CtxSplitCuFlag+splitCuCtxInc(depths, x0, y0, depth)])
		default:
			enc.EncodeDecision(0, &models[context.CtxSplitCuFlag+splitCuCtxInc(depths, x0, y0, depth)])
			depths.set(x0, y0, size, depth)
			code(x0, y0, size)
			return
		}
		half := size / 2
		for _, off := range [4][2]int{{0, 0}, {half, 0}, {0, half}, {half, half}} {
			encodeCodingQuadtree(enc, models, x0+off[0], y0+off[1], half, depth+1,
				lay, picW, picH, depths, qg, code)
		}
		return
	}

	// At minCbSize. validateFrameDimensions guarantees the picture is a multiple
	// of minCbSize, so a CU this size always fits and every sample read below is
	// inside the plane.
	depths.set(x0, y0, size, depth)
	code(x0, y0, size)
}

// idrSliceParams holds the parameters needed to write an IRAP (IDR or CRA)
// slice header when using external SPS/PPS.
type idrSliceParams struct {
	// seg says which slice segment of the picture this is; a tiled picture is
	// one segment per tile.
	seg segment

	width    int
	height   int
	qp       int
	use8x8CU bool
	// minCuQpDeltaSize is the quantization group size in luma samples,
	// CtbSizeY >> diff_cu_qp_delta_depth, or zero when the PPS leaves
	// cu_qp_delta_enabled_flag clear and no cu_qp_delta is written at all.
	minCuQpDeltaSize                int
	ppsID                           uint32
	numExtraSliceHeaderBits         uint8
	outputFlagPresent               bool
	saoEnabled                      bool
	deblockingFilterControlPresent  bool
	deblockingFilterOverrideEnabled bool
	deblockingFilterDisabled        bool
	loopFilterAcrossSlicesEnabled   bool

	// refPicSet is nil for an IDR slice and non-nil for a CRA slice, which
	// carries the POC and reference picture set fields an IDR header omits.
	refPicSet *pocRefPicSetParams
}

// pocRefPicSetParams holds the slice header fields that every picture except an
// IDR carries right after pic_output_flag (spec 7.3.6.1): the picture order
// count LSB, the short-term and long-term reference picture sets, and the
// slice-level temporal MVP flag.
type pocRefPicSetParams struct {
	poc                    int
	log2MaxPicOrderCntLsb  int  // log2_max_pic_order_cnt_lsb_minus4 + 4
	numShortTermRefPicSets int  // sps.NumShortTermRefPicSets
	numLongTermRefPics     int  // sps.NumLongTermRefPics (num_long_term_ref_pics_sps)
	longTermRefPicsPresent bool // sps.LongTermRefPicsPresentFlag
	spsTemporalMvpEnabled  bool // sps.SpsTemporalMvpEnabledFlag

	// intraRefresh marks a picture that references nothing (a CRA): an empty
	// inline short-term RPS is written and temporal MVP is switched off.
	intraRefresh bool
}

// craRefPicSetParams derives the CRA-specific slice header fields from the SPS.
func craRefPicSetParams(sps *hevc.SPS, poc int) pocRefPicSetParams {
	return pocRefPicSetParams{
		poc:                    poc,
		log2MaxPicOrderCntLsb:  int(sps.Log2MaxPicOrderCntLsbMinus4) + 4,
		numShortTermRefPicSets: int(sps.NumShortTermRefPicSets),
		numLongTermRefPics:     int(sps.NumLongTermRefPics),
		longTermRefPicsPresent: sps.LongTermRefPicsPresentFlag,
		spsTemporalMvpEnabled:  sps.SpsTemporalMvpEnabledFlag,
		intraRefresh:           true,
	}
}

// writePOCAndRefPicSets writes slice_pic_order_cnt_lsb, the short-term and
// long-term reference picture sets and slice_temporal_mvp_enabled_flag
// (spec 7.3.6.1). Only POC LSBs are coded, so the caller's POC is masked to
// log2MaxPicOrderCntLsb bits; the decoder derives the MSBs from prevTid0Pic,
// which is what lets a CRA splice into a running stream without resetting POC.
func writePOCAndRefPicSets(w *BitWriter, p pocRefPicSetParams) {
	pocBits := p.log2MaxPicOrderCntLsb
	pocMask := (1 << pocBits) - 1
	w.WriteBits(uint32(p.poc&pocMask), pocBits)

	switch {
	case p.intraRefresh:
		// An intra refresh picture uses no reference pictures, so an empty RPS
		// is written inline rather than pointing at one of the SPS sets.
		w.WriteBit(0) // short_term_ref_pic_set_sps_flag = 0
		if p.numShortTermRefPicSets > 0 {
			// Inline RPS in a slice header has stRpsIdx = num_short_term_ref_pic_sets,
			// and inter_ref_pic_set_prediction_flag is coded when stRpsIdx > 0.
			w.WriteBit(0) // inter_ref_pic_set_prediction_flag = 0
		}
		w.WriteUE(0) // num_negative_pics = 0
		w.WriteUE(0) // num_positive_pics = 0

	case p.numShortTermRefPicSets > 0:
		// short_term_ref_pic_set_sps_flag = 1, use SPS index 0
		w.WriteBit(1)
		if p.numShortTermRefPicSets > 1 {
			// short_term_ref_pic_set_idx: u(ceil(log2(numShortTermRefPicSets)))
			bits := ceilLog2(p.numShortTermRefPicSets)
			w.WriteBits(0, bits) // index 0
		}

	default:
		// short_term_ref_pic_set_sps_flag = 0 (inline)
		// Inline STRPS: stRpsIdx=0, no inter_ref_pic_set_prediction_flag
		w.WriteBit(0)
		w.WriteUE(1)  // num_negative_pics = 1
		w.WriteUE(0)  // num_positive_pics = 0
		w.WriteUE(0)  // delta_poc_s0_minus1 = 0 (deltaPOC = -1)
		w.WriteBit(1) // used_by_curr_pic_s0_flag = 1
	}

	// Long-term reference pictures are never used.
	if p.longTermRefPicsPresent {
		if p.numLongTermRefPics > 0 {
			w.WriteUE(0) // num_long_term_sps = 0
		}
		w.WriteUE(0) // num_long_term_pics = 0
	}

	// slice_temporal_mvp_enabled_flag. Spec 7.4.7.1 requires it to be 0 when
	// the current picture is a CRA or BLA picture.
	if p.spsTemporalMvpEnabled {
		if p.intraRefresh {
			w.WriteBit(0)
		} else {
			w.WriteBit(1)
		}
	}
}

func idrSliceParamsFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS) idrSliceParams {
	qp := 26 + int(pps.InitQpMinus26) // slice_qp_delta = 0
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
	minCbSize := 1 << log2MinCbSize
	log2CtbSize := log2MinCbSize + int(sps.Log2DiffMaxMinLumaCodingBlockSize)

	// The quantization group, when the PPS asks for one. diff_cu_qp_delta_depth
	// is bounded by log2_diff_max_min_luma_coding_block_size, so the group is
	// never smaller than a minimum-size CU nor larger than a CTB.
	minCuQpDeltaSize := 0
	if pps.CuQpDeltaEnabledFlag {
		minCuQpDeltaSize = 1 << (log2CtbSize - int(pps.DiffCuQpDeltaDepth))
	}

	return idrSliceParams{
		width:                           int(sps.PicWidthInLumaSamples),
		height:                          int(sps.PicHeightInLumaSamples),
		qp:                              qp,
		use8x8CU:                        minCbSize == 8,
		minCuQpDeltaSize:                minCuQpDeltaSize,
		ppsID:                           pps.PicParameterSetID,
		numExtraSliceHeaderBits:         pps.NumExtraSliceHeaderBits,
		outputFlagPresent:               pps.OutputFlagPresentFlag,
		saoEnabled:                      sps.SampleAdaptiveOffsetEnabledFlag,
		deblockingFilterControlPresent:  pps.DeblockingFilterControlPresentFlag,
		deblockingFilterOverrideEnabled: pps.DeblockingFilterOverrideEnabledFlag,
		deblockingFilterDisabled:        pps.DeblockingFilterDisabledFlag,
		loopFilterAcrossSlicesEnabled:   pps.LoopFilterAcrossSlicesEnabledFlag,
	}
}

// encodeIDRSliceWithParams generates an IRAP slice RBSP respecting all header fields
// from external SPS/PPS parameters. It writes an IDR header, or a CRA header when
// p.refPicSet is set.
func encodeIDRSliceWithParams(p idrSliceParams, y, cb, cr []uint8) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(p.seg.firstFlag()) // first_slice_segment_in_pic_flag
	w.WriteBit(0)                 // no_output_of_prior_pics_flag = 0 (all IRAP)
	w.WriteUE(p.ppsID)            // slice_pic_parameter_set_id
	p.seg.writeAddress(w)         // dependent flag and slice_segment_address

	// num_extra_slice_header_bits: skip N zero bits
	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}

	w.WriteUE(2) // slice_type = 2 (I-slice)

	// pic_output_flag (only if output_flag_present_flag)
	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set.
	// CRA: POC lsb, reference picture sets and temporal MVP flag are present.
	if p.refPicSet != nil {
		writePOCAndRefPicSets(w, *p.refPicSet)
	}

	// SAO flags
	if p.saoEnabled {
		w.WriteBit(0) // slice_sao_luma_flag = 0
		w.WriteBit(0) // slice_sao_chroma_flag = 0
	}

	// slice_qp_delta = 0
	w.WriteSE(0)

	// Deblocking filter syntax
	if p.deblockingFilterControlPresent {
		if p.deblockingFilterOverrideEnabled {
			w.WriteBit(0) // deblocking_filter_override_flag = 0
		}
		// Uses PPS default deblocking state (no per-slice override)
	}

	// slice_loop_filter_across_slices_enabled_flag
	deblockActive := !p.deblockingFilterDisabled
	if p.loopFilterAcrossSlicesEnabled && (p.saoEnabled || deblockActive) {
		w.WriteBit(1) // slice_loop_filter_across_slices_enabled_flag = PPS default (1)
	}

	// The slice data has to exist before the header can describe it: its
	// substream lengths are the entry point offsets.
	subs := encodeIDRSliceData(p.seg, p.width, p.height, p.qp, p.use8x8CU,
		p.minCuQpDeltaSize, y, cb, cr)
	p.seg.writeEntryPointOffsets(w, subs)

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	return appendSubstreams(w.Bytes(), subs)
}

// encodeIDRSlice generates the IDR slice RBSP (header + CABAC data).
func encodeIDRSlice(seg segment, width, height, qp int, use8x8CU bool, y, cb, cr []uint8) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(seg.firstFlag()) // first_slice_segment_in_pic_flag
	w.WriteBit(0)               // no_output_of_prior_pics_flag = 0 (IDR only)
	w.WriteUE(0)                // slice_pic_parameter_set_id = 0
	seg.writeAddress(w)
	w.WriteUE(2) // slice_type = 2 (I-slice)
	// IDR: no pic_order_cnt_lsb, no short_term_ref_pic_set
	// SAO disabled in SPS → no SAO flags
	w.WriteSE(0) // slice_qp_delta = 0 (PPS init_qp_minus26 = qp-26)
	// Deblocking disabled in PPS → no deblock syntax
	// loop_filter_across_slices_enabled_flag: not present (PPS flag is 0 and deblock is disabled)
	// The generated PPS leaves cu_qp_delta_enabled_flag clear, so there are no
	// quantization groups to write a delta for.
	subs := encodeIDRSliceData(seg, width, height, qp, use8x8CU, 0, y, cb, cr)
	seg.writeEntryPointOffsets(w, subs)

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	return appendSubstreams(w.Bytes(), subs)
}

// encodeIDRSliceData encodes the CABAC slice data for an IDR I-slice, as the
// list of substreams it is made of.
// The CTU size is always 16. With use8x8CU each CTU is split by the coding
// quadtree into four independently predicted 8x8 CUs (SPS minCbSize = 8),
// otherwise the CTU is one 16x16 CU (SPS minCbSize = 16).
func encodeIDRSliceData(seg segment, width, height, qp int, use8x8CU bool,
	minCuQpDeltaSize int, y, cb, cr []uint8) [][]byte {

	lay := chooseCodingLayout(width, height, use8x8CU)
	ctuSize := lay.ctuSize

	// Everything the prediction reads from is per segment. The reconstruction
	// buffer is picture-sized but only this segment's samples are ever written to
	// it, so its availability map answers "reconstructed in this tile" — which is
	// what spec 6.4.1 allows a neighbour to be. The intra mode and CU depth maps
	// start empty for the same reason.
	reconFrame := frame.NewFrame(width, height)
	modes := newIntraModes()
	depths := newCuDepths(width, height, lay.minCbSize)
	qg := newQuantGroups(minCuQpDeltaSize)

	return seg.encodeCtbs(ctuSize, context.SliceTypeI, qp,
		func(enc *cabac.Encoder, models []cabac.CtxState, ctuX, ctuY int) {
			encodeCodingQuadtree(enc, models, ctuX, ctuY, ctuSize, 0, lay,
				width, height, depths, qg,
				func(x0, y0, cuSize int) {
					encodeIDRCU(enc, models, x0, y0, cuSize, ctuSize, lay.minCbSize,
						width, height, qp, y, cb, cr, reconFrame, modes, qg)
				})
		})
}

// encodeIDRCU encodes a single intra CU of size cuSize (8 or 16) at (cuX, cuY),
// as one PART_2Nx2N partition with one luma and two chroma transform blocks.
// ctuSize is the CTU size (16), used for reference sample availability and for
// the CTB boundary rule in the MPM derivation.
func encodeIDRCU(enc *cabac.Encoder, models []cabac.CtxState,
	cuX, cuY, cuSize, ctuSize, minCbSize, width, height, qp int,
	y, cb, cr []uint8, reconFrame *frame.Frame, modes *intraModes, qg *quantGroups) {

	// split_cu_flag is written by the caller.
	// No pred_mode_flag (I-slice, implicitly intra)

	// part_mode is only coded at the minimum CB size (spec 7.3.8.5); above it,
	// PART_2Nx2N is inferred. Writing it unconditionally desynchronises CABAC
	// for a 16x16 CU in a picture whose minimum CB is 8.
	if cuSize == minCbSize {
		enc.EncodeDecision(1, &models[context.CtxPartMode]) // part_mode = 2Nx2N
	}

	// Choose the best intra prediction mode for this flat-color block.
	// For a flat block, vertical (26) or horizontal (10) can produce zero
	// residual when the block color matches the top or left neighbor, while
	// DC (1) averages both neighbors and also introduces AC energy through
	// edge filtering at color boundaries.
	lumaMode := chooseBestLumaMode(reconFrame, cuX, cuY, cuSize, width, y)

	mpm := deriveMPM(cuX, cuY, ceilLog2(ctuSize), modes)

	// Encode intra luma mode
	mpmIdx := -1
	for i, m := range mpm {
		if m == lumaMode {
			mpmIdx = i
			break
		}
	}

	if mpmIdx >= 0 {
		// prev_intra_luma_pred_flag = 1
		enc.EncodeDecision(1, &models[context.CtxPrevIntraLumaPredFlag])
		// mpm_idx: TU bypass bins (0, 10, 11)
		switch mpmIdx {
		case 0:
			enc.EncodeBypass(0)
		case 1:
			enc.EncodeBypass(1)
			enc.EncodeBypass(0)
		case 2:
			enc.EncodeBypass(1)
			enc.EncodeBypass(1)
		}
	} else {
		// prev_intra_luma_pred_flag = 0
		enc.EncodeDecision(0, &models[context.CtxPrevIntraLumaPredFlag])
		// rem_intra_luma_pred_mode: 5 bypass bins
		rem := computeRemIntraLumaPredMode(lumaMode, mpm)
		for k := 4; k >= 0; k-- {
			enc.EncodeBypass(uint8((rem >> k) & 1))
		}
	}
	modes.set(cuX, cuY, cuSize, lumaMode)

	// intra_chroma_pred_mode = 4 (DM mode)
	// First bin = 0 means "use DM mode"
	enc.EncodeDecision(0, &models[context.CtxIntraChromaPredMode])

	// Transform tree (no split, TU = CU: 16x16 luma with 8x8 chroma, or
	// 8x8 luma with 4x4 chroma)
	log2TrSize := ceilLog2(cuSize)
	log2ChromaTrSize := log2TrSize - 1

	// cbf_cb at depth 0
	cbfCb := hasNonZeroChroma(cuX, cuY, cuSize, width, cb, qp, lumaMode, reconFrame, 0)
	encBool(enc, &models[context.CtxCbfCb], cbfCb)

	// cbf_cr at depth 0
	cbfCr := hasNonZeroChroma(cuX, cuY, cuSize, width, cr, qp, lumaMode, reconFrame, 1)
	encBool(enc, &models[context.CtxCbfCr], cbfCr)

	// Compute luma residual
	lumaResidual, lumaLevels, cbfLuma := computeLumaResidual(
		cuX, cuY, cuSize, width, y, qp, lumaMode, reconFrame)

	// cbf_luma at depth 0 (context = !trafoDepth = 1, so ctxIdx = CtxCbfLuma + 1)
	encBool(enc, &models[context.CtxCbfLuma+1], cbfLuma)

	// cu_qp_delta_abs comes between cbf_luma and the residual (spec 7.3.8.10),
	// once per quantization group and only for a transform unit that codes
	// something. The transform tree here is never split, so this CU is that one
	// transform unit.
	qg.writeDelta(enc, models, cbfLuma || cbfCb || cbfCr)

	// Encode residual if any cbf is set
	if cbfLuma {
		scanIdx := slice.ScanIdxForIntraMode(lumaMode, log2TrSize, true)
		encodeResidualCoding(enc, models, lumaLevels, log2TrSize, true, scanIdx)
	}

	// Reconstruct luma
	reconstructLuma(reconFrame, cuX, cuY, cuSize, width, height,
		lumaMode, lumaResidual, cbfLuma, lumaLevels, qp)

	// Encode and reconstruct chroma
	chromaTrSize := cuSize / 2
	chromaMode := lumaMode // DM mode
	chromaScanIdx := slice.ScanIdxForIntraMode(chromaMode, log2ChromaTrSize, false)

	for comp := range 2 {
		var chromaSrc []uint8
		if comp == 0 {
			chromaSrc = cb
		} else {
			chromaSrc = cr
		}
		hasCbf := (comp == 0 && cbfCb) || (comp == 1 && cbfCr)

		if hasCbf {
			chromaLevels := computeChromaLevels(cuX, cuY, cuSize, width,
				chromaSrc, qp, chromaMode, reconFrame, comp)
			encodeResidualCoding(enc, models, chromaLevels, log2ChromaTrSize, false,
				chromaScanIdx)
			reconstructChroma(reconFrame, comp, cuX/2, cuY/2, chromaTrSize,
				width/2, height/2, chromaMode, chromaLevels, qp)
		} else {
			reconstructChroma(reconFrame, comp, cuX/2, cuY/2, chromaTrSize,
				width/2, height/2, chromaMode, nil, qp)
		}
	}
}

func encBool(enc *cabac.Encoder, ctx *cabac.CtxState, val bool) {
	v := uint8(0)
	if val {
		v = 1
	}
	enc.EncodeDecision(v, ctx)
}

// deriveMPM computes the Most Probable Modes for the CU at (x0,y0).
//
// log2CtbSize is the CTB size of the picture. Intra mode prediction does not
// cross a horizontal CTB boundary: per spec 8.4.2 the above candidate is
// INTRA_DC whenever y0-1 falls outside the current CTB. Omitting that rule
// keeps encoder and decoder self-consistent but makes the bitstream decode
// differently in every conforming decoder.
func deriveMPM(x0, y0, log2CtbSize int, modes *intraModes) [3]int {
	leftMode := modes.get(x0-1, y0)

	aboveMode := -1
	if y0-1 >= (y0>>log2CtbSize)<<log2CtbSize {
		aboveMode = modes.get(x0, y0-1)
	}

	// HEVC spec 8.4.2
	if leftMode < 0 {
		leftMode = 1 // DC
	}
	if aboveMode < 0 {
		aboveMode = 1 // DC
	}

	var mpm [3]int
	if leftMode == aboveMode {
		if leftMode < 2 {
			mpm = [3]int{0, 1, 26}
		} else {
			mpm[0] = leftMode
			mpm[1] = 2 + ((leftMode + 29) % 32)
			mpm[2] = 2 + ((leftMode - 2 + 1) % 32)
		}
	} else {
		mpm[0] = leftMode
		mpm[1] = aboveMode
		if leftMode != 0 && aboveMode != 0 {
			mpm[2] = 0
		} else if leftMode != 1 && aboveMode != 1 {
			mpm[2] = 1
		} else {
			mpm[2] = 26
		}
	}
	return mpm
}

// computeRemIntraLumaPredMode computes rem_intra_luma_pred_mode from the
// actual mode and MPM list (mode is not in MPM).
func computeRemIntraLumaPredMode(mode int, mpm [3]int) int {
	// Sort MPM in ascending order
	sorted := sortMPM(mpm)
	rem := mode
	for _, m := range sorted {
		if rem >= m {
			rem--
		}
	}
	return rem
}

func sortMPM(mpm [3]int) [3]int {
	s := mpm
	if s[0] > s[1] {
		s[0], s[1] = s[1], s[0]
	}
	if s[1] > s[2] {
		s[1], s[2] = s[2], s[1]
	}
	if s[0] > s[1] {
		s[0], s[1] = s[1], s[0]
	}
	return s
}

// hasNonZeroChroma checks if the chroma residual has non-zero quantized coefficients.
func hasNonZeroChroma(ctuX, ctuY, ctuSize, picWidth int, chromaSrc []uint8,
	qp, lumaMode int, recon *frame.Frame, comp int) bool {

	chromaTrSize := ctuSize / 2
	chromaQP := transform.ChromaQPFromLumaQP(qp)
	cx := ctuX / 2
	cy := ctuY / 2

	// Get prediction
	chromaPred := predictChromaBlock(recon, comp, cx, cy, chromaTrSize, lumaMode)

	// Compute residual
	chromaW := picWidth / 2
	chromaH := picWidth / 2 // assumes square for now; picHeight/2 for non-square
	residual := make([]int32, chromaTrSize*chromaTrSize)
	for py := range chromaTrSize {
		for px := range chromaTrSize {
			sx := cx + px
			sy := cy + py
			if sx < chromaW && sy < chromaH {
				srcIdx := sy*chromaW + sx
				residual[py*chromaTrSize+px] = int32(chromaSrc[srcIdx]) - chromaPred[py*chromaTrSize+px]
			}
		}
	}

	// Forward transform + quantize
	coeffs := forwardDCT(residual, chromaTrSize)
	levels := quantize(coeffs, chromaTrSize, chromaQP)

	for _, l := range levels {
		if l != 0 {
			return true
		}
	}
	return false
}

// computeLumaResidual computes the luma residual, transforms, and quantizes.
// Returns the prediction, quantized levels, and whether cbf_luma is set.
func computeLumaResidual(ctuX, ctuY, ctuSize, picWidth int, lumaSrc []uint8,
	qp, lumaMode int, recon *frame.Frame) (prediction []int32, levels []int32, cbfLuma bool) {

	// Get intra prediction for this block
	prediction = predictLumaBlock(recon, ctuX, ctuY, ctuSize, lumaMode)

	// Compute residual
	residual := make([]int32, ctuSize*ctuSize)
	for py := range ctuSize {
		for px := range ctuSize {
			sx := ctuX + px
			sy := ctuY + py
			if sx < picWidth {
				residual[py*ctuSize+px] = int32(lumaSrc[sy*picWidth+sx]) - prediction[py*ctuSize+px]
			}
		}
	}

	// Forward transform + quantize
	coeffs := forwardDCT(residual, ctuSize)
	levels = quantize(coeffs, ctuSize, qp)

	for _, l := range levels {
		if l != 0 {
			cbfLuma = true
			break
		}
	}
	return prediction, levels, cbfLuma
}

// computeChromaLevels computes quantized chroma levels for encoding.
func computeChromaLevels(ctuX, ctuY, ctuSize, picWidth int, chromaSrc []uint8,
	qp, chromaMode int, recon *frame.Frame, comp int) []int32 {

	chromaTrSize := ctuSize / 2
	chromaQP := transform.ChromaQPFromLumaQP(qp)
	cx := ctuX / 2
	cy := ctuY / 2

	chromaPred := predictChromaBlock(recon, comp, cx, cy, chromaTrSize, chromaMode)

	chromaW := picWidth / 2
	residual := make([]int32, chromaTrSize*chromaTrSize)
	for py := range chromaTrSize {
		for px := range chromaTrSize {
			sx := cx + px
			sy := cy + py
			if sx < chromaW {
				residual[py*chromaTrSize+px] = int32(chromaSrc[sy*chromaW+sx]) - chromaPred[py*chromaTrSize+px]
			}
		}
	}

	coeffs := forwardDCT(residual, chromaTrSize)
	return quantize(coeffs, chromaTrSize, chromaQP)
}

// predictLumaBlock generates DC intra prediction for a luma block.
func predictLumaBlock(f *frame.Frame, x0, y0, size, mode int) []int32 {
	neighbors := pred.BuildRefSamples(x0, y0, size, f.Width, f.Height,
		func(x, y int) uint8 { return f.GetLumaPixel(x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x, y) },
	)
	pred.FilterRefSamples(neighbors, mode, size, true, false)
	return predictIntra(mode, size, neighbors, 8, true)
}

// predictChromaBlock generates intra prediction for a chroma block.
func predictChromaBlock(f *frame.Frame, comp, x0, y0, size, mode int) []int32 {
	neighbors := pred.BuildRefSamples(x0, y0, size, f.Width/2, f.Height/2,
		func(x, y int) uint8 { return f.GetChromaPixel(comp, x, y) },
		func(x, y int) bool { return f.IsLumaDecoded(x*2, y*2) },
	)
	pred.FilterRefSamples(neighbors, mode, size, false, false)
	return predictIntra(mode, size, neighbors, 8, false)
}

// predictIntra performs intra prediction for a block.
func predictIntra(mode, size int, neighbors *pred.Neighbors, bitDepth int, isLuma bool) []int32 {
	switch mode {
	case 0:
		return pred.PredictPlanar(size, neighbors, bitDepth)
	case 1:
		return pred.PredictDC(size, neighbors, bitDepth, isLuma)
	default:
		return pred.PredictAngular(mode, size, neighbors, bitDepth, isLuma)
	}
}

// chooseBestLumaMode evaluates candidate intra prediction modes and returns
// the one with the smallest residual. For flat-color 16x16 blocks, vertical (26)
// gives zero residual when the block matches the top neighbor, horizontal (10)
// when it matches the left neighbor, and DC (1) when both match or neither does.
func chooseBestLumaMode(recon *frame.Frame, ctuX, ctuY, ctuSize, picWidth int, lumaSrc []uint8) int {
	candidates := []int{1, 10, 26} // DC, horizontal, vertical
	bestMode := 1
	bestCost := int64(1 << 62)

	for _, mode := range candidates {
		prediction := predictLumaBlock(recon, ctuX, ctuY, ctuSize, mode)

		var cost int64
		for py := range ctuSize {
			for px := range ctuSize {
				sx := ctuX + px
				sy := ctuY + py
				if sx < picWidth {
					diff := int64(lumaSrc[sy*picWidth+sx]) - int64(prediction[py*ctuSize+px])
					if diff < 0 {
						diff = -diff
					}
					cost += diff
				}
			}
		}

		if cost < bestCost {
			bestCost = cost
			bestMode = mode
		}
		if cost == 0 {
			break
		}
	}

	return bestMode
}

func reconstructLuma(f *frame.Frame, x0, y0, size, picW, picH, mode int,
	prediction []int32, cbfLuma bool, levels []int32, qp int) {

	var residual []int32
	if cbfLuma {
		dequantCoeffs := transform.Dequantize(levels, size, qp)
		residual = transform.InverseDCT(dequantCoeffs, size)
	} else {
		residual = make([]int32, size*size)
	}

	recon := make([]int32, size*size)
	for i := range recon {
		recon[i] = prediction[i] + residual[i]
	}
	f.SetLumaBlock(x0, y0, size, recon)
}

// reconstructChroma reconstructs chroma pixels after encoding.
func reconstructChroma(f *frame.Frame, comp, cx, cy, chromaTrSize, chromaW, chromaH,
	mode int, levels []int32, qp int) {

	chromaQP := transform.ChromaQPFromLumaQP(qp)
	chromaPred := predictChromaBlock(f, comp, cx, cy, chromaTrSize, mode)

	var residual []int32
	if levels != nil {
		dequantCoeffs := transform.Dequantize(levels, chromaTrSize, chromaQP)
		residual = transform.InverseDCT(dequantCoeffs, chromaTrSize)
	} else {
		residual = make([]int32, chromaTrSize*chromaTrSize)
	}

	recon := make([]int32, chromaTrSize*chromaTrSize)
	for i := range recon {
		recon[i] = chromaPred[i] + residual[i]
	}
	f.SetChromaBlock(comp, cx, cy, chromaTrSize, recon)
}

// pSkipSliceParams holds the parameters needed to write a P-skip slice header.
type pSkipSliceParams struct {
	// seg says which slice segment of the picture this is; a tiled picture is
	// one segment per tile.
	seg segment

	width                             int
	height                            int
	qp                                int
	poc                               int
	ppsID                             uint32
	numExtraSliceHeaderBits           uint8
	outputFlagPresent                 bool
	log2MaxPicOrderCntLsb             int // log2_max_pic_order_cnt_lsb_minus4 + 4
	numShortTermRefPicSets            int
	numLongTermRefPics                int // num_long_term_ref_pics_sps
	longTermRefPicsPresent            bool
	spsTemporalMvpEnabled             bool
	saoEnabled                        bool
	cabacInitPresent                  bool
	sliceChromaQpOffsetsPresent       bool
	deblockingFilterControlPresent    bool
	deblockingFilterOverrideEnabled   bool
	deblockingFilterDisabled          bool
	loopFilterAcrossSlicesEnabled     bool
	log2MinCodingBlockSizeMinus3      int
	log2DiffMaxMinLumaCodingBlockSize int
}

// encodePSkipSlice generates a P-skip slice RBSP (header + CABAC data).
// Uses the default encoder parameters: CTU=16, minCb=16, or minCb=8 with
// use8x8CU to match the SPS written for 8x8 coding granularity. The picture is
// one 16x16 skip CU per CTU either way; with minCb=8 that needs an explicit
// split_cu_flag = 0 at depth 0.
func encodePSkipSlice(seg segment, width, height, qp, poc int, use8x8CU bool) []byte {
	lay := chooseCodingLayout(width, height, use8x8CU)
	log2MinCbMinus3 := lay.log2MinCbSize - 3
	log2Diff := lay.log2CtuSize - lay.log2MinCbSize
	return encodePSkipSliceWithParams(pSkipSliceParams{
		seg:                               seg,
		width:                             width,
		height:                            height,
		qp:                                qp,
		poc:                               poc,
		log2MaxPicOrderCntLsb:             4,
		log2MinCodingBlockSizeMinus3:      log2MinCbMinus3,
		log2DiffMaxMinLumaCodingBlockSize: log2Diff,
	})
}

// encodePSkipSliceWithParams generates a P-skip slice RBSP respecting all header fields.
func encodePSkipSliceWithParams(p pSkipSliceParams) []byte {
	w := NewBitWriter()

	// === Slice header ===
	w.WriteBit(p.seg.firstFlag()) // first_slice_segment_in_pic_flag
	w.WriteUE(p.ppsID)            // slice_pic_parameter_set_id
	p.seg.writeAddress(w)         // dependent flag and slice_segment_address
	// num_extra_slice_header_bits: skip N zero bits
	for range p.numExtraSliceHeaderBits {
		w.WriteBit(0)
	}
	w.WriteUE(1) // slice_type = 1 (P-slice)

	// pic_output_flag (only if output_flag_present_flag)
	if p.outputFlagPresent {
		w.WriteBit(1) // pic_output_flag = 1
	}

	// pic_order_cnt_lsb, reference picture sets, slice_temporal_mvp_enabled_flag
	writePOCAndRefPicSets(w, pocRefPicSetParams{
		poc:                    p.poc,
		log2MaxPicOrderCntLsb:  p.log2MaxPicOrderCntLsb,
		numShortTermRefPicSets: p.numShortTermRefPicSets,
		numLongTermRefPics:     p.numLongTermRefPics,
		longTermRefPicsPresent: p.longTermRefPicsPresent,
		spsTemporalMvpEnabled:  p.spsTemporalMvpEnabled,
	})

	// SAO flags
	if p.saoEnabled {
		w.WriteBit(0) // slice_sao_luma_flag = 0
		w.WriteBit(0) // slice_sao_chroma_flag = 0
	}

	// num_ref_idx_active_override_flag = 0
	w.WriteBit(0)

	// cabac_init_flag (only if cabac_init_present_flag)
	if p.cabacInitPresent {
		w.WriteBit(0) // cabac_init_flag = 0
	}

	// five_minus_max_num_merge_cand = 4 (maxMergeCand = 1)
	w.WriteUE(4)

	// slice_qp_delta = 0
	w.WriteSE(0)

	// chroma QP offsets (only if pps_slice_chroma_qp_offsets_present_flag)
	if p.sliceChromaQpOffsetsPresent {
		w.WriteSE(0) // slice_cb_qp_offset = 0
		w.WriteSE(0) // slice_cr_qp_offset = 0
	}

	// Deblocking filter syntax
	if p.deblockingFilterControlPresent {
		if p.deblockingFilterOverrideEnabled {
			w.WriteBit(0) // deblocking_filter_override_flag = 0
		}
		// Uses PPS default deblocking state (no per-slice override)
	}

	// slice_loop_filter_across_slices_enabled_flag
	// Present when PPS has loop_filter_across_slices_enabled and
	// (SAO or deblocking is active in this slice).
	// With SAO flags=0 and no deblock override, deblocking uses PPS default.
	deblockActive := !p.deblockingFilterDisabled
	if p.loopFilterAcrossSlicesEnabled && (p.saoEnabled || deblockActive) {
		w.WriteBit(1) // slice_loop_filter_across_slices_enabled_flag = PPS default (1)
	}

	// === Slice data (CABAC) ===
	// Derive CTU and min CB sizes from SPS parameters. The data comes first
	// because the header's entry point offsets are its substream lengths.
	log2MinCbSize := p.log2MinCodingBlockSizeMinus3 + 3
	ctuLog2 := log2MinCbSize + p.log2DiffMaxMinLumaCodingBlockSize
	ctuSize := 1 << ctuLog2
	subs := encodePSkipSliceData(p.seg, p.width, p.height, p.qp, ctuSize, log2MinCbSize)
	p.seg.writeEntryPointOffsets(w, subs)

	// byte_alignment
	w.WriteBit(1)
	for w.BitsWritten()%8 != 0 {
		w.WriteBit(0)
	}

	return appendSubstreams(w.Bytes(), subs)
}

// encodePSkipSliceData encodes the CABAC slice data for a P-skip slice, as the
// list of substreams it is made of.
// log2MinCbSize is the minimum coding block size (log2). When ctuSize > minCbSize,
// split_cu_flag=0 must be written at each quadtree level before cu_skip_flag.
func encodePSkipSliceData(seg segment, width, height, qp, ctuSize, log2MinCbSize int) [][]byte {
	// The segment's own left and top edges in luma samples: a neighbour beyond
	// them is in another tile, which spec 6.4.1 makes unavailable.
	segLeft := seg.region.ColStart * ctuSize
	segTop := seg.region.RowStart * ctuSize

	// The layout comes from the SPS the caller is matching, not from
	// chooseCodingLayout: an external SPS may use any minimum CB size.
	lay := codingLayout{
		ctuSize:       ctuSize,
		minCbSize:     1 << log2MinCbSize,
		log2CtuSize:   ceilLog2(ctuSize),
		log2MinCbSize: log2MinCbSize,
	}
	depths := newCuDepths(width, height, lay.minCbSize)

	// No quantization groups: a skipped CU has no transform tree, so there is
	// never a transform unit to carry a cu_qp_delta however the PPS is set
	// (spec 7.3.8.10 codes it inside transform_unit).
	return seg.encodeCtbs(ctuSize, context.SliceTypeP, qp,
		func(enc *cabac.Encoder, models []cabac.CtxState, ctuX, ctuY int) {
			encodeCodingQuadtree(enc, models, ctuX, ctuY, ctuSize, 0, lay,
				width, height, depths, nil,
				func(x0, y0, _ int) {
					// cu_skip_flag = 1. ctxInc counts the left and above neighbours
					// that are skipped; every coded CU here is, so availability alone
					// decides it — and a neighbour outside this tile is unavailable,
					// which is what the decoder's per-tile reset amounts to.
					ctxInc := 0
					if x0 > segLeft {
						ctxInc++
					}
					if y0 > segTop {
						ctxInc++
					}
					enc.EncodeDecision(1, &models[context.CtxCuSkipFlag+ctxInc])
					// merge_idx = 0: when maxMergeCand=1, no bins coded
				})
		})
}

// ceilLog2 returns ceil(log2(n)) for n >= 1.
func ceilLog2(n int) int {
	bits := 0
	v := n - 1
	for v > 0 {
		v >>= 1
		bits++
	}
	return bits
}
