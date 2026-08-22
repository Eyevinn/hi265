// Package slice implements HEVC slice data parsing including
// CTU/CU/TU quadtree structure and residual coding via CABAC.
package slice

import (
	"fmt"

	"github.com/Eyevinn/hi265/internal/cabac"
	"github.com/Eyevinn/hi265/internal/context"
	"github.com/Eyevinn/hi265/internal/tiles"
)

// CodingUnit holds the decoded data for a single CU.
type CodingUnit struct {
	X0, Y0          int
	Log2CbSize      int
	PredMode        int // 0=inter, 1=intra
	PartMode        int // for intra: 0=2Nx2N, 1=NxN
	SkipFlag        bool
	MergeIdx        int
	QpY             int    // resolved luma QP for this CU
	IntraLumaMode   [4]int // up to 4 PUs for NxN
	IntraChromaMode int
	// Residual data per TU
	TransformUnits []TransformUnit
}

// TransformUnit holds decoded residual coefficients for one TU.
type TransformUnit struct {
	X0, Y0     int
	Log2TrSize int
	// ChromaX0, ChromaY0 give the luma position the chroma block covers, which
	// differs from X0, Y0 for 4x4 luma TBs: there one chroma block spans the
	// whole 8x8 group and is carried by the last of the four TUs.
	ChromaX0, ChromaY0 int
	// HasChroma is false for the TUs of a 4x4 group that carry no chroma block.
	HasChroma         bool
	CbfLuma           bool
	CbfCb             bool
	CbfCr             bool
	TransformSkipLuma bool
	TransformSkipCb   bool
	TransformSkipCr   bool
	LumaCoeffs        []int32 // coefficients in scan order
	CbCoeffs          []int32
	CrCoeffs          []int32
}

// SaoParams holds SAO parameters for one CTU (per component: 0=luma, 1=Cb, 2=Cr).
type SaoParams struct {
	TypeIdx [3]int    // 0=none, 1=band offset, 2=edge offset
	Offsets [3][4]int // 4 offset values per component
	BandPos [3]int    // band position (BO mode)
	EoClass [3]int    // edge offset class (EO mode)
}

// SliceData holds all decoded CUs for a slice segment.
type SliceData struct {
	CUs       []CodingUnit
	SaoParams []SaoParams // one per CTU, indexed by CTU raster address
	// CtbsDecoded counts the CTBs this segment covered, so a caller assembling
	// a picture from several segments can tell whether they add up to one.
	CtbsDecoded int
	// State is what this slice carries to its next segment. A caller decoding a
	// dependent slice segment passes it back in through Params.
	State *State
}

// State is what a slice keeps across its segments.
//
// A slice is one independent slice segment followed by zero or more dependent
// ones (spec 7.4.7.1). A dependent segment carries almost no header of its own:
// it continues its predecessor's CABAC contexts (spec 9.3.1), its QP prediction
// (8.6.1), and its neighbour maps, since spec 6.4.1 makes a neighbour available
// when it is in the same *slice* — segment boundaries inside a slice are not
// prediction boundaries at all.
type State struct {
	// sliceAddrRS is SliceAddrRs: the raster address of the first CTB of the
	// slice, which is the segment address of its independent segment. Several
	// syntax elements are conditioned on it rather than on the segment's own
	// address, the SAO merge flags among them (spec 7.3.8.3).
	sliceAddrRS int
	modeMap     *intraModeMap
	depthMap    *minCbMap
	skipMap     *minCbMap
	qps         *qpState
	// ctx is the context state stored when the previous segment of this slice
	// ended (spec 9.3.2.4), which a dependent segment resumes from.
	ctx []cabac.CtxState
	// sync is the wavefront snapshot taken after the second CTB of a row
	// (spec 9.3.2.3). It outlives a segment boundary because the row above may
	// have been decoded by an earlier segment of the same slice.
	sync []cabac.CtxState
}

// newState prepares the state of a slice beginning at raster address addrRS.
func newState(p *Params, addrRS int) *State {
	return &State{
		sliceAddrRS: addrRS,
		modeMap:     newIntraModeMap(p.PicWidth, p.PicHeight),
		depthMap:    newMinCbMap(p.PicWidth, p.PicHeight, 1<<p.Log2MinCbSize),
		skipMap:     newMinCbMap(p.PicWidth, p.PicHeight, 1<<p.Log2MinCbSize),
		qps:         newQpState(p.SliceQPY, p.PicWidth, p.PicHeight, 1<<p.Log2MinCbSize),
	}
}

// Params holds SPS/PPS-derived parameters needed for slice data decoding.
type Params struct {
	SliceType                       int // 0=B, 1=P, 2=I
	SliceQPY                        int
	PicWidth, PicHeight             int
	Log2CtbSize                     int
	Log2MinCbSize                   int
	Log2MinTrafoSize                int
	Log2MaxTrafoSize                int
	MaxTransformHierarchyDepthIntra int
	MaxNumMergeCand                 int
	SignDataHidingEnabled           bool
	TransformSkipEnabled            bool
	SaoLuma                         bool
	SaoChroma                       bool
	CuQpDeltaEnabled                bool
	Log2MinCuQpDeltaSize            int // log2CtbSize - DiffCuQpDeltaDepth

	// EntropyCodingSyncEnabled mirrors the PPS flag: wavefront parallel
	// processing, where each CTU row is its own CABAC substream.
	EntropyCodingSyncEnabled bool
	// EntryPoints holds the byte offset into the slice data of each substream
	// after the first, from the slice header's entry point offsets.
	EntryPoints []int

	// Grid is the picture's tile partitioning, which decides the order CTBs are
	// coded in. Nil means a single tile, where tile scan is raster scan.
	Grid *tiles.Grid
	// SegmentAddressRS is slice_segment_address: the raster scan address of the
	// first CTB of this slice segment. Zero for a picture's only segment.
	SegmentAddressRS int
	// SaoParams, when non-nil, is the picture's SAO parameter array, shared by
	// every slice segment so that a merge can reference a CTB decoded by an
	// earlier one. A nil value allocates a fresh picture-sized array.
	SaoParams []SaoParams

	// Dependent mirrors dependent_slice_segment_flag: this segment continues the
	// slice that the previous segment belonged to, and carries no header of its
	// own beyond its address. State must then hold that slice's state.
	Dependent bool
	// State is the slice state returned by the previous segment of the same
	// slice. Nil starts a new slice.
	State *State
}

// qpState tracks the QP prediction state of spec 8.6.1.
//
// A quantization group is the area sharing one cu_qp_delta. When it is the whole
// CTB (diff_cu_qp_delta_depth 0) the predicted QP is simply the last QP decoded,
// which a single running value models fine. When the group is smaller — x265
// defaults to a 32x32 group with a 64x64 CTB, so most rate-controlled encodes —
// the prediction averages the QPs of the blocks left of and above the group's
// origin, and only falls back to the previous group's QP where those are
// unavailable or in another CTB. Without that, decoded QPs drift, and drift far
// enough to go negative.
type qpState struct {
	currentQP        int  // QP of the CU last decoded
	isCuQpDeltaCoded bool // whether cu_qp_delta has been decoded for this group
	qpYPrev          int  // QP of the last CU of the previous group
	qpYPred          int  // predicted QP for the current group
	// Quantization group origin, in luma samples.
	xQg, yQg int
	// qpMap holds the QP of every decoded block at minCb granularity, so the
	// left and above neighbours of a group origin can be looked up.
	qpMap      []int
	qpMapMinCb int
	qpMapWidth int
}

// newQpState prepares the QP prediction state for one slice.
func newQpState(sliceQPY, picW, picH, minCbSize int) *qpState {
	w := (picW + minCbSize - 1) / minCbSize
	h := (picH + minCbSize - 1) / minCbSize
	m := make([]int, w*h)
	for i := range m {
		m[i] = -1 // not yet decoded
	}
	return &qpState{
		currentQP:  sliceQPY,
		qpYPrev:    sliceQPY,
		qpYPred:    sliceQPY,
		qpMap:      m,
		qpMapMinCb: minCbSize,
		qpMapWidth: w,
	}
}

// resetToSliceQP restarts QP prediction from SliceQpY, which spec 8.6.1 requires
// at the first quantization group of a slice, of a tile, and of a CTB row when
// wavefront parallel processing is on.
func (q *qpState) resetToSliceQP(sliceQPY int) {
	q.currentQP = sliceQPY
	q.qpYPrev = sliceQPY
	q.qpYPred = sliceQPY
	q.isCuQpDeltaCoded = false
}

// qpAt returns the QP of the decoded block covering (x, y), or -1.
func (q *qpState) qpAt(x, y int) int {
	if x < 0 || y < 0 {
		return -1
	}
	bx, by := x/q.qpMapMinCb, y/q.qpMapMinCb
	if bx >= q.qpMapWidth || by*q.qpMapWidth+bx >= len(q.qpMap) {
		return -1
	}
	return q.qpMap[by*q.qpMapWidth+bx]
}

// setQP records a CU's QP over its area.
func (q *qpState) setQP(x0, y0, size, qp int) {
	h := len(q.qpMap) / q.qpMapWidth
	for y := y0 / q.qpMapMinCb; y < (y0+size)/q.qpMapMinCb; y++ {
		if y < 0 || y >= h {
			continue
		}
		for x := x0 / q.qpMapMinCb; x < (x0+size)/q.qpMapMinCb; x++ {
			if x < 0 || x >= q.qpMapWidth {
				continue
			}
			q.qpMap[y*q.qpMapWidth+x] = qp
		}
	}
}

// startQuantizationGroup applies spec 8.6.1 at the start of a group: the
// prediction averages the blocks left of and above the origin, each falling back
// to the previous group's QP when it is unavailable or lies in another CTB.
func (q *qpState) startQuantizationGroup(xQg, yQg, log2CtbSize int) {
	q.isCuQpDeltaCoded = false
	q.qpYPrev = q.currentQP
	q.xQg, q.yQg = xQg, yQg

	ctbMask := ^((1 << log2CtbSize) - 1)
	sameCtb := func(x, y int) bool {
		return x >= 0 && y >= 0 &&
			x&ctbMask == xQg&ctbMask && y&ctbMask == yQg&ctbMask
	}

	qpA := q.qpYPrev
	if sameCtb(xQg-1, yQg) {
		if v := q.qpAt(xQg-1, yQg); v >= 0 {
			qpA = v
		}
	}
	qpB := q.qpYPrev
	if sameCtb(xQg, yQg-1) {
		if v := q.qpAt(xQg, yQg-1); v >= 0 {
			qpB = v
		}
	}
	q.qpYPred = (qpA + qpB + 1) >> 1
	q.currentQP = q.qpYPred
}

// DecodeSliceData decodes the slice data segment using CABAC.
// cabacData is the raw CABAC bitstream (after slice header, emulation prevention removed).
func DecodeSliceData(cabacData []byte, p Params) (*SliceData, error) {
	ctbSize := 1 << p.Log2CtbSize
	ctbsX := (p.PicWidth + ctbSize - 1) / ctbSize
	ctbsY := (p.PicHeight + ctbSize - 1) / ctbSize
	numCTUs := ctbsX * ctbsY

	grid := p.Grid
	if grid == nil {
		grid = tiles.Single(ctbsX, ctbsY)
	}
	if grid.NumCtbs() != numCTUs {
		return nil, fmt.Errorf("tile grid covers %d CTBs, picture has %d",
			grid.NumCtbs(), numCTUs)
	}
	if p.SegmentAddressRS < 0 || p.SegmentAddressRS >= numCTUs {
		return nil, fmt.Errorf("slice_segment_address %d outside a picture of %d CTBs",
			p.SegmentAddressRS, numCTUs)
	}
	// Tiles and wavefront parallel processing together would make each CTB row
	// of each tile its own substream, with the context snapshot coming from the
	// row above within the same tile. No HEVC profile allows the combination
	// (kvazaar calls it experimental), so it is refused rather than guessed at.
	if grid.NumTiles() > 1 && p.EntropyCodingSyncEnabled {
		return nil, fmt.Errorf("tiles combined with wavefront parallel processing is not supported")
	}

	sd := &SliceData{SaoParams: p.SaoParams}
	if sd.SaoParams == nil {
		sd.SaoParams = make([]SaoParams, numCTUs)
	} else if len(sd.SaoParams) != numCTUs {
		return nil, fmt.Errorf("picture SAO array holds %d CTBs, picture has %d",
			len(sd.SaoParams), numCTUs)
	}

	// The neighbour maps and the QP prediction belong to the slice, not to the
	// segment: spec 6.4.1 makes a neighbour unavailable when it is in another
	// *slice*, so a dependent segment inherits them and an independent one starts
	// clean. An undecoded neighbour and one in another slice then look alike,
	// which is exactly right.
	st := p.State
	if st == nil {
		if p.Dependent {
			return nil, fmt.Errorf("dependent slice segment at CTB %d without the state of the slice it continues",
				p.SegmentAddressRS)
		}
		st = newState(&p, p.SegmentAddressRS)
	}
	sd.State = st
	modeMap, depthMap, skipMap, qps := st.modeMap, st.depthMap, st.skipMap, st.qps
	sliceFirstTs := grid.RsToTs(st.sliceAddrRS)

	// Wavefront parallel processing splits the slice data into one substream per
	// CTU row. Each substream restarts the arithmetic decoding engine at its
	// entry point, and its context state is inherited from a snapshot taken
	// after the second CTU of the row above rather than continuing the row
	// before it (spec 9.3.1). Without WPP there is a single substream and the
	// state simply carries on across rows.
	// Wavefront parallel processing has two halves that must not be conflated.
	// The *context* rules — a snapshot after the second CTB of every row, and a
	// row starting from the row above's snapshot — follow from
	// entropy_coding_sync_enabled_flag alone. The *substream* switch at a row
	// boundary needs an entry point offset, and a segment holding a single row
	// carries none. A dependent segment per row, which is what
	// "kvazaar --slices wpp" emits, has the first without the second.
	sync := p.EntropyCodingSyncEnabled
	firstRow := p.SegmentAddressRS / ctbsX
	substreams := splitSubstreams(cabacData, p.EntryPoints)
	if len(cabacData) == 0 {
		return nil, fmt.Errorf("slice segment at CTB %d carries no data", p.SegmentAddressRS)
	}

	dec, err := cabac.NewDecoder(substreams[0])
	if err != nil {
		return nil, fmt.Errorf("init CABAC: %w", err)
	}
	// Spec 9.3.1 decides what the contexts start from, and the same three-way
	// choice applies at every substream boundary further down.
	ctxModels, resetQP := initialContexts(grid, &p, st, ctbsX, sliceFirstTs)
	if resetQP {
		qps.resetToSliceQP(p.SliceQPY)
	}

	// The segment covers a run of consecutive tile scan addresses starting at
	// its own, and ends when end_of_slice_segment_flag says so.
	firstTs := grid.RsToTs(p.SegmentAddressRS)
	// substream indexes into substreams for the tile case, where each tile of the
	// segment is one; the wavefront case indexes them by row instead. tile tracks
	// which tile the previous CTB belonged to, so the first CTB of the next one
	// can be recognised.
	substream := 0
	tile := grid.TileIDOfRs(p.SegmentAddressRS)

	for ts := firstTs; ts < numCTUs; ts++ {
		ctbAddrRS := grid.TsToRs(ts)
		ctbX := (ctbAddrRS % ctbsX) * ctbSize
		ctbY := (ctbAddrRS / ctbsX) * ctbSize
		row := ctbAddrRS / ctbsX

		// A tile boundary inside this slice segment. Each tile of the segment is
		// its own substream, reached through an entry point offset, and it starts
		// from a clean slate: the CABAC contexts are re-initialised rather than
		// carried over (spec 9.3.1), QP prediction restarts from SliceQpY
		// (8.6.1), and nothing decoded in an earlier tile is available for
		// prediction (6.4.1).
		if t := grid.TileIDOfRs(ctbAddrRS); t != tile {
			tile = t
			substream++
			if substream >= len(substreams) {
				return nil, fmt.Errorf(
					"tiles: no substream for the tile at CTB %d; the segment carries %d entry point offsets",
					ctbAddrRS, len(p.EntryPoints))
			}
			dec, err = cabac.NewDecoder(substreams[substream])
			if err != nil {
				return nil, fmt.Errorf("tiles: init CABAC for the tile at CTB %d: %w", ctbAddrRS, err)
			}
			ctxModels = context.InitModels(p.SliceType, p.SliceQPY)
			qps.resetToSliceQP(p.SliceQPY)
			modeMap.reset()
			depthMap.reset()
			skipMap.reset()
		}

		if sync && ctbAddrRS%ctbsX == 0 && ts > firstTs {
			// Start of a CTU row inside this segment. Its data is a substream of
			// its own, indexed from the segment's first row; a segment that
			// reaches a new row without an entry point for it is malformed.
			idx := row - firstRow
			if idx >= len(substreams) {
				return nil, fmt.Errorf("WPP: no substream for CTU row %d; the segment carries %d entry point offsets",
					row, len(p.EntryPoints))
			}
			dec, err = cabac.NewDecoder(substreams[idx])
			if err != nil {
				return nil, fmt.Errorf("WPP: init CABAC for CTU row %d: %w", row, err)
			}
			// The contexts come from the row above, whose snapshot is only usable
			// when the CTB it was taken from is available — same slice, same tile
			// (spec 9.3.1 via 6.4.1). A row that opens a new slice starts fresh.
			aboveRight := ctbAddrRS - ctbsX + 1
			if st.sync != nil && ctbsX > 1 && available(grid, sliceFirstTs, aboveRight, ctbAddrRS) {
				ctxModels = append(ctxModels[:0], st.sync...)
			} else {
				ctxModels = context.InitModels(p.SliceType, p.SliceQPY)
			}
			qps.resetToSliceQP(p.SliceQPY)
		}

		// Decode SAO parameters for this CTU. The merge flags are only coded
		// when the neighbour they would copy from belongs to this slice segment
		// and this tile (spec 7.3.8.3) — a bin read for a neighbour across
		// either boundary was never coded, and desyncs CABAC from there on.
		if p.SaoLuma || p.SaoChroma {
			mergeLeftAvail := ctbX > 0 && available(grid, sliceFirstTs, ctbAddrRS-1, ctbAddrRS)
			mergeUpAvail := ctbY > 0 && available(grid, sliceFirstTs, ctbAddrRS-ctbsX, ctbAddrRS)
			sd.SaoParams[ctbAddrRS] = decodeSaoParams(
				dec, ctxModels, ctbsX, ctbAddrRS, sd.SaoParams,
				mergeLeftAvail, mergeUpAvail, p.SaoLuma, p.SaoChroma,
			)
		}

		cus, err := decodeCodingQuadtree(dec, ctxModels, ctbX, ctbY, p.Log2CtbSize, 0, &p,
			modeMap, depthMap, skipMap, qps)
		if err != nil {
			return nil, fmt.Errorf("CTU (%d,%d): %w", ctbX, ctbY, err)
		}
		sd.CUs = append(sd.CUs, cus...)

		// Snapshot the context state after the second CTU of a row, which is
		// what the first CTU of the next row starts from. It lives in the slice
		// state because that next row can be in a later segment.
		if sync && ctbAddrRS%ctbsX == 1 {
			st.sync = append(st.sync[:0], ctxModels...)
		}

		sd.CtbsDecoded++

		// end_of_slice_segment_flag
		endOfSlice := dec.DecodeTerminate()
		if endOfSlice == 1 {
			break
		}
		// A row under WPP, and a tile within a segment, both end with
		// end_of_subset_one_bit followed by byte alignment. The next substream is
		// reached through its entry point, so those trailing bits are simply left
		// behind with the one that ended.
	}

	// Store the context state for a dependent segment continuing this slice
	// (spec 9.3.2.4). Substreams the header announced but the segment never
	// reached are left alone: kvazaar writes one entry point offset per CTB row
	// of the *picture* into the first segment even when the following rows live
	// in their own dependent segments, and such a stream is otherwise perfectly
	// decodable.
	st.ctx = append(st.ctx[:0], ctxModels...)

	return sd, nil
}

// available implements the part of spec 6.4.1 that this decoder needs: a
// neighbouring CTB is available when it belongs to the same slice and the same
// tile as the current one, and comes earlier in tile scan. sliceFirstTs is the
// tile scan address of the slice's first CTB — the slice's, not the segment's,
// since segment boundaries inside a slice do not break availability.
func available(grid *tiles.Grid, sliceFirstTs, neighbourRS, curRS int) bool {
	nTs := grid.RsToTs(neighbourRS)
	return nTs >= sliceFirstTs && nTs < grid.RsToTs(curRS) && grid.SameTileRs(neighbourRS, curRS)
}

// initialContexts applies spec 9.3.1 at the first CTB of a slice segment, and
// reports whether QP prediction restarts there too (spec 8.6.1). The order of
// the tests is the spec's: beginning a tile beats beginning a wavefront row,
// which beats continuing a dependent segment.
func initialContexts(grid *tiles.Grid, p *Params, st *State, ctbsX, sliceFirstTs int) (
	ctx []cabac.CtxState, resetQP bool) {

	rs := p.SegmentAddressRS
	ts := grid.RsToTs(rs)
	fresh := func() []cabac.CtxState { return context.InitModels(p.SliceType, p.SliceQPY) }

	// The first CTB of a tile always starts from initial values.
	if ts == 0 || grid.TileIDOfRs(grid.TsToRs(ts-1)) != grid.TileIDOfRs(rs) {
		return fresh(), true
	}
	// The first CTB of a wavefront row takes the snapshot from the row above
	// when that CTB is available — same slice, same tile — and starts fresh
	// otherwise, which is what happens when a slice begins mid-picture.
	if p.EntropyCodingSyncEnabled && rs%ctbsX == 0 {
		if st.sync != nil && ctbsX > 1 && available(grid, sliceFirstTs, rs-ctbsX+1, rs) {
			return append([]cabac.CtxState(nil), st.sync...), true
		}
		return fresh(), true
	}
	// A dependent segment picks up where the previous segment of its slice left
	// off, mid-row and mid-tile, so nothing restarts.
	if p.Dependent && st.ctx != nil {
		return append([]cabac.CtxState(nil), st.ctx...), false
	}
	return fresh(), true
}

// splitSubstreams divides the slice data at the entry point offsets. A segment
// with no entry points is a single substream. The offsets mean the same thing
// for tiles as for wavefront parallel processing — where the next substream
// begins — only what happens at that point differs: a tile re-initialises the
// CABAC contexts, a WPP row inherits them from the row above.
//
// Offsets that fall outside the segment's own data are ignored rather than
// rejected, and so are ones the segment never reaches. kvazaar writes an entry
// point offset for every CTB row of the *picture* into the first slice segment,
// even when the following rows live in their own dependent segments, which makes
// the tail of that list describe data this segment does not contain. Such a
// stream decodes perfectly well as long as nothing insists on the offsets being
// meaningful, and a segment that really does run out of substreams says so where
// it needs one.
func splitSubstreams(cabacData []byte, entryPoints []int) [][]byte {
	subs := make([][]byte, 0, len(entryPoints)+1)
	start := 0
	for _, ep := range entryPoints {
		if ep <= start || ep > len(cabacData) {
			break
		}
		subs = append(subs, cabacData[start:ep])
		start = ep
	}
	return append(subs, cabacData[start:])
}

// decodeSaoParams decodes SAO parameters for one CTU per HEVC spec 7.3.8.3.
func decodeSaoParams(dec *cabac.Decoder, ctx []cabac.CtxState,
	ctbsX, ctbAddrRS int, saoParams []SaoParams,
	mergeLeftAvail, mergeUpAvail, saoLuma, saoChroma bool) SaoParams {

	var sao SaoParams

	// sao_merge_left_flag
	if mergeLeftAvail {
		mergeLeft := dec.DecodeDecision(&ctx[context.CtxSaoMergeFlag])
		if mergeLeft == 1 {
			return saoParams[ctbAddrRS-1]
		}
	}

	// sao_merge_up_flag
	if mergeUpAvail {
		mergeUp := dec.DecodeDecision(&ctx[context.CtxSaoMergeFlag])
		if mergeUp == 1 {
			return saoParams[ctbAddrRS-ctbsX]
		}
	}

	// Decode per-component SAO parameters
	// Per HEVC spec 7.3.8.3, type is decoded for luma (cIdx=0) and
	// once for chroma (cIdx=1), with cIdx=2 sharing type/class from cIdx=1.
	for cIdx := range 3 {
		if cIdx == 0 && !saoLuma {
			continue
		}
		if cIdx > 0 && !saoChroma {
			continue
		}

		// sao_type_idx: decoded for cIdx 0 and 1 only
		typeIdx := 0
		if cIdx < 2 {
			bin0 := dec.DecodeDecision(&ctx[context.CtxSaoTypeIdx])
			if bin0 == 1 {
				bin1 := dec.DecodeBypass()
				if bin1 == 0 {
					typeIdx = 1 // Band Offset
				} else {
					typeIdx = 2 // Edge Offset
				}
			}
			sao.TypeIdx[cIdx] = typeIdx
			if cIdx == 1 {
				// Cr shares type from Cb
				sao.TypeIdx[2] = typeIdx
			}
		} else {
			typeIdx = sao.TypeIdx[2]
		}

		if typeIdx == 0 {
			continue
		}

		// sao_offset_abs[i] (4 values per component)
		for i := range 4 {
			sao.Offsets[cIdx][i] = decodeSaoOffsetAbs(dec)
		}

		if typeIdx == 1 {
			// Band Offset: decode sign flags and band position
			for i := range 4 {
				if sao.Offsets[cIdx][i] != 0 {
					sign := dec.DecodeBypass()
					if sign == 1 {
						sao.Offsets[cIdx][i] = -sao.Offsets[cIdx][i]
					}
				}
			}
			// sao_band_position (5 bypass bins)
			sao.BandPos[cIdx] = int(dec.ReadBypassU(5))
			if cIdx == 1 {
				sao.BandPos[2] = sao.BandPos[1]
			}
		} else {
			// Edge Offset: categories 1,2 positive, 3,4 negative (spec Table 8-12)
			sao.Offsets[cIdx][2] = -sao.Offsets[cIdx][2]
			sao.Offsets[cIdx][3] = -sao.Offsets[cIdx][3]
			if cIdx < 2 {
				// sao_eo_class (2 bypass bins) - only for luma and once for chroma
				sao.EoClass[cIdx] = int(dec.ReadBypassU(2))
				if cIdx == 1 {
					sao.EoClass[2] = sao.EoClass[1]
				}
			}
		}
	}
	return sao
}

// decodeSaoOffsetAbs decodes one sao_offset_abs value using truncated Rice (bypass bins).
// Per HEVC spec 9.3.3.3, max value for 8-bit is (1<<(min(bitDepth,10)-5))-1 = 7.
func decodeSaoOffsetAbs(dec *cabac.Decoder) int {
	val := 0
	for val < 7 { // max for 8-bit
		bin := dec.DecodeBypass()
		if bin == 0 {
			break
		}
		val++
	}
	return val
}

// decodeCodingQuadtree recursively splits the CTU into CUs.
func decodeCodingQuadtree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize, depth int, p *Params,
	modeMap *intraModeMap, depthMap, skipMap *minCbMap, qps *qpState,
) ([]CodingUnit, error) {

	// Quantization group boundary, spec 7.3.8.4.
	if p.CuQpDeltaEnabled && log2CbSize >= p.Log2MinCuQpDeltaSize {
		qps.startQuantizationGroup(x0, y0, p.Log2CtbSize)
	}

	// Spec 7.3.8.4: split_cu_flag is only coded when the CU lies entirely inside
	// the picture. A CU that crosses the right or bottom edge is split without a
	// flag, inferred as 1 for as long as the size stays above MinCbLog2SizeY.
	// This is what makes heights like 360 and 1080 codable with a 16x16 CTU.
	cbSize := 1 << log2CbSize
	fits := x0+cbSize <= p.PicWidth && y0+cbSize <= p.PicHeight

	split := false
	switch {
	case log2CbSize <= p.Log2MinCbSize:
		// At the minimum CB size there is nothing left to split.
	case !fits:
		split = true
	default:
		// Determine context for split_cu_flag per HEVC spec 9.3.4.2.2 Table 9-37
		ctxInc := 0
		// condL: left neighbor at (x0-1, y0) has depth > current depth
		depthL := depthMap.get(x0-1, y0)
		if depthL >= 0 && depthL > depth {
			ctxInc++
		}
		// condA: above neighbor at (x0, y0-1) has depth > current depth
		depthA := depthMap.get(x0, y0-1)
		if depthA >= 0 && depthA > depth {
			ctxInc++
		}
		splitFlag := dec.DecodeDecision(&ctx[context.CtxSplitCuFlag+ctxInc])
		split = splitFlag == 1
	}

	if split {
		// Split into 4 sub-CUs
		newLog2CbSize := log2CbSize - 1
		halfSize := 1 << newLog2CbSize
		var allCUs []CodingUnit

		positions := [4][2]int{
			{x0, y0},
			{x0 + halfSize, y0},
			{x0, y0 + halfSize},
			{x0 + halfSize, y0 + halfSize},
		}

		for _, pos := range positions {
			if pos[0] < p.PicWidth && pos[1] < p.PicHeight {
				cus, err := decodeCodingQuadtree(dec, ctx, pos[0], pos[1],
					newLog2CbSize, depth+1, p, modeMap, depthMap, skipMap, qps)
				if err != nil {
					return nil, err
				}
				allCUs = append(allCUs, cus...)
			}
		}
		return allCUs, nil
	}

	// Store CU depth in map
	depthMap.set(x0, y0, cbSize, depth)

	// Decode coding unit
	cu, err := decodeCodingUnit(dec, ctx, x0, y0, log2CbSize, p, modeMap, skipMap, qps)
	if err != nil {
		return nil, err
	}
	return []CodingUnit{cu}, nil
}

// decodeCodingUnit decodes a single CU.
func decodeCodingUnit(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, log2CbSize int, p *Params, modeMap *intraModeMap, skipMap *minCbMap,
	qps *qpState) (CodingUnit, error) {

	cu := CodingUnit{
		X0:         x0,
		Y0:         y0,
		Log2CbSize: log2CbSize,
	}

	cbSize := 1 << log2CbSize

	// For P/B-slices: decode cu_skip_flag
	if p.SliceType != context.SliceTypeI {
		// The context is the number of neighbours that were themselves skipped,
		// left plus above (spec 9.3.4.2.2). An unavailable neighbour contributes
		// nothing, and the map reads -1 exactly where a neighbour is unavailable:
		// undecoded, or in another slice or tile.
		ctxInc := 0
		if skipMap.get(x0-1, y0) == 1 {
			ctxInc++
		}
		if skipMap.get(x0, y0-1) == 1 {
			ctxInc++
		}
		skipFlag := dec.DecodeDecision(&ctx[context.CtxCuSkipFlag+ctxInc])
		cu.SkipFlag = skipFlag == 1
		skipMap.set(x0, y0, cbSize, int(skipFlag))

		if cu.SkipFlag {
			cu.PredMode = 0 // inter
			// merge_idx: truncated unary, bypass coded with context for first bin
			if p.MaxNumMergeCand > 1 {
				// First bin is context-coded
				bin0 := dec.DecodeDecision(&ctx[context.CtxMergeIdx])
				if bin0 == 1 {
					idx := 1
					for idx < p.MaxNumMergeCand-1 {
						bin := dec.DecodeBypass()
						if bin == 0 {
							break
						}
						idx++
					}
					cu.MergeIdx = idx
				}
			}
			// Store mode in map (use DC as placeholder for intra mode map)
			modeMap.set(x0, y0, cbSize, 1)
			cu.QpY = qps.currentQP
			qps.setQP(x0, y0, cbSize, cu.QpY)
			return cu, nil
		}
	}

	// pred_mode_flag distinguishes an intra CU from an inter one in a P or B
	// slice (spec 7.3.8.5); an I slice has neither the flag nor the choice.
	if p.SliceType != context.SliceTypeI {
		if dec.DecodeDecision(&ctx[context.CtxPredModeFlag]) == 0 {
			// MODE_INTER, and not the zero-motion skip that reconstruction can
			// handle: the prediction unit that follows carries merge flags,
			// reference indices and motion vector differences, none of which this
			// decoder derives motion from. Stopping here is the point: decoding
			// on would return a picture that looks plausible and is wrong.
			return cu, fmt.Errorf(
				"inter CU at (%d,%d): motion compensation is not implemented, only zero-motion skip CUs decode",
				x0, y0)
		}
	}

	cu.PredMode = 1 // intra

	// part_mode: for intra CU, 0=2Nx2N, 1=NxN
	// NxN is only allowed when log2CbSize == log2MinCbSize
	if log2CbSize == p.Log2MinCbSize {
		partModeBin := dec.DecodeDecision(&ctx[context.CtxPartMode])
		if partModeBin == 0 {
			cu.PartMode = 1 // NxN
		} else {
			cu.PartMode = 0 // 2Nx2N
		}
	}

	// Decode intra prediction modes
	numPU := 1
	puSize := cbSize
	if cu.PartMode == 1 {
		numPU = 4
		puSize = cbSize / 2
	}

	// PU positions (raster order within CU)
	puPositions := make([][2]int, numPU)
	if numPU == 1 {
		puPositions[0] = [2]int{x0, y0}
	} else {
		half := cbSize / 2
		puPositions[0] = [2]int{x0, y0}
		puPositions[1] = [2]int{x0 + half, y0}
		puPositions[2] = [2]int{x0, y0 + half}
		puPositions[3] = [2]int{x0 + half, y0 + half}
	}

	// prev_intra_luma_pred_flag for each PU
	prevFlags := make([]bool, numPU)
	for i := range numPU {
		flag := dec.DecodeDecision(&ctx[context.CtxPrevIntraLumaPredFlag])
		prevFlags[i] = flag == 1
	}

	// mpm_idx or rem_intra_luma_pred_mode for each PU
	for i := range numPU {
		px, py := puPositions[i][0], puPositions[i][1]

		// Derive MPM list from left and above neighbors (HEVC spec 8.4.2)
		candA := modeMap.get(px-1, py) // left neighbor
		candB := modeMap.get(px, py-1) // above neighbor
		// Intra mode prediction does not cross a horizontal CTB boundary:
		// candB is INTRA_DC when py-1 lies outside the current CTB.
		if py-1 < (py>>p.Log2CtbSize)<<p.Log2CtbSize {
			candB = -1
		}
		mpmList := deriveMPM(candA, candB)
		if prevFlags[i] {
			// mpm_idx: decoded as truncated unary, max 2
			mpmIdx := 0
			if dec.DecodeBypass() == 1 {
				mpmIdx++
				if dec.DecodeBypass() == 1 {
					mpmIdx++
				}
			}
			cu.IntraLumaMode[i] = mpmList[mpmIdx]
		} else {
			// rem_intra_luma_pred_mode: 5 bypass bins
			rem := int(dec.ReadBypassU(5))
			// Convert rem to actual mode (skipping MPM entries)
			sorted := sortMPM(mpmList)
			mode := rem
			for _, mpm := range sorted {
				if mode >= mpm {
					mode++
				}
			}
			cu.IntraLumaMode[i] = mode
		}

		// Store mode in map so subsequent PUs can use it as neighbor
		modeMap.set(px, py, puSize, cu.IntraLumaMode[i])
	}

	// intra_chroma_pred_mode
	chromaPredFlag := dec.DecodeDecision(&ctx[context.CtxIntraChromaPredMode])
	if chromaPredFlag == 0 {
		// DM mode (derived from luma)
		cu.IntraChromaMode = 4 // DM
	} else {
		// 2 bypass bins for mode index (0-3: Planar, Angular26, Angular10, DC)
		chromaIdx := int(dec.ReadBypassU(2))
		chromaModes := [4]int{0, 26, 10, 1}
		cu.IntraChromaMode = chromaModes[chromaIdx]
	}

	// IntraSplitFlag: set when intra CU uses NxN partition mode
	intraSplitFlag := cu.PartMode == 1 // SIZE_NxN

	// Decode transform tree
	tus, err := decodeTransformTree(dec, ctx, x0, y0, x0, y0, log2CbSize, log2CbSize,
		0, cbSize, true, true, p, cu.IntraLumaMode, cu.IntraChromaMode, x0, y0, qps,
		intraSplitFlag, 0)
	if err != nil {
		return cu, err
	}
	cu.TransformUnits = tus
	cu.QpY = qps.currentQP
	qps.setQP(x0, y0, cbSize, cu.QpY)

	return cu, nil
}

// decodeTransformTree recursively decodes the transform quadtree.
func decodeTransformTree(dec *cabac.Decoder, ctx []cabac.CtxState,
	x0, y0, xBase, yBase int, log2TrafoSize, log2CbSize int,
	trafoDepth, cbSize int, cbfCb, cbfCr bool, p *Params,
	lumaIntraModes [4]int, intraChromaMode int, cuX0, cuY0 int, qps *qpState,
	intraSplitFlag bool, blkIdx int) ([]TransformUnit, error) {

	log2MaxTrafoSize := p.Log2MaxTrafoSize
	log2MinTrafoSize := p.Log2MinTrafoSize
	maxTrafoDepth := p.MaxTransformHierarchyDepthIntra
	if intraSplitFlag {
		maxTrafoDepth++
	}

	split := false
	if log2TrafoSize <= log2MaxTrafoSize &&
		log2TrafoSize > log2MinTrafoSize &&
		trafoDepth < maxTrafoDepth &&
		(!intraSplitFlag || trafoDepth != 0) &&
		log2TrafoSize > 2 {
		splitFlag := dec.DecodeDecision(&ctx[context.CtxSplitTransformFlag+int(5-log2TrafoSize)])
		split = splitFlag == 1
	} else if log2TrafoSize > log2MaxTrafoSize ||
		(intraSplitFlag && trafoDepth == 0) {
		split = true
	}

	// cbf_cb and cbf_cr: only signaled when log2TrafoSize > 2 for 4:2:0
	// (chroma block must be at least 4x4, which requires luma >= 8x8)
	if log2TrafoSize > 2 {
		if trafoDepth == 0 || cbfCb {
			cbfCbFlag := dec.DecodeDecision(&ctx[context.CtxCbfCb+trafoDepth])
			cbfCb = cbfCbFlag == 1
		}
		if trafoDepth == 0 || cbfCr {
			cbfCrFlag := dec.DecodeDecision(&ctx[context.CtxCbfCr+trafoDepth])
			cbfCr = cbfCrFlag == 1
		}
	}

	if split {
		newLog2TrafoSize := log2TrafoSize - 1
		halfSize := 1 << newLog2TrafoSize
		var allTUs []TransformUnit

		positions := [4][2]int{
			{x0, y0},
			{x0 + halfSize, y0},
			{x0, y0 + halfSize},
			{x0 + halfSize, y0 + halfSize},
		}

		for childIdx, pos := range positions {
			tus, err := decodeTransformTree(dec, ctx, pos[0], pos[1], x0, y0,
				newLog2TrafoSize, log2CbSize, trafoDepth+1, cbSize,
				cbfCb, cbfCr, p, lumaIntraModes, intraChromaMode, cuX0, cuY0, qps,
				intraSplitFlag, childIdx)
			if err != nil {
				return nil, err
			}
			allTUs = append(allTUs, tus...)
		}
		return allTUs, nil
	}

	// Leaf TU: decode cbf_luma and residual
	tu := TransformUnit{
		X0:         x0,
		Y0:         y0,
		Log2TrSize: log2TrafoSize,
		CbfCb:      cbfCb,
		CbfCr:      cbfCr,
	}

	// cbf_luma: context index is !trafoDepth per HEVC spec / FFmpeg
	cbfLumaCtx := 0
	if trafoDepth == 0 {
		cbfLumaCtx = 1
	}
	cbfLumaFlag := dec.DecodeDecision(&ctx[context.CtxCbfLuma+cbfLumaCtx])
	tu.CbfLuma = cbfLumaFlag == 1

	// Decode cu_qp_delta if enabled and not yet coded for this QG
	if p.CuQpDeltaEnabled && !qps.isCuQpDeltaCoded && (tu.CbfLuma || cbfCb || cbfCr) {
		delta := decodeCuQpDelta(dec, ctx)
		// QpY = ( ( qPY_PRED + CuQpDeltaVal + 52 ) % 52 ) for 8-bit, which wraps
		// rather than clamping.
		qps.currentQP = ((qps.qpYPred+delta+52)%52 + 52) % 52
		qps.isCuQpDeltaCoded = true
	}

	// Parse transform_skip_flag for 4x4 TUs when enabled
	if p.TransformSkipEnabled && log2TrafoSize == 2 {
		if tu.CbfLuma {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag])
			tu.TransformSkipLuma = tsFlag == 1
		}
	}

	// Decode residual coefficients
	trSize := 1 << log2TrafoSize

	// Luma prediction mode of this TU. Only an NxN partitioning gives the four
	// quadrants of a CU their own mode; a 2Nx2N CU has one mode for all its TUs.
	lumaMode := lumaIntraModes[0]
	if intraSplitFlag {
		puIdx := 0
		halfCb := cbSize / 2
		if x0 >= cuX0+halfCb {
			puIdx += 1
		}
		if y0 >= cuY0+halfCb {
			puIdx += 2
		}
		lumaMode = lumaIntraModes[puIdx]
	}

	// Mode-dependent scan (spec 7.4.9.11): 4x4 and 8x8 luma transform blocks.
	lumaScanIdx := ScanIdxForIntraMode(lumaMode, log2TrafoSize, true)

	if tu.CbfLuma {
		coeffs, err := decodeResidualCoding(dec, ctx, log2TrafoSize, true, lumaScanIdx, p.SignDataHidingEnabled)
		if err != nil {
			return nil, fmt.Errorf("luma residual: %w", err)
		}
		tu.LumaCoeffs = coeffs
	} else {
		tu.LumaCoeffs = make([]int32, trSize*trSize)
	}

	// Chroma TUs are half the luma size (4:2:0), min 4x4
	chromaLog2TrSize := log2TrafoSize - 1
	if chromaLog2TrSize < 2 {
		chromaLog2TrSize = 2
	}
	chromaTrSize := 1 << chromaLog2TrSize

	// Spec 7.3.8.10: with 4x4 luma transform blocks the chroma residual covers
	// the whole 8x8 group and is coded once, at blkIdx 3 — the LAST of the four
	// children, not the first — for the area at (xBase, yBase). Reading it at
	// the first child consumes those bins several blocks too early and
	// desynchronises everything after. Only 4x4 luma TBs reach this, which no
	// flat-colour generator produces, so it took real encoder output to show.
	parseChroma := log2TrafoSize > 2 || blkIdx == 3
	tu.ChromaX0, tu.ChromaY0 = x0, y0
	if log2TrafoSize == 2 {
		tu.ChromaX0, tu.ChromaY0 = xBase, yBase
	}
	tu.HasChroma = parseChroma

	// Chroma prediction mode (IntraPredModeC, spec 8.4.3): mode 4 is DM
	// (derived from luma), and an explicit mode that collides with the luma
	// mode is substituted by mode 34. For 4:2:0 the chroma block belongs to the
	// CU, so the mode comes from the CU's first luma PU.
	chromaMode := intraChromaMode
	switch chromaMode {
	case 4:
		chromaMode = lumaIntraModes[0]
	case lumaIntraModes[0]:
		chromaMode = 34
	}
	// Mode-dependent scan (spec 7.4.9.11): 4x4 chroma transform blocks.
	chromaScanIdx := ScanIdxForIntraMode(chromaMode, chromaLog2TrSize, false)

	// Parse chroma transform_skip_flag for 4x4 chroma TUs
	if p.TransformSkipEnabled && parseChroma && chromaLog2TrSize == 2 {
		if tu.CbfCb {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag+1])
			tu.TransformSkipCb = tsFlag == 1
		}
		if tu.CbfCr {
			tsFlag := dec.DecodeDecision(&ctx[context.CtxTransformSkipFlag+1])
			tu.TransformSkipCr = tsFlag == 1
		}
	}

	if parseChroma && tu.CbfCb {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false, chromaScanIdx, p.SignDataHidingEnabled)
		if err != nil {
			return nil, fmt.Errorf("cb residual: %w", err)
		}
		tu.CbCoeffs = coeffs
	} else {
		tu.CbCoeffs = make([]int32, chromaTrSize*chromaTrSize)
	}

	if parseChroma && tu.CbfCr {
		coeffs, err := decodeResidualCoding(dec, ctx, chromaLog2TrSize, false, chromaScanIdx, p.SignDataHidingEnabled)
		if err != nil {
			return nil, fmt.Errorf("cr residual: %w", err)
		}
		tu.CrCoeffs = coeffs
	} else {
		tu.CrCoeffs = make([]int32, chromaTrSize*chromaTrSize)
	}

	return []TransformUnit{tu}, nil
}

// decodeResidualCoding decodes a single residual block using CABAC.
// Returns coefficients in raster scan order.
func decodeResidualCoding(dec *cabac.Decoder, ctx []cabac.CtxState,
	log2TrafoSize int, isLuma bool, scanIdx int, signDataHiding bool) ([]int32, error) {

	trSize := 1 << log2TrafoSize

	// HEVC spec 7.3.8.11: both prefixes first, then both suffixes
	lastSigCoeffXPrefix := decodeLastSigCoeffPrefix(dec, ctx, log2TrafoSize, isLuma, true)
	lastSigCoeffYPrefix := decodeLastSigCoeffPrefix(dec, ctx, log2TrafoSize, isLuma, false)
	lastSigCoeffX := decodeLastSigCoeffSuffix(dec, lastSigCoeffXPrefix)
	lastSigCoeffY := decodeLastSigCoeffSuffix(dec, lastSigCoeffYPrefix)

	// Spec 7.3.8.11: for the vertical scan the coded x and y are swapped, so
	// that one set of context models serves both scan directions.
	if scanIdx == 2 {
		lastSigCoeffX, lastSigCoeffY = lastSigCoeffY, lastSigCoeffX
	}

	// Convert to sub-block coordinates
	log2SbSize := 2 // 4x4 sub-blocks
	if log2TrafoSize == 2 {
		log2SbSize = 2
	}

	// Number of sub-blocks
	numSbX := trSize >> log2SbSize
	numSbY := trSize >> log2SbSize

	// Find the sub-block containing the last significant coefficient
	lastSbX := lastSigCoeffX >> log2SbSize
	lastSbY := lastSigCoeffY >> log2SbSize

	// Scan order depends on scanIdx: 0=diagonal, 1=horizontal, 2=vertical
	sbScanOrder := ScanOrder(numSbX, numSbY, scanIdx)
	coeffScanOrder := ScanOrder(1<<log2SbSize, 1<<log2SbSize, scanIdx)

	// Find lastScanPos and lastSubBlock
	lastSubBlock := len(sbScanOrder) - 1
	lastScanPos := (1 << (2 * log2SbSize)) - 1

	for i, pos := range sbScanOrder {
		if pos[0] == lastSbX && pos[1] == lastSbY {
			lastSubBlock = i
			break
		}
	}

	// Find scan position within the sub-block
	localX := lastSigCoeffX - (lastSbX << log2SbSize)
	localY := lastSigCoeffY - (lastSbY << log2SbSize)
	for i, pos := range coeffScanOrder {
		if pos[0] == localX && pos[1] == localY {
			lastScanPos = i
			break
		}
	}

	coeffs := make([]int32, trSize*trSize)

	// Track which sub-blocks are coded for context derivation
	codedSbFlags := make([][]bool, numSbY)
	for j := range codedSbFlags {
		codedSbFlags[j] = make([]bool, numSbX)
	}

	// Decode sub-blocks in reverse scan order
	// lastC1 tracks the c1 state from the previous sub-block's greater1 decoding.
	// c1=0 means previous sub-block had a coefficient with greater1_flag=1.
	// Initialized to 1 (no previous sub-block).
	lastC1 := 1
	ctxSet := 0

	for i := lastSubBlock; i >= 0; i-- {
		sbX := sbScanOrder[i][0]
		sbY := sbScanOrder[i][1]

		// coded_sub_block_flag
		codedSubBlock := true
		if i > 0 && i < lastSubBlock {
			// Context derivation per HEVC spec 9.3.4.2.4
			// Check right neighbor (sbX+1, sbY) and below neighbor (sbX, sbY+1)
			csbfRight := 0
			csbfBelow := 0
			if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
				csbfRight = 1
			}
			if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
				csbfBelow = 1
			}
			ctxInc := min(csbfRight+csbfBelow, 1)

			sbCtx := context.CtxCodedSubBlockFlag
			if !isLuma {
				sbCtx += 2
			}
			flag := dec.DecodeDecision(&ctx[sbCtx+ctxInc])
			codedSubBlock = flag == 1
		} else if i == lastSubBlock {
			codedSubBlock = true
		}

		codedSbFlags[sbY][sbX] = codedSubBlock

		if !codedSubBlock {
			continue
		}

		// Decode significant coefficient flags within sub-block
		numCoeffs := 1 << (2 * log2SbSize) // 16 for 4x4 sub-blocks
		sigFlags := make([]bool, numCoeffs)
		firstScanPos := numCoeffs - 1
		lastScanPosInSb := -1

		// Compute prevCsbf for sig_coeff_flag context derivation
		// prevCsbf = csbfRight + 2*csbfBelow
		prevCsbf := 0
		if sbX+1 < numSbX && codedSbFlags[sbY][sbX+1] {
			prevCsbf += 1
		}
		if sbY+1 < numSbY && codedSbFlags[sbY+1][sbX] {
			prevCsbf += 2
		}

		startPos := numCoeffs - 1
		if i == lastSubBlock {
			sigFlags[lastScanPos] = true
			lastScanPosInSb = lastScanPos
			firstScanPos = lastScanPos
			startPos = lastScanPos - 1
		}

		// implicitNonZeroCoeff: for middle sub-blocks (coded_sub_block_flag
		// explicitly decoded), if no sig_coeff at positions 1..15, position 0
		// is implicitly significant without decoding a CABAC bin.
		implicitNonZeroCoeff := (i > 0 && i < lastSubBlock)

		for n := startPos; n >= 0; n-- {
			// sig_coeff_flag
			if n > 0 || !implicitNonZeroCoeff {
				cx := coeffScanOrder[n][0]
				cy := coeffScanOrder[n][1]
				sigCtx := GetSigCtxInc(cx, cy, log2TrafoSize, isLuma, sbX, sbY, scanIdx, prevCsbf)
				flag := dec.DecodeDecision(&ctx[context.CtxSigCoeffFlag+sigCtx])
				sigFlags[n] = flag == 1
				if sigFlags[n] {
					if lastScanPosInSb < 0 {
						lastScanPosInSb = n
					}
					firstScanPos = n
					implicitNonZeroCoeff = false
				}
			} else {
				// DC coefficient of coded sub-block is always significant
				sigFlags[0] = true
				firstScanPos = 0
				if lastScanPosInSb < 0 {
					lastScanPosInSb = 0
				}
			}
		}

		// Decode coefficient levels
		// coeff_abs_level_greater1_flag
		greater1Flags := make([]bool, numCoeffs)
		greater1Decoded := make([]bool, numCoeffs) // tracks if greater1 was decoded
		firstGreater1Pos := -1
		numGreater1InSb := 0

		if isLuma && i > 0 {
			ctxSet = 2
		} else {
			ctxSet = 0
		}
		if lastC1 == 0 {
			ctxSet++
		}

		// c1 tracks which of the 4 contexts within the set to use.
		// Reset to 1 at the start of each sub-block.
		c1 := 1
		chromaOffset := 0
		if !isLuma {
			chromaOffset = 16
		}

		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			if numGreater1InSb < 8 {
				greater1Ctx := ctxSet*4 + c1 + chromaOffset
				flag := dec.DecodeDecision(&ctx[context.CtxCoeffAbsLevelGreater1+greater1Ctx])
				greater1Flags[n] = flag == 1
				greater1Decoded[n] = true
				numGreater1InSb++
				if greater1Flags[n] {
					if firstGreater1Pos < 0 {
						firstGreater1Pos = n
					}
					c1 = 0
				} else if c1 > 0 && c1 < 3 {
					c1++
				}
			}
		}

		// Update lastC1 for next sub-block's ctxSet derivation
		lastC1 = c1

		// coeff_abs_level_greater2_flag (at most 1 per sub-block)
		greater2Flag := false
		if firstGreater1Pos >= 0 {
			greater2Ctx := ctxSet
			if !isLuma {
				greater2Ctx += 4
			}
			flag := dec.DecodeDecision(&ctx[context.CtxCoeffAbsLevelGreater2+greater2Ctx])
			greater2Flag = flag == 1
		}

		// Sign flags (bypass coded)
		signFlags := make([]bool, numCoeffs)
		signHidden := signDataHiding && (lastScanPosInSb-firstScanPos) > 3
		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if sigFlags[n] {
				if signHidden && n == firstScanPos {
					// Sign will be inferred from parity
					continue
				}
				sign := dec.DecodeBypass()
				signFlags[n] = sign == 1
			}
		}

		// coeff_abs_level_remaining (bypass coded, Golomb-Rice)
		absLevels := make([]int32, numCoeffs)
		cRiceParam := 0

		for n := lastScanPosInSb; n >= firstScanPos; n-- {
			if !sigFlags[n] {
				continue
			}
			baseLevel := int32(1)
			if greater1Decoded[n] && greater1Flags[n] {
				baseLevel = 2
			}
			if n == firstGreater1Pos && greater2Flag {
				baseLevel = 3
			}

			// Decode coeff_abs_level_remaining per HEVC spec:
			// coeff_has_max_base_level is initially 1 for each sig coeff,
			// set to 0 when greater1_flag==0, and overridden with greater2_flag
			// at firstGreater1Pos.
			absRemaining := int32(0)
			needRemaining := !greater1Decoded[n] || // bypassed (>=8 sig coeffs)
				(greater1Decoded[n] && greater1Flags[n] && n != firstGreater1Pos) || // greater1=1, not first
				(n == firstGreater1Pos && greater2Flag) // first greater1 with greater2=1
			if needRemaining {
				absRemaining = decodeAbsLevelRemaining(dec, cRiceParam)
			}

			absLevel := baseLevel + absRemaining
			absLevels[n] = absLevel

			// Update Rice parameter
			if absLevel > int32(3*(1<<cRiceParam)) && cRiceParam < 4 {
				cRiceParam++
			}
		}

		// Sign data hiding: infer sign of firstScanPos from parity
		if signHidden {
			sumAbs := int32(0)
			for n := firstScanPos; n <= lastScanPosInSb; n++ {
				sumAbs += absLevels[n]
			}
			if sumAbs%2 != 0 {
				// Odd sum → sign is negative
				signFlags[firstScanPos] = true
			}
		}

		// Place coefficients into the output array
		sbPixelX := sbX << log2SbSize
		sbPixelY := sbY << log2SbSize

		for n := range numCoeffs {
			if absLevels[n] != 0 {
				cx := coeffScanOrder[n][0]
				cy := coeffScanOrder[n][1]
				px := sbPixelX + cx
				py := sbPixelY + cy
				val := absLevels[n]
				if signFlags[n] {
					val = -val
				}
				coeffs[py*trSize+px] = val
			}
		}
	}

	return coeffs, nil
}

// decodeLastSigCoeffPrefix decodes last_sig_coeff_x_prefix or last_sig_coeff_y_prefix.
// Returns the raw prefix value. Suffix is decoded separately per HEVC spec 7.3.8.11.
func decodeLastSigCoeffPrefix(dec *cabac.Decoder, ctx []cabac.CtxState,
	log2TrafoSize int, isLuma bool, isX bool) int {

	ctxBase := context.CtxLastSigCoeffXPrefix
	if !isX {
		ctxBase = context.CtxLastSigCoeffYPrefix
	}

	// Context selection per HEVC spec Table 9-32/9-33
	var ctxOffset, ctxShift int
	if isLuma {
		ctxOffset = 3*(log2TrafoSize-2) + ((log2TrafoSize - 1) >> 2)
		ctxShift = (log2TrafoSize + 1) >> 2
	} else {
		ctxOffset = 15 // chroma contexts start at offset 15
		ctxShift = log2TrafoSize - 2
		if ctxShift < 0 {
			ctxShift = 0
		}
	}

	maxPrefix := (log2TrafoSize << 1) - 1
	prefix := 0
	for prefix < maxPrefix {
		ctxInc := prefix >> ctxShift
		bin := dec.DecodeDecision(&ctx[ctxBase+ctxOffset+ctxInc])
		if bin == 0 {
			break
		}
		prefix++
	}

	return prefix
}

// decodeLastSigCoeffSuffix decodes the suffix for last_sig_coeff_x or last_sig_coeff_y.
func decodeLastSigCoeffSuffix(dec *cabac.Decoder, prefix int) int {
	if prefix > 3 {
		suffixLen := (prefix >> 1) - 1
		suffix := int(dec.ReadBypassU(suffixLen))
		return ((2 + (prefix & 1)) << suffixLen) + suffix
	}
	return prefix
}

// decodeAbsLevelRemaining decodes coeff_abs_level_remaining using
// Truncated Rice + Exp-Golomb (bypass bins).
func decodeAbsLevelRemaining(dec *cabac.Decoder, cRiceParam int) int32 {
	// Truncated Rice prefix
	prefix := 0
	for prefix < 20 { // HEVC max prefix length
		bin := dec.DecodeBypass()
		if bin == 0 {
			break
		}
		prefix++
	}

	if prefix < 3 { // cMax = 3 for truncated Rice
		// suffix is cRiceParam bits
		suffix := int32(0)
		if cRiceParam > 0 {
			suffix = int32(dec.ReadBypassU(cRiceParam))
		}
		return int32(prefix)<<uint(cRiceParam) + suffix
	}

	// Exp-Golomb suffix
	suffixLen := prefix - 3 + cRiceParam
	suffix := int32(dec.ReadBypassU(suffixLen))
	return (((int32(1) << uint(prefix-3)) + 3 - 1) << uint(cRiceParam)) + suffix
}

// decodeCuQpDelta decodes cu_qp_delta_abs and cu_qp_delta_sign_flag per HEVC spec 9.3.3.5.
func decodeCuQpDelta(dec *cabac.Decoder, ctx []cabac.CtxState) int {
	// prefix: up to 5 context-coded bins
	prefix := 0
	inc := 0
	for prefix < 5 {
		bin := dec.DecodeDecision(&ctx[context.CtxCuQpDeltaAbs+inc])
		if bin == 0 {
			break
		}
		prefix++
		inc = 1
	}
	// suffix: bypass Exp-Golomb if prefix >= 5
	suffix := 0
	if prefix >= 5 {
		k := 0
		for dec.DecodeBypass() == 1 {
			suffix += 1 << k
			k++
		}
		for k > 0 {
			k--
			suffix += int(dec.DecodeBypass()) << k
		}
	}
	abs := prefix + suffix
	if abs == 0 {
		return 0
	}
	// sign
	sign := dec.DecodeBypass()
	if sign == 1 {
		return -abs
	}
	return abs
}

// GetSigCtxInc returns the context index for sig_coeff_flag.
// Based on HEVC spec 9.3.4.2.6.
// prevCsbf = csbfRight + 2*csbfBelow (coded_sub_block_flag of right and below neighbors).
func GetSigCtxInc(cx, cy, log2TrafoSize int, isLuma bool, sbX, sbY, scanIdx, prevCsbf int) int {
	if log2TrafoSize == 2 {
		// 4x4 transform block: spec 9.3.4.2.5 uses one position map, Table 9-42.
		// It does not depend on the scan: a scan-dependent variant picks the
		// wrong context for vertically scanned blocks, and while the decoded
		// bins often still come out right, the arithmetic decoder's state
		// diverges and every later bin in the slice is affected.
		ctxIdxMap := [16]int{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8, 8}
		sigCtx := ctxIdxMap[cy*4+cx]
		if isLuma {
			return sigCtx
		}
		return sigCtx + 27
	}

	// For larger blocks (8x8+), per HEVC spec 9.3.4.2.6
	xP := cx // position within 4x4 sub-block
	yP := cy

	// DC position of DC sub-block
	if xP+yP == 0 && sbX == 0 && sbY == 0 {
		sigCtx := 0
		if isLuma {
			return sigCtx
		}
		return sigCtx + 27
	}

	// Compute base sigCtx from prevCsbf and position within sub-block
	var sigCtx int
	switch prevCsbf {
	case 0:
		if xP+yP >= 3 {
			sigCtx = 0
		} else if xP+yP > 0 {
			sigCtx = 1
		} else {
			sigCtx = 2
		}
	case 1: // right neighbor coded
		switch yP {
		case 0:
			sigCtx = 2
		case 1:
			sigCtx = 1
		default:
			sigCtx = 0
		}
	case 2: // below neighbor coded
		switch xP {
		case 0:
			sigCtx = 2
		case 1:
			sigCtx = 1
		default:
			sigCtx = 0
		}
	default: // prevCsbf == 3, both neighbors coded
		sigCtx = 2
	}

	if isLuma {
		if sbX+sbY > 0 {
			sigCtx += 3
		}
		numSbX := (1 << log2TrafoSize) >> 2
		if numSbX == 2 { // 8x8 TU
			if scanIdx == 0 {
				sigCtx += 9
			} else {
				sigCtx += 15
			}
		} else { // 16x16 or 32x32 TU
			sigCtx += 21
		}
		return sigCtx
	}

	// Chroma
	numSbX := (1 << log2TrafoSize) >> 2
	if numSbX == 2 { // 8x8
		sigCtx += 9
	} else {
		sigCtx += 12
	}
	return sigCtx + 27
}

// diagonalScanOrder generates the HEVC diagonal scan order for an NxN block.
// HEVC spec Table 6-5: diagonal scan, within each diagonal goes up-right (x++, y--).
func diagonalScanOrder(width, height int) [][2]int {
	var order [][2]int
	for s := 0; s < width+height-1; s++ {
		y := min(s, height-1)
		x := s - y
		for x < width && y >= 0 {
			order = append(order, [2]int{x, y})
			x++
			y--
		}
	}
	return order
}

// horizontalScanOrder generates a row-major scan order for an NxN block.
func horizontalScanOrder(width, height int) [][2]int {
	order := make([][2]int, 0, width*height)
	for y := range height {
		for x := range width {
			order = append(order, [2]int{x, y})
		}
	}
	return order
}

// verticalScanOrder generates a column-major scan order for an NxN block.
func verticalScanOrder(width, height int) [][2]int {
	order := make([][2]int, 0, width*height)
	for x := range width {
		for y := range height {
			order = append(order, [2]int{x, y})
		}
	}
	return order
}

// ScanIdxForIntraMode returns the residual scan order of an intra transform
// block per HEVC spec 7.4.9.11. The scan is prediction-mode dependent for 4x4
// transform blocks of any component and for 8x8 luma transform blocks; all
// other sizes use the up-right diagonal scan. Near-horizontal prediction
// (modes 6-14) uses the vertical scan, near-vertical prediction (modes 22-30)
// the horizontal scan.
//
// log2TrafoSize is the size of the transform block of the component itself
// (so 2 for the 4x4 chroma block of an 8x8 luma block in 4:2:0), and mode is
// that component's intra prediction mode (IntraPredModeY or IntraPredModeC).
func ScanIdxForIntraMode(mode, log2TrafoSize int, isLuma bool) int {
	modeDependent := log2TrafoSize == 2 || (log2TrafoSize == 3 && isLuma)
	if !modeDependent {
		return 0 // diagonal
	}
	switch {
	case mode >= 6 && mode <= 14:
		return 2 // vertical
	case mode >= 22 && mode <= 30:
		return 1 // horizontal
	}
	return 0 // diagonal
}

// ScanOrder returns the scan order for the given scanIdx.
// scanIdx: 0=diagonal, 1=horizontal, 2=vertical.
func ScanOrder(width, height, scanIdx int) [][2]int {
	switch scanIdx {
	case 1:
		return horizontalScanOrder(width, height)
	case 2:
		return verticalScanOrder(width, height)
	default:
		return diagonalScanOrder(width, height)
	}
}

// sortMPM sorts a 3-element MPM list in ascending order.
func sortMPM(mpm [3]int) [3]int {
	sorted := mpm
	if sorted[0] > sorted[1] {
		sorted[0], sorted[1] = sorted[1], sorted[0]
	}
	if sorted[1] > sorted[2] {
		sorted[1], sorted[2] = sorted[2], sorted[1]
	}
	if sorted[0] > sorted[1] {
		sorted[0], sorted[1] = sorted[1], sorted[0]
	}
	return sorted
}

// minCbMap holds one value per minimum coding block, which is the granularity
// the context derivations need for their left and above neighbours: the CU split
// depth for split_cu_flag, and cu_skip_flag for the next CU's own. A value of -1
// means no CU has been decoded there — which is also how a neighbour in another
// slice or another tile reads, since the map is per slice and reset at every
// tile boundary.
type minCbMap struct {
	values     []int // flat array, indexed as [y/minCb * widthMinCb + x/minCb]
	minCbSize  int
	widthMinCb int
}

func newMinCbMap(picW, picH, minCbSize int) *minCbMap {
	w := (picW + minCbSize - 1) / minCbSize
	h := (picH + minCbSize - 1) / minCbSize
	values := make([]int, w*h)
	for i := range values {
		values[i] = -1 // unavailable
	}
	return &minCbMap{values: values, minCbSize: minCbSize, widthMinCb: w}
}

// reset marks every block unavailable again, which is what crossing into a new
// tile means: spec 6.4.1 allows no neighbour from another tile.
func (m *minCbMap) reset() {
	for i := range m.values {
		m.values[i] = -1
	}
}

// set stores the depth for all minCb blocks covered by the CU at (x0,y0) of given
// size. Writes outside the map are dropped rather than panicking, so a malformed
// stream cannot take the decoder down.
func (m *minCbMap) set(x0, y0, cuSize, value int) {
	heightMinCb := len(m.values) / m.widthMinCb
	for y := y0 / m.minCbSize; y < (y0+cuSize)/m.minCbSize; y++ {
		if y < 0 || y >= heightMinCb {
			continue
		}
		for x := x0 / m.minCbSize; x < (x0+cuSize)/m.minCbSize; x++ {
			if x < 0 || x >= m.widthMinCb {
				continue
			}
			m.values[y*m.widthMinCb+x] = value
		}
	}
}

// get returns the CU depth at pixel position (x,y), or -1 if unavailable.
func (m *minCbMap) get(x, y int) int {
	bx, by := x/m.minCbSize, y/m.minCbSize
	if bx < 0 || by < 0 || bx >= m.widthMinCb || by*m.widthMinCb+bx >= len(m.values) {
		return -1
	}
	return m.values[by*m.widthMinCb+bx]
}

// intraModeMap tracks decoded intra luma prediction modes at 4x4 PU granularity.
type intraModeMap struct {
	modes  []int // flat array, indexed as [y/4 * width4 + x/4]
	width4 int   // picture width in 4-sample units
}

func newIntraModeMap(picW, picH int) *intraModeMap {
	w := (picW + 3) / 4
	h := (picH + 3) / 4
	modes := make([]int, w*h)
	for i := range modes {
		modes[i] = -1 // unavailable
	}
	return &intraModeMap{modes: modes, width4: w}
}

// reset marks every block unavailable again; see minCbMap.reset.
func (m *intraModeMap) reset() {
	for i := range m.modes {
		m.modes[i] = -1
	}
}

// set stores a mode for all 4x4 blocks within the PU at (x0, y0) of given size.
func (m *intraModeMap) set(x0, y0, puSize, mode int) {
	height4 := len(m.modes) / m.width4
	for y := y0 / 4; y < (y0+puSize)/4; y++ {
		if y < 0 || y >= height4 {
			continue
		}
		for x := x0 / 4; x < (x0+puSize)/4; x++ {
			if x < 0 || x >= m.width4 {
				continue
			}
			m.modes[y*m.width4+x] = mode
		}
	}
}

// get returns the intra mode at pixel position (x, y), or -1 if unavailable.
func (m *intraModeMap) get(x, y int) int {
	bx, by := x/4, y/4
	if bx < 0 || by < 0 || bx >= m.width4 || by*m.width4+bx >= len(m.modes) {
		return -1
	}
	return m.modes[by*m.width4+bx]
}

// deriveMPM builds the 3-element MPM list per HEVC spec 8.4.2 (Table 8-3).
func deriveMPM(candA, candB int) [3]int {
	// -1 means unavailable → default to DC (mode 1)
	if candA < 0 {
		candA = 1
	}
	if candB < 0 {
		candB = 1
	}

	if candA == candB {
		if candA < 2 {
			return [3]int{0, 1, 26} // Planar, DC, Angular26
		}
		return [3]int{
			candA,
			2 + ((candA + 29) % 32),    // candA - 1 in angular range
			2 + ((candA - 2 + 1) % 32), // candA + 1 in angular range
		}
	}

	// candA != candB
	var third int
	if candA != 0 && candB != 0 {
		third = 0 // Planar
	} else if candA != 1 && candB != 1 {
		third = 1 // DC
	} else {
		third = 26 // Angular 26
	}
	return [3]int{candA, candB, third}
}
