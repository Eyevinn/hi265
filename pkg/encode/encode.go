package encode

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"
)

// EncodeParams holds parameters for HEVC encoding.
type EncodeParams struct {
	Width    int  // must be a multiple of 8
	Height   int  // must be a multiple of 8
	QP       int  // 0-51, default 26
	Use8x8CU bool // code each 16x16 CTU as four 8x8 CUs
	// TileCols and TileRows cut the picture into a uniform tile grid, each tile
	// coded as its own independent slice segment with no filtering across the
	// boundaries. Zero or one of each means no tiles.
	TileCols int
	TileRows int
	// WPP sets entropy_coding_sync_enabled_flag: the picture stays one slice
	// segment, but every CTB row becomes its own CABAC substream, reached through
	// an entry point offset, so a decoder can run the rows as a wavefront. It
	// cannot be combined with tiles — no HEVC profile permits that.
	WPP        bool
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

// tileCols and tileRows normalise the tile grid: unset means one.
func (p EncodeParams) tileCols() int {
	if p.TileCols < 1 {
		return 1
	}
	return p.TileCols
}

func (p EncodeParams) tileRows() int {
	if p.TileRows < 1 {
		return 1
	}
	return p.TileRows
}

// segments returns the slice segments this picture is emitted as: one per tile,
// or a single one cut into a substream per CTB row when WPP is set.
func (p EncodeParams) segments() ([]segment, error) {
	lay := chooseCodingLayout(p.Width, p.Height, p.Use8x8CU)
	return segmentsForGrid(p.Width, p.Height, lay.ctuSize, p.tileCols(), p.tileRows(), p.WPP)
}

// validateParallelism rejects the one combination of the two parallelism tools
// that no HEVC profile permits. It is checked where the parameter sets are
// written as well as where the slice data is, so a PPS can never end up
// announcing something the slices do not carry.
func (p EncodeParams) validateParallelism() error {
	if p.WPP && (p.tileCols() > 1 || p.tileRows() > 1) {
		return fmt.Errorf("tiles combined with wavefront parallel processing is not supported")
	}
	return nil
}

func (p EncodeParams) qp() int {
	if p.QP == 0 {
		return 26
	}
	return p.QP
}

// GenerateVPSSPSPPS returns Annex-B bytes containing VPS + SPS + PPS NALUs.
func GenerateVPSSPSPPS(p EncodeParams) ([]byte, error) {
	if err := validateFrameDimensions(p.Width, p.Height); err != nil {
		return nil, err
	}
	if err := p.validateParallelism(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	WriteNALU(&buf, naluVPS, generateVPS())
	WriteNALU(&buf, naluSPS, generateSPS(p.Width, p.Height, p.ColorSpace, p.Range, p.Use8x8CU))
	WriteNALU(&buf, naluPPS, generatePPS(p.qp(), p.tileCols(), p.tileRows(), p.WPP, -1))
	return buf.Bytes(), nil
}

// GenerateIDR returns Annex-B bytes containing an IDR slice NALU.
// The grid and colors define the per-CTU content (each grid cell is one 16x16 CTU).
func GenerateIDR(p EncodeParams, grid *yuv.Grid, colors yuv.ColorMap) ([]byte, error) {
	if err := validateFrameDimensions(p.Width, p.Height); err != nil {
		return nil, err
	}
	f, err := yuv.BuildFrame(grid, colors)
	if err != nil {
		return nil, err
	}
	f.Width = p.Width
	f.Height = p.Height

	segs, err := p.segments()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, sg := range segs {
		WriteNALU(&buf, naluIDRWRadl,
			encodeIDRSlice(sg, p.Width, p.Height, p.qp(), p.Use8x8CU, f.Y, f.Cb, f.Cr))
	}
	return buf.Bytes(), nil
}

// GeneratePSkip returns Annex-B bytes containing a P-skip slice NALU.
// All CUs copy from the reference frame with zero motion.
func GeneratePSkip(p EncodeParams, poc int) ([]byte, error) {
	if err := validateFrameDimensions(p.Width, p.Height); err != nil {
		return nil, err
	}
	segs, err := p.segments()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, sg := range segs {
		WriteNALU(&buf, naluTrailR, encodePSkipSlice(sg, p.Width, p.Height, p.qp(), poc, p.Use8x8CU))
	}
	return buf.Bytes(), nil
}

// EncodeIDRSliceFromSPSPPS encodes an IDR I-slice compatible with external SPS/PPS.
// The grid and colors define the per-CTU content (each grid cell is one 16x16 CTU).
// Returns Annex-B framed IDR_W_RADL NALU.
func EncodeIDRSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, grid *yuv.Grid, colors yuv.ColorMap) ([]byte, error) {
	if err := validateSPSPPSForIDR(sps, pps); err != nil {
		return nil, err
	}

	w := int(sps.PicWidthInLumaSamples)
	h := int(sps.PicHeightInLumaSamples)
	f, err := yuv.BuildFrame(grid, colors)
	if err != nil {
		return nil, err
	}
	f.Width = w
	f.Height = h

	segs, err := tileSegments(sps, pps)
	if err != nil {
		return nil, err
	}
	sp := idrSliceParamsFromSPSPPS(sps, pps)

	// A tiled picture is one independent slice segment per tile. Each is coded
	// as if the rest of the picture were not there, which is what makes the tile
	// boundaries real: nothing outside a tile is available to predict from.
	var buf bytes.Buffer
	for _, sg := range segs {
		sp.seg = sg
		WriteNALU(&buf, naluIDRWRadl, encodeIDRSliceWithParams(sp, f.Y, f.Cb, f.Cr))
	}
	return buf.Bytes(), nil
}

// EncodeCRASliceFromSPSPPS encodes a CRA (Clean Random Access) I-slice compatible
// with external SPS/PPS at the given picture order count.
// The grid and colors define the per-CTU content (each grid cell is one 16x16 CTU).
//
// Unlike an IDR, a CRA does not reset the picture order count: its POC MSBs are
// derived from the preceding pictures, so a CRA carrying the right
// slice_pic_order_cnt_lsb can be spliced into a running stream as a refresh point
// without breaking POC continuity of what follows (Gradual Decoder Refresh).
//
// Returns Annex-B framed CRA_NUT NALU.
func EncodeCRASliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, grid *yuv.Grid, colors yuv.ColorMap,
	poc int) ([]byte, error) {

	if err := validateSPSPPSForIDR(sps, pps); err != nil {
		return nil, err
	}

	w := int(sps.PicWidthInLumaSamples)
	h := int(sps.PicHeightInLumaSamples)
	f, err := yuv.BuildFrame(grid, colors)
	if err != nil {
		return nil, err
	}
	f.Width = w
	f.Height = h

	segs, err := tileSegments(sps, pps)
	if err != nil {
		return nil, err
	}
	sp := idrSliceParamsFromSPSPPS(sps, pps)
	rps := craRefPicSetParams(sps, poc)
	sp.refPicSet = &rps

	var buf bytes.Buffer
	for _, sg := range segs {
		sp.seg = sg
		WriteNALU(&buf, naluCRA, encodeIDRSliceWithParams(sp, f.Y, f.Cb, f.Cr))
	}
	return buf.Bytes(), nil
}

// EncodePSkipSliceFromSPSPPS encodes a P-skip slice compatible with external SPS/PPS.
// Returns Annex-B framed TRAIL_R NALU.
func EncodePSkipSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) ([]byte, error) {
	if err := validateSPSPPS(sps, pps); err != nil {
		return nil, err
	}

	segs, err := tileSegments(sps, pps)
	if err != nil {
		return nil, err
	}
	p := pSkipSliceParamsFromSPSPPS(sps, pps, poc)

	var buf bytes.Buffer
	for _, sg := range segs {
		p.seg = sg
		WriteNALU(&buf, naluTrailR, encodePSkipSliceWithParams(p))
	}
	return buf.Bytes(), nil
}

func validateSPSPPS(_ *hevc.SPS, pps *hevc.PPS) error {
	// Tiles are supported, emitted as one independent slice segment per tile, and
	// so is wavefront parallel processing, as one substream per CTB row of a
	// single segment. The two together are not: no HEVC profile permits the
	// combination, and the decoder refuses it as well.
	if pps.EntropyCodingSyncEnabledFlag && pps.TilesEnabledFlag {
		return fmt.Errorf("tiles combined with wavefront parallel processing is not supported")
	}
	if pps.WeightedPredFlag {
		return fmt.Errorf("weighted prediction not supported")
	}
	return nil
}

func validateSPSPPSForIDR(sps *hevc.SPS, pps *hevc.PPS) error {
	if err := validateSPSPPS(sps, pps); err != nil {
		return err
	}
	// IDR encoding only supports CTU size 16, with 16x16 CUs (minCb=16) or
	// four 8x8 CUs per CTU (minCb=8)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
	ctuLog2 := log2MinCbSize + int(sps.Log2DiffMaxMinLumaCodingBlockSize)
	ctuSize := 1 << ctuLog2
	if ctuSize != 16 {
		return fmt.Errorf("IDR encoding only supports CTU size 16, got %d", ctuSize)
	}
	minCbSize := 1 << log2MinCbSize
	if minCbSize != 8 && minCbSize != 16 {
		return fmt.Errorf("IDR encoding only supports min CB size 8 or 16, got %d", minCbSize)
	}
	return nil
}

func pSkipSliceParamsFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) pSkipSliceParams {
	qp := 26 + int(pps.InitQpMinus26) // slice_qp_delta = 0
	return pSkipSliceParams{
		width:                             int(sps.PicWidthInLumaSamples),
		height:                            int(sps.PicHeightInLumaSamples),
		qp:                                qp,
		poc:                               poc,
		ppsID:                             pps.PicParameterSetID,
		numExtraSliceHeaderBits:           pps.NumExtraSliceHeaderBits,
		outputFlagPresent:                 pps.OutputFlagPresentFlag,
		log2MaxPicOrderCntLsb:             int(sps.Log2MaxPicOrderCntLsbMinus4) + 4,
		numShortTermRefPicSets:            int(sps.NumShortTermRefPicSets),
		numLongTermRefPics:                int(sps.NumLongTermRefPics),
		longTermRefPicsPresent:            sps.LongTermRefPicsPresentFlag,
		spsTemporalMvpEnabled:             sps.SpsTemporalMvpEnabledFlag,
		saoEnabled:                        sps.SampleAdaptiveOffsetEnabledFlag,
		cabacInitPresent:                  pps.CabacInitPresentFlag,
		sliceChromaQpOffsetsPresent:       pps.SliceChromaQpOffsetsPresentFlag,
		deblockingFilterControlPresent:    pps.DeblockingFilterControlPresentFlag,
		deblockingFilterOverrideEnabled:   pps.DeblockingFilterOverrideEnabledFlag,
		deblockingFilterDisabled:          pps.DeblockingFilterDisabledFlag,
		loopFilterAcrossSlicesEnabled:     pps.LoopFilterAcrossSlicesEnabledFlag,
		log2MinCodingBlockSizeMinus3:      int(sps.Log2MinLumaCodingBlockSizeMinus3),
		log2DiffMaxMinLumaCodingBlockSize: int(sps.Log2DiffMaxMinLumaCodingBlockSize),
	}
}

// FrameEncoder provides grid-based HEVC encoding using hi264's yuv package.
type FrameEncoder struct {
	Grid       *yuv.Grid
	Colors     yuv.ColorMap
	QP         int
	Use8x8CU   bool           // code each 16x16 CTU as four 8x8 CUs
	TileCols   int            // uniform tile columns (0 or 1 = no tiles)
	TileRows   int            // uniform tile rows (0 or 1 = no tiles)
	WPP        bool           // one CABAC substream per CTB row (excludes tiles)
	Width      int            // pixel width (0 = Grid.Width*16)
	Height     int            // pixel height (0 = Grid.Height*16)
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

// Encode produces a complete Annex-B IDR frame: VPS + SPS + PPS + IDR slice.
func (e *FrameEncoder) Encode() ([]byte, error) {
	p := e.encodeParams()
	vpsSPSPPS, err := GenerateVPSSPSPPS(p)
	if err != nil {
		return nil, err
	}
	idr, err := GenerateIDR(p, e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	return append(vpsSPSPPS, idr...), nil
}

// EncodeVPSSPSPPS writes the VPS, SPS, and PPS NALUs to buf.
func (e *FrameEncoder) EncodeVPSSPSPPS(buf *bytes.Buffer) {
	WriteNALU(buf, naluVPS, generateVPS())
	WriteNALU(buf, naluSPS, generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range, e.Use8x8CU))
	p := e.encodeParams()
	WriteNALU(buf, naluPPS, generatePPS(e.qp(), p.tileCols(), p.tileRows(), p.WPP, -1))
}

// EncodeIDRSlice produces an Annex-B IDR slice (no VPS/SPS/PPS).
func (e *FrameEncoder) EncodeIDRSlice() ([]byte, error) {
	return GenerateIDR(e.encodeParams(), e.Grid, e.Colors)
}

// EncodePSkipSlice produces an Annex-B P-skip slice.
func (e *FrameEncoder) EncodePSkipSlice(poc int) ([]byte, error) {
	return GeneratePSkip(e.encodeParams(), poc)
}

// EncodeIDRSliceFromSPSPPS produces an Annex-B IDR slice compatible with external SPS/PPS.
func (e *FrameEncoder) EncodeIDRSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS) ([]byte, error) {
	return EncodeIDRSliceFromSPSPPS(sps, pps, e.Grid, e.Colors)
}

// EncodeCRASliceFromSPSPPS produces an Annex-B CRA slice compatible with external
// SPS/PPS at the given picture order count.
func (e *FrameEncoder) EncodeCRASliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) ([]byte, error) {
	return EncodeCRASliceFromSPSPPS(sps, pps, e.Grid, e.Colors, poc)
}

// EncodePSkipSliceFromSPSPPS produces an Annex-B P-skip slice compatible with external SPS/PPS.
func (e *FrameEncoder) EncodePSkipSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) ([]byte, error) {
	return EncodePSkipSliceFromSPSPPS(sps, pps, poc)
}

// VPSNALUs returns the raw VPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) VPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluVPS, generateVPS())}
}

// SPSNALUs returns the raw SPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) SPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluSPS,
		generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range, e.Use8x8CU))}
}

// PPSNALUs returns the raw PPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) PPSNALUs() [][]byte {
	p := e.encodeParams()
	return [][]byte{buildNALU(naluPPS, generatePPS(e.qp(), p.tileCols(), p.tileRows(), p.WPP, -1))}
}

func (e *FrameEncoder) encodeParams() EncodeParams {
	return EncodeParams{
		Width:      e.frameWidth(),
		Height:     e.frameHeight(),
		QP:         e.qp(),
		Use8x8CU:   e.Use8x8CU,
		TileCols:   e.TileCols,
		TileRows:   e.TileRows,
		WPP:        e.WPP,
		ColorSpace: e.ColorSpace,
		Range:      e.Range,
	}
}

func (e *FrameEncoder) frameWidth() int {
	if e.Width > 0 {
		return e.Width
	}
	if e.Grid != nil {
		return e.Grid.Width * 16
	}
	return 16
}

func (e *FrameEncoder) frameHeight() int {
	if e.Height > 0 {
		return e.Height
	}
	if e.Grid != nil {
		return e.Grid.Height * 16
	}
	return 16
}

func (e *FrameEncoder) qp() int {
	if e.QP == 0 {
		return 26
	}
	return e.QP
}
