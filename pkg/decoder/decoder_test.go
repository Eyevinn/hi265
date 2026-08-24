package decoder

import (
	"os"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/Eyevinn/hi265/pkg/frame"
)

func testGolden(t *testing.T, bitstreamPath, goldenPath string, width, height int) {
	t.Helper()
	testGoldenFrames(t, bitstreamPath, goldenPath, width, height, 1)
}

func testGoldenFrames(t *testing.T, bitstreamPath, goldenPath string, width, height, numFrames int) {
	t.Helper()

	bitstreamData, err := os.ReadFile(bitstreamPath)
	if err != nil {
		t.Fatalf("read bitstream: %v", err)
	}

	goldenData, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	dec := New()
	frames, err := dec.DecodeAnnexB(bitstreamData)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(frames) != numFrames {
		t.Fatalf("expected %d frames, got %d", numFrames, len(frames))
	}

	// Concatenate all frames' YUV data
	var decoded []byte
	for _, f := range frames {
		if f.Width != width || f.Height != height {
			t.Fatalf("expected %dx%d, got %dx%d", width, height, f.Width, f.Height)
		}
		decoded = append(decoded, f.YUV420Bytes()...)
	}

	if len(decoded) != len(goldenData) {
		t.Fatalf("decoded size %d != golden size %d", len(decoded), len(goldenData))
	}

	frameSize := width*height + 2*(width/2)*(height/2)
	for fi := range numFrames {
		compareFrame(t, decoded[fi*frameSize:(fi+1)*frameSize],
			goldenData[fi*frameSize:(fi+1)*frameSize], width, height, fi)
	}
}

func compareFrame(t *testing.T, decoded, golden []byte, width, height, frameIdx int) {
	t.Helper()
	_ = frame.Frame{} // ensure import is used

	lumaSize := width * height
	chromaSize := (width / 2) * (height / 2)
	mismatches := 0
	for i := range decoded {
		if decoded[i] != golden[i] {
			if mismatches < 10 {
				plane := "Y"
				pos := i
				if i >= lumaSize+chromaSize {
					plane = "Cr"
					pos = i - lumaSize - chromaSize
				} else if i >= lumaSize {
					plane = "Cb"
					pos = i - lumaSize
				}
				t.Errorf("frame %d: mismatch at %s[%d]: got 0x%02x, want 0x%02x",
					frameIdx, plane, pos, decoded[i], golden[i])
			}
			mismatches++
		}
	}
	if mismatches > 0 {
		for i := range decoded {
			if decoded[i] != golden[i] {
				plane := "Y"
				pos := i
				if i >= lumaSize+chromaSize {
					plane = "Cr"
					pos = i - lumaSize - chromaSize
				} else if i >= lumaSize {
					plane = "Cb"
					pos = i - lumaSize
				}
				x := pos % width
				y := pos / width
				if plane != "Y" {
					x = pos % (width / 2)
					y = pos / (width / 2)
				}
				t.Logf("frame %d: mismatch %s[%d] (x=%d,y=%d): got 0x%02x, want 0x%02x, diff=%d",
					frameIdx, plane, pos, x, y, decoded[i], golden[i], int(decoded[i])-int(golden[i]))
			}
		}
		t.Fatalf("frame %d: total mismatches: %d out of %d bytes", frameIdx, mismatches, len(decoded))
	}
}

func TestDecodeBlack16x16(t *testing.T) {
	testGolden(t, "../../testdata/black_16x16.265", "../../testdata/golden/black_16x16.yuv", 16, 16)
}

func TestDecodeGray16x16(t *testing.T) {
	testGolden(t, "../../testdata/gray_16x16.265", "../../testdata/golden/gray_16x16.yuv", 16, 16)
}

func TestDecodeBlack32x32(t *testing.T) {
	testGolden(t, "../../testdata/black_32x32.265", "../../testdata/golden/black_32x32.yuv", 32, 32)
}

func TestDecodeRed16x16(t *testing.T) {
	testGolden(t, "../../testdata/red_16x16.265", "../../testdata/golden/red_16x16.yuv", 16, 16)
}

func TestDecodeColorGrid32x32(t *testing.T) {
	testGolden(t, "../../testdata/colorgrid_32x32.265", "../../testdata/golden/colorgrid_32x32.yuv", 32, 32)
}

func TestDecodeChecker16x16(t *testing.T) {
	testGolden(t, "../../testdata/checker_16x16.265", "../../testdata/golden/checker_16x16.yuv", 16, 16)
}

func TestDecodeGradient64x64(t *testing.T) {
	testGolden(t, "../../testdata/gradient_64x64.265", "../../testdata/golden/gradient_64x64.yuv", 64, 64)
}

func TestDecodeGray128x64(t *testing.T) {
	testGolden(t, "../../testdata/gray_128x64.265", "../../testdata/golden/gray_128x64.yuv", 128, 64)
}

func TestDecodeSinCos128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_128x64.265", "../../testdata/golden/sincos_128x64.yuv", 128, 64)
}

func TestDecodeGrayDeblock128x64(t *testing.T) {
	testGolden(t, "../../testdata/gray_deblock_128x64.265", "../../testdata/golden/gray_deblock_128x64.yuv", 128, 64)
}

func TestDecodeSinCosDeblock128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_deblock_128x64.265",
		"../../testdata/golden/sincos_deblock_128x64.yuv", 128, 64)
}

func TestDecodeSinCosSignHide128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_signhide_128x64.265",
		"../../testdata/golden/sincos_signhide_128x64.yuv", 128, 64)
}

func TestDecodeSinCosTSkip128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_tskip_128x64.265", "../../testdata/golden/sincos_tskip_128x64.yuv", 128, 64)
}

func TestDecodeSinCosSao128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_sao_128x64.265", "../../testdata/golden/sincos_sao_128x64.yuv", 128, 64)
}

func TestDecodeSinCosCRF32_128x64(t *testing.T) {
	testGolden(t, "../../testdata/sincos_crf32_128x64.265", "../../testdata/golden/sincos_crf32_128x64.yuv", 128, 64)
}

func TestDecodeGray2Frames128x64(t *testing.T) {
	testGoldenFrames(t, "../../testdata/gray_2frames_128x64.265",
		"../../testdata/golden/gray_2frames_128x64.yuv", 128, 64, 2)
}

// Tiled pictures, one slice segment per tile — the shape kvazaar's
// "--tiles WxH --slices tiles" produces and the one `hevc-retiler` stitches.
// Both loop filters are off in these vectors so that tile geometry, tile scan
// order and neighbour availability are the only things under test; filtering
// across a tile boundary is a separate matter (docs/tiles-decoding.md, T5).

// Two tile columns over two frames, which also covers closing one picture when
// the next picture's first slice segment arrives.
func TestDecodeTiles2x1TwoFrames128x64(t *testing.T) {
	testGoldenFrames(t, "../../testdata/tiles_2x1_2frames_128x64.265",
		"../../testdata/golden/tiles_2x1_2frames_128x64.yuv", 128, 64, 2)
}

// A 2x2 grid, so the picture has both a vertical and a horizontal tile
// boundary and tile scan differs from raster scan in both directions.
func TestDecodeTiles2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_128x128.265",
		"../../testdata/golden/tiles_2x2_128x128.yuv", 128, 128)
}

// Uniform spacing that is not an equal division: 3 CTBs in 2 tile columns is
// one CTB then two, per the spec 6.5.1 formula. A decoder that divided equally
// would put the tile boundary in the wrong place.
func TestDecodeTilesNonEqual192x64(t *testing.T) {
	testGolden(t, "../../testdata/tiles_nonequal_192x64.265",
		"../../testdata/golden/tiles_nonequal_192x64.yuv", 192, 64)
}

// The same 2x2 grid with one loop filter on at a time. These are the vectors
// that pin the boundary rules of spec 8.7.2 and 8.7.3.2: with
// loop_filter_across_tiles_enabled_flag equal to 0, neither filter may reach
// across a tile edge, so a decoder that filtered through it would differ from
// FFmpeg along the seams and nowhere else.
func TestDecodeTilesDeblock2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_deblock_128x128.265",
		"../../testdata/golden/tiles_2x2_deblock_128x128.yuv", 128, 128)
}

func TestDecodeTilesSao2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_2x2_sao_128x128.265",
		"../../testdata/golden/tiles_2x2_sao_128x128.yuv", 128, 128)
}

// Several tiles in one slice segment, reached through entry point offsets —
// kvazaar's default when tiles are enabled, and what most encoders emit. Each
// tile is its own CABAC substream: the contexts are re-initialised, QP
// prediction restarts and no earlier tile is available for prediction.
func TestDecodeTilesMultiPerSegment2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_2x2_128x128.yuv", 128, 128)
}

// The same shape with both loop filters on. One slice covers every tile here,
// so slice_loop_filter_across_slices_enabled_flag cannot protect a seam — this
// is the vector that pins the tile rule on its own.
func TestDecodeTilesMultiPerSegmentFilters2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_filters_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_filters_2x2_128x128.yuv", 128, 128)
}

// A delta-QP map makes cu_qp_delta live, which is what makes the per-tile reset
// of qPY_PREV (spec 8.6.1) observable: without it the predicted QP walks in from
// the previous tile and every coefficient dequantises against the wrong step.
func TestDecodeTilesMultiPerSegmentQP2x2_128x128(t *testing.T) {
	testGolden(t, "../../testdata/tiles_multi_qp_2x2_128x128.265",
		"../../testdata/golden/tiles_multi_qp_2x2_128x128.yuv", 128, 128)
}

// A slice is one independent slice segment plus any number of dependent ones
// (spec 7.4.7.1). A dependent segment carries almost no header: it continues its
// predecessor's CABAC contexts, QP prediction and neighbour maps, and a boundary
// between two segments of one slice is not a prediction or filtering boundary at
// all. "kvazaar --slices wpp" puts each CTB row in a dependent segment.
func TestDecodeDependentSegmentsWpp128x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_dep_wpp_128x128.265",
		"../../testdata/golden/slices_dep_wpp_128x128.yuv", 128, 128)
}

// The same stream with deblocking on, which pins the filtering half: treating a
// dependent segment boundary as a slice boundary leaves the row seam unfiltered.
func TestDecodeDependentSegmentsDeblock128x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_dep_wpp_deblock_128x128.265",
		"../../testdata/golden/slices_dep_wpp_deblock_128x128.yuv", 128, 128)
}

// Two *independent* slices with wavefront parallel processing, from x265
// --wpp --slices 2. Each slice indexes its entry point offsets from its own
// first CTB row, and the second slice must not inherit the first's wavefront
// snapshot: the row above it belongs to another slice, so spec 6.4.1 makes it
// unavailable and the contexts start from their initial values.
func TestDecodeTwoSlicesWpp256x128(t *testing.T) {
	testGolden(t, "../../testdata/slices_wpp_2slices_256x128.265",
		"../../testdata/golden/slices_wpp_2slices_256x128.yuv", 256, 128)
}

// A stream whose PPS carries non-zero pps_cb_qp_offset and pps_cr_qp_offset, with
// opposite signs so the two components genuinely scale at different QPs and one
// wrong offset cannot pass as the other.
//
// Spec 8.6.1 adds these to the luma QP before Table 8-10 maps it, and spec
// 8.7.2.5.5 folds the picture-level pair into the chroma deblocking edge as well.
// Both were ignored: the luma plane was untouched but every chroma sample was
// wrong, by up to 71 on this vector. Nothing caught it because no fixture had a
// non-zero offset — x265 only writes one when asked.
func TestDecodeChromaQPOffsets192x96(t *testing.T) {
	testGolden(t, "../../testdata/chromaqpoffs_192x96.265",
		"../../testdata/golden/chromaqpoffs_192x96.yuv", 192, 96)
}

// A stream with scaling_list_enabled_flag set and no explicit scaling_list_data,
// so the default non-flat matrices of Table 7-6 apply. Coded at QP 12 on purpose:
// the weights only bite where a transform block larger than 4x4 carries
// coefficients, and the 4x4 default matrix is flat, so a coarse quantizer hides
// the whole feature. Ignoring the lists put 2870 of 49152 samples wrong here, at
// a max delta of 2 — small enough to read as rounding, which is why it survived.
func TestDecodeScalingListQ12(t *testing.T) {
	testGolden(t, "../../testdata/scalinglist_q12_256x128.265",
		"../../testdata/golden/scalinglist_q12_256x128.yuv", 256, 128)
}

// A per-CU chroma QP offset is a different feature, and one that puts syntax in
// the transform unit this decoder does not read. It must be refused rather than
// decoded past.
func TestDecodeChromaQPOffsetListIsRefused(t *testing.T) {
	data, err := os.ReadFile("../../testdata/chromaqpoffs_192x96.265")
	if err != nil {
		t.Fatal(err)
	}
	// The committed vector does not enable it — no encoder to hand does — so the
	// refusal is checked by turning the flag on in the parsed PPS. Feeding the
	// decoder the stream then exercises the same guard a real such stream would.
	dec := New()
	var patched bool
	for _, nalu := range avc.ExtractNalusFromByteStream(data) {
		if len(nalu) < 2 {
			continue
		}
		if hevc.GetNaluType(nalu[0]) < 32 { // a coded slice segment
			if !patched {
				t.Fatal("no PPS in the fixture to turn the flag on in")
			}
			_, err := dec.DecodeNALUs([][]byte{nalu})
			if err == nil {
				t.Fatal("expected chroma_qp_offset_list_enabled_flag to be refused")
			}
			if !strings.Contains(err.Error(), "chroma_qp_offset_list_enabled_flag") {
				t.Errorf("the error should name the flag, got: %v", err)
			}
			return
		}
		// A parameter set on its own decodes no frame, which DecodeNALUs reports as
		// an error; what matters is that it registered the set.
		_, _ = dec.DecodeNALUs([][]byte{nalu})
		if hevc.GetNaluType(nalu[0]) == hevc.NALU_PPS {
			for _, pps := range dec.ppsMap {
				if pps.RangeExtension == nil {
					pps.RangeExtension = &hevc.RangeExtension{}
				}
				pps.RangeExtension.ChromaQpOffsetListEnabledFlag = true
				patched = true
			}
		}
	}
	t.Fatal("the IDR slice was never reached")
}

// A P picture with real motion vectors must be refused rather than
// approximated. Only zero-motion skip CUs are reconstructed, so an inter CU that
// carries a motion vector, a merge candidate or a reference index has no
// prediction to build from — and returning the picture anyway would hand the
// caller something that looks like a decode and is not one.
func TestDecodeInterMotionIsRefused(t *testing.T) {
	data, err := os.ReadFile("../../testdata/pframe_motion_128x64.265")
	if err != nil {
		t.Fatal(err)
	}
	frames, err := New().DecodeAnnexB(data)
	if err == nil {
		t.Fatalf("expected the inter picture to be refused, got %d frames", len(frames))
	}
	if !strings.Contains(err.Error(), "motion compensation is not implemented") {
		t.Errorf("the error should name the limitation, got: %v", err)
	}
}

// The coding tools this decoder does not implement have to be refused rather than
// decoded past. Each one below either desynchronises the arithmetic decoder at the
// first coding unit that uses it, or makes the samples mean something other than
// what is reconstructed — and in both cases a picture would still be returned.
//
// A subtly wrong thumbnail is worse than an error, so this pins that each is
// named rather than ignored. The flags are set on the parsed parameter sets,
// since no encoder to hand emits most of them.
func TestUnsupportedToolsAreRefused(t *testing.T) {
	data, err := os.ReadFile("../../testdata/sincos_128x64.265")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		want   string
		break_ func(sps *hevc.SPS, pps *hevc.PPS)
	}{
		{"bit_depth_luma", "bit depth 10", func(s *hevc.SPS, _ *hevc.PPS) { s.BitDepthLumaMinus8 = 2 }},
		{"bit_depth_chroma", "chroma bit depth 10", func(s *hevc.SPS, _ *hevc.PPS) { s.BitDepthChromaMinus8 = 2 }},
		{"chroma_format", "chroma_format_idc 2", func(s *hevc.SPS, _ *hevc.PPS) { s.ChromaFormatIDC = 2 }},
		{"separate_colour_plane", "separate_colour_plane_flag", func(s *hevc.SPS, _ *hevc.PPS) {
			s.ChromaFormatIDC = 1
			s.SeparateColourPlaneFlag = true
		}},
		{"pcm", "pcm_enabled_flag", func(s *hevc.SPS, _ *hevc.PPS) { s.PCMEnabledFlag = true }},
		{"transquant_bypass", "transquant_bypass_enabled_flag", func(_ *hevc.SPS, p *hevc.PPS) {
			p.TransquantBypassEnabledFlag = true
		}},
		{"explicit_scaling_lists", "scaling_list_data", func(s *hevc.SPS, _ *hevc.PPS) {
			s.ScalingListEnabledFlag = true
			s.ScalingListDataPresentFlag = true
		}},
		{"cross_component_prediction", "cross_component_prediction", func(_ *hevc.SPS, p *hevc.PPS) {
			p.RangeExtension = &hevc.RangeExtension{CrossComponentPredictionEnabledFlag: true}
		}},
		{"persistent_rice", "persistent_rice_adaptation", func(s *hevc.SPS, _ *hevc.PPS) {
			s.RangeExtension = &hevc.SPSRangeExtension{PersistentRiceAdaptationEnabledFlag: true}
		}},
		{"cabac_bypass_alignment", "cabac_bypass_alignment", func(s *hevc.SPS, _ *hevc.PPS) {
			s.RangeExtension = &hevc.SPSRangeExtension{CabacBypassAlignmentEnabledFlag: true}
		}},
		{"implicit_rdpcm", "implicit_rdpcm", func(s *hevc.SPS, _ *hevc.PPS) {
			s.RangeExtension = &hevc.SPSRangeExtension{ImplicitRdpcmEnabledFlag: true}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec := New()
			for _, nalu := range avc.ExtractNalusFromByteStream(data) {
				if len(nalu) < 2 {
					continue
				}
				if hevc.GetNaluType(nalu[0]) < 32 { // a coded slice segment
					_, err := dec.DecodeNALUs([][]byte{nalu})
					if err == nil {
						t.Fatalf("expected %s to be refused", tc.name)
					}
					if !strings.Contains(err.Error(), tc.want) {
						t.Errorf("the error should mention %q, got: %v", tc.want, err)
					}
					return
				}
				// A parameter set alone decodes no frame, which is reported as an
				// error; what matters is that it registered.
				_, _ = dec.DecodeNALUs([][]byte{nalu})
				if hevc.GetNaluType(nalu[0]) == hevc.NALU_PPS {
					for _, pps := range dec.ppsMap {
						for _, sps := range dec.spsMap {
							tc.break_(sps, pps)
						}
					}
				}
			}
			t.Fatal("the slice segment was never reached")
		})
	}
}

// And the streams that use none of them still decode, so the guard above cannot
// be a blanket refusal.
func TestSupportedStreamsStillDecode(t *testing.T) {
	for _, path := range []string{
		"../../testdata/sincos_128x64.265",
		"../../testdata/scalinglist_q12_256x128.265",
		"../../testdata/chromaqpoffs_192x96.265",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := New().DecodeAnnexB(data); err != nil {
			t.Errorf("%s should decode: %v", path, err)
		}
	}
}
