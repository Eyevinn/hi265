package encode

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/Eyevinn/mp4ff/hevc"
)

// EncodeParams holds parameters for HEVC encoding.
type EncodeParams struct {
	Width      int            // must be multiple of 16
	Height     int            // must be multiple of 16
	QP         int            // 0-51, default 26
	Use8x8CU   bool           // code each 16x16 CTU as four 8x8 CUs
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

func (p EncodeParams) qp() int {
	if p.QP == 0 {
		return 26
	}
	return p.QP
}

// GenerateVPSSPSPPS returns Annex-B bytes containing VPS + SPS + PPS NALUs.
func GenerateVPSSPSPPS(p EncodeParams) ([]byte, error) {
	var buf bytes.Buffer
	WriteNALU(&buf, naluVPS, generateVPS())
	WriteNALU(&buf, naluSPS, generateSPS(p.Width, p.Height, p.ColorSpace, p.Range, p.Use8x8CU))
	WriteNALU(&buf, naluPPS, generatePPS(p.qp()))
	return buf.Bytes(), nil
}

// GenerateIDR returns Annex-B bytes containing an IDR slice NALU.
// The grid and colors define the per-CTU content (each grid cell is one 16x16 CTU).
func GenerateIDR(p EncodeParams, grid *yuv.Grid, colors yuv.ColorMap) ([]byte, error) {
	f, err := yuv.BuildFrame(grid, colors)
	if err != nil {
		return nil, err
	}
	f.Width = p.Width
	f.Height = p.Height

	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(p.Width, p.Height, p.qp(), p.Use8x8CU, f.Y, f.Cb, f.Cr))
	return buf.Bytes(), nil
}

// GeneratePSkip returns Annex-B bytes containing a P-skip slice NALU.
// All CUs copy from the reference frame with zero motion.
func GeneratePSkip(p EncodeParams, poc int) ([]byte, error) {
	var buf bytes.Buffer
	WriteNALU(&buf, naluTrailR, encodePSkipSlice(p.Width, p.Height, p.qp(), poc, p.Use8x8CU))
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

	sp := idrSliceParamsFromSPSPPS(sps, pps)
	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSliceWithParams(sp, f.Y, f.Cb, f.Cr))
	return buf.Bytes(), nil
}

// EncodePSkipSliceFromSPSPPS encodes a P-skip slice compatible with external SPS/PPS.
// Returns Annex-B framed TRAIL_R NALU.
func EncodePSkipSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, poc int) ([]byte, error) {
	if err := validateSPSPPS(sps, pps); err != nil {
		return nil, err
	}

	p := pSkipSliceParamsFromSPSPPS(sps, pps, poc)
	var buf bytes.Buffer
	WriteNALU(&buf, naluTrailR, encodePSkipSliceWithParams(p))
	return buf.Bytes(), nil
}

func validateSPSPPS(sps *hevc.SPS, pps *hevc.PPS) error {
	if pps.TilesEnabledFlag {
		return fmt.Errorf("tiles not supported")
	}
	if pps.DependentSliceSegmentsEnabledFlag {
		return fmt.Errorf("dependent slice segments not supported")
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
	WriteNALU(buf, naluPPS, generatePPS(e.qp()))
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
	return [][]byte{buildNALU(naluPPS, generatePPS(e.qp()))}
}

func (e *FrameEncoder) encodeParams() EncodeParams {
	return EncodeParams{
		Width:      e.frameWidth(),
		Height:     e.frameHeight(),
		QP:         e.qp(),
		Use8x8CU:   e.Use8x8CU,
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
