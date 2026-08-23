# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Chroma QP offsets were ignored throughout, in the **decoder** as well as the
  encoder. Spec 8.6.1 adds `pps_cb_qp_offset`/`pps_cr_qp_offset` and the slice-level
  pair to the luma QP before Table 8-10 maps it to a chroma QP, and spec 8.7.2.5.5
  folds the picture-level pair into the chroma deblocking edge; every chroma QP came
  from the luma QP alone, and the slice-level offsets were parsed and discarded.
  Both sides agreed with each other, so only a stream carrying a non-zero offset
  showed it: on an x265 vector with `--cbqpoffs 6 --crqpoffs -6` the luma plane was
  perfect and every chroma sample was wrong, by up to 71. `transform.ChromaQP` is
  now the single derivation, including the clip that the ±24 an offset pair can
  reach makes necessary. Chroma deblocking derives `tC` per component, since the two
  offsets need not be equal. `chroma_qp_offset_list_enabled_flag`, the per-CU
  variant, needs transform-unit syntax neither side handles and is refused rather
  than decoded past.

- A picture width that is not a multiple of the 16x16 CTU coded the wrong
  samples. A grid cell is one CTU, so `yuv.BuildFrame` lays a grid out wider than
  such a picture; the grid entry points handed those planes to the slice writers,
  which index them at the picture width, so every row was read one CTU remainder
  further along than the last and the encoder coded a sheared picture. The source
  samples are now repacked at the picture's own stride — the same crop
  `YUV420Bytes` applies — so a `.265` and a `.yuv` of one pattern agree.
  `hi265gen -smpte -w 120 -h 80` differed from its own raw output on 14050 of
  14400 samples at max delta 177; it now differs on 480 at max delta 1. Only the
  width was affected, so 1920x1080 and 640x360 were never wrong, and widths that
  are multiples of 16 are byte-identical to before. A grid too small for the
  picture is now an error instead of a read past the buffer.

### Added

#### Sign data hiding (encoder)
- `pkg/encode` honours `sign_data_hiding_enabled_flag`, which x265 sets by
  default. Where a sub-block's significant coefficients span more than three scan
  positions, the sign of the lowest-frequency one is no longer coded and the parity
  of the sub-block's absolute levels carries it instead (spec 7.3.8.11), one level
  moving by a step where the parity does not already agree. The flag used to be
  ignored, which desynchronised CABAC from the first such sub-block: the same
  streams came out at max delta 217 against their source, or were refused outright
  by `pkg/decoder`. Together with cu_qp_delta this leaves only the PPS chroma QP
  offsets unhandled from a default-settings x265 PPS.

#### cu_qp_delta (encoder)
- `pkg/encode` writes `cu_qp_delta_abs` where a PPS with
  `cu_qp_delta_enabled_flag` set requires it: at the first transform unit of each
  quantization group that codes a coefficient (spec 7.3.8.10), with the group
  boundary taken from `diff_cu_qp_delta_depth` (7.3.8.4). Such parameter sets —
  what x265 writes for any rate-controlled encode — were accepted before and
  produced a stream that desynchronised CABAC where the element was missing. The
  value is always zero, which is the correct delta for an encoder that codes every
  CU at the slice QP. The gray and P-skip writers need none, since neither reaches
  a transform unit that codes coefficients.

#### Wavefront parallel processing (encoder)
- `pkg/encode` emits WPP: with `entropy_coding_sync_enabled_flag` set — x265's
  default — the slice data is one CABAC substream per CTB row, each closed by an
  `end_of_subset_one_bit` on a byte boundary, with the entry point offsets in the
  slice header to reach them. Each row's contexts come from the snapshot taken
  after the second CTB of the row above (spec 9.3.1), or from initial values
  where the picture is one CTB wide. Covers the gray IDR, gray CRA, grid IDR/CRA
  and P-skip writers, so `hi265gray` and `hi265-mp4-extend` now serve a
  default-settings x265 stream instead of refusing it.
- `EncodeParams.WPP` and `hi265gen -wpp` generate wavefront streams from this
  package's own parameter sets. Combining it with tiles is refused: no HEVC
  profile permits that.

#### Tiles
- Tiled pictures with one slice segment per tile, the shape `hevc-retiler` emits
  and kvazaar's `--tiles WxH --slices tiles` produces: the spec 6.5.1 tile scan
  tables (`internal/tiles`), `slice_segment_address` and
  `dependent_slice_segment_flag` parsing, CTB iteration in tile scan bounded by
  the slice segment, and the SAO merge flags gated on the neighbour being in the
  same tile and segment. Several tiles in one slice segment, dependent
  slice segments and WPP across several segments are refused with a clear error.
- Several tiles in one slice segment, reached through entry point offsets — what
  `kvazaar --tiles` emits by default, and most encoders with it. Each tile is its
  own CABAC substream, and three things restart at its first CTB: the contexts
  (spec 9.3.1), `qPY_PREV` (8.6.1), and neighbour availability, since nothing in
  an earlier tile may be predicted from (6.4.1). Tiles combined with wavefront
  parallel processing is refused: no HEVC profile allows it.
- Loop filters that stop at tile and slice boundaries: with
  `loop_filter_across_tiles_enabled_flag` equal to 0 neither deblocking nor SAO
  reaches across a tile edge, and the same holds at a slice edge whose
  `slice_loop_filter_across_slices_enabled_flag` is 0 — a flag that was read and
  discarded before, and whose presence was keyed on the SPS SAO flag instead of
  the two slice SAO flags. Deblocking also takes its beta and tC offsets and its
  disabled flag per slice rather than from the picture's first slice. Tiled
  streams, loop filters included, now decode bit-exactly against FFmpeg.
- A picture is now assembled from all its slice segments and filtered once, as
  the spec requires. Multi-slice pictures previously decoded as one bogus frame
  per slice, tiles or not.
- Neighbour availability follows spec 6.4.1: a sample in another slice segment
  or another tile is unavailable, replacing a comparison of raster CTB addresses
  that granted availability across both.

#### Encoding tiles
- Every slice encoder emits tiles as one independent slice segment per tile:
  `EncodeGrayIDRSliceFromSPSPPS`, `EncodeGrayCRASliceFromSPSPPS`,
  `EncodePSkipSliceFromSPSPPS`, `EncodeIDRSliceFromSPSPPS` and
  `EncodeCRASliceFromSPSPPS` accept a tiled external PPS, so a gray CRA refresh
  can be spliced into a tiled stream and `hi265-mp4-extend` can freeze one. The
  return shape is unchanged: concatenated NALUs are still one Annex-B stream.
- `EncodeParams.TileCols` / `TileRows` and `hi265gen -tiles CxR` generate tiled
  pictures from this package's own parameter sets, with
  `loop_filter_across_tiles_enabled_flag = 0`. Generated 2x2, 4x1 and 1x8 grids
  decode bit-exactly in FFmpeg and in `pkg/decoder`.
- A parameter set with wavefront parallel processing enabled is now refused by
  the encoders rather than silently mis-encoded; see below.

#### Refusing what cannot be decoded
- A P or B picture with real motion is refused, naming the CU that stopped it,
  instead of decoding to a plausible-looking wrong picture: `pred_mode_flag` is
  parsed, and an inter CU that is not a zero-motion skip errors. Only zero-motion
  skip CUs are reconstructed, and previously such a stream produced no error at
  all.
- The encoder kept its own copy of the intra reference sample construction, still
  comparing raster CTB addresses to decide availability — the rule the decoder
  dropped when tiles landed. A tiled encode therefore predicted the first CU of
  each tile from the neighbouring tile, which in a fresh encoder frame reads as
  zero: the decoder substituted 128 as it should and reconstructed 255 where the
  source was 235, with both decoders agreeing. Encoder and decoder now share
  `internal/pred.BuildRefSamples`.
- `EncodeGrayIDRSliceFromSPSPPS` and the other encoders silently ignored
  `entropy_coding_sync_enabled_flag`, emitting one continuous CABAC substream for
  a parameter set that promises one per CTB row. FFmpeg accepted such a stream
  and decoded it to garbage — 73 % of a 1280x720 case came out zeros instead of
  mid-grey — and only byte lengths were asserted. Those parameter sets are
  refused until the encoder can emit WPP.
- `cu_skip_flag`'s context counts how many of the left and above neighbours were
  themselves skipped (spec 9.3.4.2.2) rather than whether those positions are
  inside the picture — the old form agrees with the spec only for all-skip content
  in a single slice without tiles.
- `pred_weight_table` is parsed rather than skipped. x265 enables weighted
  prediction by default, so a default-settings P slice carries the table, and
  skipping it left the reader mid-header with the entry point offsets that follow
  reading as garbage. A slice that signals an actual weight is refused; one with
  default weights decodes on. `ref_pic_lists_modification` is likewise refused
  when `NumPocTotalCurr` says it is present.

#### Slice segments
- Dependent slice segments: a slice is one independent segment plus any number of
  dependent ones, and the dependent kind carries only its address. The CABAC
  contexts are stored at the end of a segment and resumed by the next
  (spec 9.3.2.4), and QP prediction, the neighbour maps and sample availability
  are now per *slice* rather than per segment — spec 6.4.1 makes a neighbour
  available when it is in the same slice, so a boundary inside one is neither a
  prediction nor a filtering boundary. The SAO merge flags follow suit, gated on
  `SliceAddrRs` rather than the segment's own address.
- Wavefront parallel processing across several slice segments: entry point
  offsets are indexed from the segment's own first CTB row, the row snapshot
  survives a dependent segment boundary, and the first row of an independent
  slice starts from initial contexts rather than inheriting the previous slice's
  snapshot. This is what `x265 --wpp --slices N` produces.
- Entry point offsets describing data outside the segment that carries them are
  ignored rather than rejected: kvazaar writes one per CTB row of the whole
  picture into the first segment even when later rows live in their own dependent
  segments.

#### Decoding real encoder output
- Wavefront parallel processing (`entropy_coding_sync_enabled_flag`), which x265
  enables by default: `num_entry_point_offsets` and the substream offsets are
  parsed, each CTU row is decoded from its own entry point, and the CABAC context
  state is inherited from the snapshot taken after the second CTU of the row above
  (spec 9.3.1). Entry point offsets count emulation prevention bytes, so they are
  translated into the stripped slice data.
- The SPS conformance window is applied to decoder output, while the full coded
  picture is kept as the prediction reference. x265 codes a 360-line source as 368
  lines and signals the padding.
- Frame dimensions that are not a multiple of the 16x16 CTU, 1920x1080 and 640x360
  among them: `split_cu_flag` is only coded when the CU lies inside the picture,
  and the implicit split at the boundary is honoured on both sides. Generated
  streams use an 8-sample minimum coding block for such sizes.
- The QP prediction of spec 8.6.1, needed when the quantization group is smaller
  than the CTB. x265 defaults to a 32x32 group with a 64x64 CTB.

#### Time Code SEI
- `encode.GenerateTimeCodeSEI` / `encode.BuildTimeCodeSEINALU`: HEVC carries the
  SMPTE timecode in the Time Code SEI (payload type 136), not in pic_timing as
  H.264 does, and needs no VUI change.
- `pkg/timecode`: 24-hour-wrapping timecode arithmetic with NTSC drop-frame
  counting, and text formatting that takes the drop-frame flag so a burned-in
  overlay and the SEI cannot disagree.
- `hi265gen -timecode`, `-start-frame N`, `-drop-frame`, and fractional `-fps`
  (`25`, `30000/1001`, `29.97`). Applies to IDR, CRA and P-skip pictures alike, in
  both Annex-B and MP4 output.

#### CRA and Gradual Decoder Refresh
- CRA (`nal_unit_type` 21) slice generation at a caller-chosen picture order
  count — the GDR primitive, since a CRA does not reset the POC the way an IDR
  does: `encode.EncodeCRASliceFromSPSPPS`,
  `encode.EncodeGrayCRASliceFromSPSPPS` (any chroma format / bit depth),
  `hi265gray -cra -poc N`.
- CRA decoding: decoded as an intra picture, and becomes the reference frame for
  the P-skip pictures that follow.

#### hi265-mp4-extend (new command)
- Extends a fragmented MP4 media segment with empty frames, reusing the source's
  SPS and PPS verbatim so the output splices with no parameter-set change.
- `-frames N` appends P-skip copies of the last reference picture (a freeze);
  `-gray-cra` starts the span with a mid-gray CRA that continues the source POC;
  `-gray-idr` uses an IDR instead (resetting POC); `-timecode` attaches a Time
  Code SEI to each appended frame.
- `encode.LastFrameState` and `encode.AppendEmptyFrames` expose the same at the
  Annex-B level.

#### hi265dec
- MP4 input (`.mp4`, `.m4v`, `.m4s`), progressive and fragmented, with parameter
  sets from the `hvcC` box. Samples decode in order rather than jumping between
  sync samples, so P-skip frames resolve against their reference.
- `.y4m` and `.jpg` output alongside `.yuv` and `.png`; `-n` frame limit, with one
  numbered file per frame for image formats; `-q`, `-colorspace`, `-full-range`,
  `-version`.

#### hi265gen
- `-8x8`: each 16x16 CTU is coded as four independent 8x8 CUs, each with its own
  intra mode. Changes the coding structure, not yet the pixel granularity.
  Exposed on the library API as `EncodeParams.Use8x8CU` and
  `FrameEncoder.Use8x8CU`.

#### Verification
- `TestGeneratorConformance`: every generated stream is decoded by both FFmpeg and
  `pkg/decoder` and asserted byte-identical, then both are checked against the
  intended pattern. This is what nothing did before — the golden tests validated
  the decoder only.
- `TestDecodeRealContent`: 48 x265 configurations (3 content types x 4 rate
  control modes x 4 CTU/CU/TU geometries) decoded bit-exactly against FFmpeg.
- `TestDecodeWPPStreams`, `TestDeblockExactness`, and unit tests for the coding
  layout, entry point translation and the chroma QP table.
- Tests needing FFmpeg or x265 skip cleanly when those are absent; override the
  binaries with `HI265_FFMPEG` and `HI265_X265`.

### Fixed

Twelve conformance defects. Each was invisible to the existing tests for the same
reason: the generator emits flat colours, three intra modes and one transform
size, while a real encoder uses everything.

- **Intra MPM candidate B** ignored the rule that intra mode prediction does not
  cross a horizontal CTB boundary (spec 8.4.2). Encoder and decoder shared the
  omission, so they agreed with each other and with nothing else: FFmpeg's decode
  of a generated SMPTE frame differed by mean 18.5 / max 109.
- **The intra boundary filter for modes 10 and 26** (spec 8.4.4.2.6, luma below
  32x32) was missing. The term is exactly zero on column-uniform content for mode
  26 and row-uniform content for mode 10, which is why colour bars and gradients
  were unaffected.
- **Chroma reference samples were filtered.** Spec 8.4.4.2.3 invokes the smoothing
  filter for luma only (or 4:4:4 chroma, unsupported here).
- **The chroma residual of a 4x4 luma group** was parsed at the first of the four
  transform units; spec 7.3.8.10 codes it at `blkIdx == 3`, the last one.
- **The 4x4 `sig_coeff_flag` context map was indexed by scan order.** Spec
  9.3.4.2.5 has one position map with no scan dependence; the decoded bins often
  still came out right and only the arithmetic decoder's state diverged.
- **`IntraPredModeC`** was derived from the transform block's own PU rather than
  `IntraPredModeY` at the CU's top-left (spec 8.4.3). With an NxN partition the
  four luma PUs share one chroma block.
- **Mode-dependent residual scan** covered only 4x4 luma; extended to 8x8 luma and
  4x4 chroma, **the mapping was inverted** (modes 6-14 select vertical, 22-30
  horizontal), and the vertical-scan `last_sig_coeff` x/y swap was missing.
- **MPM `candModeList[1]`** computed `2+((candA-2+29)%32)` where spec 8.4.2 says
  `2+((candA+29)%32)` — two steps off whenever both neighbours predict the same
  angular mode. The encoder already had it right, so this was an
  encoder/decoder mismatch.
- **The deblocking normal filter** clipped delta before testing
  `abs(delta) < 10*tC`, making the gate vacuous (spec 8.7.2.5.7), and the
  strong/normal decision tested `(dp+dq)` against `beta >> 2` where spec
  8.7.2.5.6 tests `2*(dp+dq)`.
- **Chroma deblocking** filtered four samples per call while the caller stepped the
  4x4 luma grid, two chroma samples apart, so every chroma line was filtered twice.
- **The chroma QP table** mapped qPi 34 to 32 where spec Table 8-10 says 33. It
  existed in three copies, all with the same error; it now lives once in
  `internal/transform`.
- **The generated PPS enabled deblocking by accident**: `deblocking_filter_control_present_flag = 0`
  infers *disabled = 0*, the opposite of what the slice-header writers assume.
- Slice headers now write the long-term reference picture set fields when the SPS
  sets `long_term_ref_pics_present_flag`, and the parser consumes
  `used_by_curr_pic_lt_flag` / `delta_poc_msb_cycle_lt`.
- `hi265dec` accepted flags only before the input path, so
  `hi265dec in.265 -o out.yuv` silently ignored `-o`.
- A malformed stream could panic the decoder through out-of-range CU depth and
  intra mode map writes.
- Every `hi265gen` example in the README failed as written: the documented `-f`,
  `-grid`, `-c` and `-digits` are `-gi`, `-gp`, `-gc` and `-text`.

And one defect of a different kind, in a dependency rather than in the codec:

- **`hi265-mp4-extend` refused x265 `--no-deblock` streams.** The slice header
  parser it reads the last picture's state with missed the spec 7.4.7.1 inference
  that `slice_deblocking_filter_disabled_flag`, when absent, comes from the PPS,
  went on to read a `slice_loop_filter_across_slices_enabled_flag` bit that was
  never coded, and failed `byte_alignment()`. It affected this repo's own
  `testdata/sincos_128x64.265`; nothing caught it because the other tests extend
  streams from our generator, which does not have that shape. Fixed upstream
  (Eyevinn/mp4ff#558) and pinned here by requiring mp4ff v0.56.0.

### Changed
- mp4ff bumped from v0.52.0 to v0.56.0, which is now the minimum: it carries the
  spec 7.4.7.1 slice header inferences that `hi265-mp4-extend` depends on.
- Generated bitstreams differ from v0.1.0 for non-flat content, deliberately: the
  intra boundary filter and the MPM CTB rule both change what a conforming decoder
  reads. Flat-colour output at multiple-of-16 dimensions is unchanged byte for
  byte, pinned by a digest test.
- `pkg/decoder` output is the conformance window rather than the coded picture.
- `hi265dec` image output starts at the first decoded frame and writes one
  numbered file per frame when there is more than one; it previously wrote the
  last decoded frame to a single file.
- Frame dimensions must be a multiple of 8 rather than 16, and sizes that cannot
  be coded now return an error naming the nearest usable size instead of
  panicking.

### Known limitations
- Tiles are not supported: the entry point offsets parse, but tile scan order and
  per-tile CABAC reset are not implemented.
- P and B frames with real motion are out of scope; only zero-motion skip is
  implemented, so inter pictures beyond a freeze are far off.
- Generated frame dimensions must be a multiple of 8. Finer sizes need a
  conformance window on the encoder side and are rejected with an error naming the
  nearest usable size.
- Decoding is 8-bit 4:2:0. `hi265gray` generates any chroma format and bit depth,
  but its output can only be verified with an external decoder.

## [0.1.0] - 2026-05-12

Initial release of hi265 — a pure Go HEVC/H.265 frame decoder and bitstream
generator. Sister project to [hi264](https://github.com/Eyevinn/hi264).

### Added

#### Decoder (hi265dec)
- CABAC arithmetic engine and 170 context models (I-slice and P-slice init)
- CTU/CU/TU quadtree parsing with intra and inter (skip) prediction
- All 35 HEVC intra prediction modes (planar, DC, angular)
- Inverse quantization and transform (4x4, 8x8, 16x16 DCT)
- Deblocking filter (luma and chroma)
- Sample Adaptive Offset (SAO) — band offset and edge offset
- Sign data hiding and transform skip
- P-frame decoding (all-skip CUs with zero-motion copy)
- cu_qp_delta support
- Auto-detection of Annex-B input
- Raw YUV output

#### Encoder (hi265gen)
- HEVC IDR frame encoder with DC intra prediction (Main profile, CABAC)
- P-skip slice encoder (all-skip CUs copying from reference)
- Forward DCT and HM-compatible quantization (QP 0-51)
- VPS/SPS/PPS generation with VUI colour description
  (BT.601/BT.709/BT.2020, full/limited range)
- `FrameEncoder` type with grid-based API using hi264's `pkg/yuv` package
- Grid-based pattern input with RGB or YCbCr color specification
- `.gridimg` file support with `@rgb`, `@bt709`, `@bt2020` directives
- SMPTE 75% color bars, frame counter overlay, tiled backgrounds
- P-skip frame sequences with configurable IDR interval
- Filler NALUs for fixed bytes-per-picture output
- Multiple output formats: `.265`/`.hevc`, `.mp4`, `.y4m`, `.yuv`, `.png`, `.jpg`
- Fragmented MP4 (fMP4/CMAF) output with HEVC descriptor

#### Gray IDR generator (hi265gray)
- hi265gray CLI and `EncodeGrayIDRSliceFromSPSPPS` API for generating uniform
  mid-gray IDR frames from external VPS/SPS/PPS. Supports any chroma format
  (4:2:0, 4:2:2, 4:4:4) and bit depth (8, 10, 12). Uses DC prediction with
  zero residual and largest-possible CU encoding (~275 bytes for 1920x1080,
  ~40 us on Apple M4 Pro). Intended for bootstrapping GDR streams that lack
  IDR frames.

#### Verification
- 16 golden decoder test cases with pixel-perfect FFmpeg match
- Encoder round-trip tests (encode → decode byte-exact match)
- Multi-resolution gray IDR test covering 12 resolutions (64x64 to 1920x1088)
  with CTU=32 and CTU=64, verified pixel-perfect against FFmpeg decode
- Benchmark for gray IDR generation at 1920x1080 in three formats
  (4:2:0 8-bit, 4:2:0 10-bit, 4:2:2 10-bit)

[Unreleased]: https://github.com/Eyevinn/hi265/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Eyevinn/hi265/releases/tag/v0.1.0
