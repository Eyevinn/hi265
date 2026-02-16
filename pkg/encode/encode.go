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
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

// EncodeIDRFrame returns Annex-B bytes: VPS + SPS + PPS + IDR slice.
// y, cb, cr are raw pixel planes in raster scan order (4:2:0).
func EncodeIDRFrame(p EncodeParams, y, cb, cr []uint8) ([]byte, error) {
	qp := p.QP
	if qp == 0 {
		qp = 26
	}

	var buf bytes.Buffer

	// Write VPS
	WriteNALU(&buf, naluVPS, generateVPS())

	// Write SPS
	WriteNALU(&buf, naluSPS, generateSPS(p.Width, p.Height, p.ColorSpace, p.Range))

	// Write PPS
	WriteNALU(&buf, naluPPS, generatePPS(qp))

	// Write IDR slice
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(p.Width, p.Height, qp, y, cb, cr))

	return buf.Bytes(), nil
}

// EncodePSkipFrame returns Annex-B bytes: P-slice (all skip CUs).
// Must be called after EncodeIDRFrame to produce a valid stream.
func EncodePSkipFrame(p EncodeParams, poc int) ([]byte, error) {
	qp := p.QP
	if qp == 0 {
		qp = 26
	}

	var buf bytes.Buffer

	// Write P-slice
	WriteNALU(&buf, naluTrailR, encodePSkipSlice(p.Width, p.Height, qp, poc))

	return buf.Bytes(), nil
}

// EncodeIDRSliceFromSPSPPS encodes an IDR I-slice compatible with external SPS/PPS.
// y, cb, cr are raw pixel planes in raster scan order (4:2:0).
// Returns Annex-B framed IDR_W_RADL NALU.
func EncodeIDRSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS, y, cb, cr []uint8) ([]byte, error) {
	if err := validateSPSPPSForIDR(sps, pps); err != nil {
		return nil, err
	}

	p := idrSliceParamsFromSPSPPS(sps, pps)
	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSliceWithParams(p, y, cb, cr))
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
	// IDR encoding only supports 16x16 CUs (log2MinCb=4, log2Diff=0 → CTU=16)
	log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3
	ctuLog2 := log2MinCbSize + int(sps.Log2DiffMaxMinLumaCodingBlockSize)
	ctuSize := 1 << ctuLog2
	if ctuSize != 16 {
		return fmt.Errorf("IDR encoding only supports CTU size 16, got %d", ctuSize)
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
	Width      int            // pixel width (0 = Grid.Width*16)
	Height     int            // pixel height (0 = Grid.Height*16)
	ColorSpace yuv.ColorSpace // default BT601
	Range      yuv.Range      // default LimitedRange
}

// Encode produces a complete Annex-B IDR frame: VPS + SPS + PPS + IDR slice.
func (e *FrameEncoder) Encode() ([]byte, error) {
	f, err := yuv.BuildFrame(e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	w := e.frameWidth()
	h := e.frameHeight()
	f.Width = w
	f.Height = h
	return EncodeIDRFrame(EncodeParams{
		Width:      w,
		Height:     h,
		QP:         e.qp(),
		ColorSpace: e.ColorSpace,
		Range:      e.Range,
	}, f.Y, f.Cb, f.Cr)
}

// EncodeVPSSPSPPS writes the VPS, SPS, and PPS NALUs to buf.
func (e *FrameEncoder) EncodeVPSSPSPPS(buf *bytes.Buffer) {
	WriteNALU(buf, naluVPS, generateVPS())
	WriteNALU(buf, naluSPS, generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range))
	WriteNALU(buf, naluPPS, generatePPS(e.qp()))
}

// EncodeIDRSlice produces an Annex-B IDR slice (no VPS/SPS/PPS).
func (e *FrameEncoder) EncodeIDRSlice() ([]byte, error) {
	f, err := yuv.BuildFrame(e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	w := e.frameWidth()
	h := e.frameHeight()
	f.Width = w
	f.Height = h

	var buf bytes.Buffer
	WriteNALU(&buf, naluIDRWRadl, encodeIDRSlice(w, h, e.qp(), f.Y, f.Cb, f.Cr))
	return buf.Bytes(), nil
}

// EncodePSkipSlice produces an Annex-B P-skip slice.
func (e *FrameEncoder) EncodePSkipSlice(poc int) ([]byte, error) {
	var buf bytes.Buffer
	WriteNALU(&buf, naluTrailR, encodePSkipSlice(e.frameWidth(), e.frameHeight(), e.qp(), poc))
	return buf.Bytes(), nil
}

// EncodeIDRSliceFromSPSPPS produces an Annex-B IDR slice compatible with external SPS/PPS.
func (e *FrameEncoder) EncodeIDRSliceFromSPSPPS(sps *hevc.SPS, pps *hevc.PPS) ([]byte, error) {
	f, err := yuv.BuildFrame(e.Grid, e.Colors)
	if err != nil {
		return nil, err
	}
	w := e.frameWidth()
	h := e.frameHeight()
	f.Width = w
	f.Height = h
	return EncodeIDRSliceFromSPSPPS(sps, pps, f.Y, f.Cb, f.Cr)
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
	return [][]byte{buildNALU(naluSPS, generateSPS(e.frameWidth(), e.frameHeight(), e.ColorSpace, e.Range))}
}

// PPSNALUs returns the raw PPS NALU (with 2-byte header, no start code) for MP4.
func (e *FrameEncoder) PPSNALUs() [][]byte {
	return [][]byte{buildNALU(naluPPS, generatePPS(e.qp()))}
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
