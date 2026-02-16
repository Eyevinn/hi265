# Codec Internals

This document describes how the hi265 decoder and encoder work internally:
intra prediction, P-skip frames, and how external VPS/SPS/PPS parameter sets
drive the decoding process.

## Intra Prediction

### Overview

Intra prediction reconstructs pixels from already-decoded neighbors within the
same frame. HEVC defines 35 intra modes (0-34):

| Mode | Name | Description |
|------|------|-------------|
| 0 | Planar | Bilinear interpolation from edges |
| 1 | DC | Flat fill from average of neighbors |
| 2-34 | Angular | Directional prediction at various angles |

The decoder supports all 35 modes. The encoder only uses DC (mode 1).

### Quadtree Structure

HEVC splits frames into a hierarchy of blocks:

```
CTU (Coding Tree Unit, up to 64x64)
 └─ CU (Coding Unit, split via coding quadtree)
     └─ TU (Transform Unit, split via transform quadtree)
```

**Coding quadtree (CTU to CU):** Each CTU can be recursively split into four
equal sub-CUs. The `split_cu_flag` controls splitting, with context derived from
left and above neighbor depths. Splitting stops at `log2MinCbSize` from the SPS.

**Partition mode:** At the CU level, intra CUs use either 2Nx2N (one prediction
unit covering the whole CU) or NxN (four prediction units, only allowed at the
minimum CU size). NxN mode sets `IntraSplitFlag`, which increases the maximum
transform depth by one and forces a split at transform tree depth 0 without
decoding `split_transform_flag`.

**Transform quadtree (CU to TU):** Within a CU, the residual can be further
split via `split_transform_flag`. The split depth is bounded by
`MaxTransformHierarchyDepthIntra` from the SPS. For 4:2:0 chroma, the chroma TU
is half the luma size (minimum 4x4), and chroma coefficients at the 4x4 level
are only present for the top-left TU of the group.

### Intra Mode Coding (CABAC)

Each prediction unit's intra mode is coded in two steps:

1. **`prev_intra_luma_pred_flag`**: A context-coded flag indicating whether the
   mode is in the Most Probable Mode (MPM) list.

2. If MPM: **`mpm_idx`** (truncated unary, 0-2) selects from the 3-entry MPM
   list. If not MPM: **`rem_intra_luma_pred_mode`** (5 bypass bins) encodes the
   remaining mode index after excluding the sorted MPM entries.

**MPM derivation** follows spec section 8.4.2: the left and above neighbor intra
modes are used to build a 3-entry candidate list. When both neighbors have the
same mode, the list is `{mode, mode-1, mode+1}` for angular modes or
`{Planar, DC, Angular26}` for DC/Planar. When they differ, the third entry is
Planar, DC, or Angular26 depending on what is already in the list.

**Chroma mode** is coded as a single context bin: 0 selects DM mode (derived
from luma), 1 plus two bypass bins selects from `{Planar, Angular26, Angular10,
DC}`. If the selected chroma mode matches the luma mode, it is substituted with
mode 34 per Table 8-1.

### Reference Sample Preparation

Before prediction, the decoder gathers reference samples from reconstructed
neighbors (left column, top row, top-left corner, plus extensions for angular
modes). Unavailable samples are filled by substitution from the nearest available
sample, scanning bottom-left, up through top-left, then right to top-right (spec
8.4.4.2.2).

Reference samples may then be filtered with a 3-tap `[1, 2, 1]` smoothing
filter. The filtering decision depends on the mode and block size:

- DC: never filtered
- Planar: filtered for size >= 8
- Angular: filtered when the angle is far from horizontal/vertical, with a
  size-dependent threshold

For 32x32 luma blocks with `strong_intra_smoothing_enabled_flag` set in the SPS,
a bilinear interpolation replaces the 3-tap filter when the edge samples are
smooth.

### Prediction Modes

**Planar (mode 0):** Bilinear interpolation using four edge values (top-right,
bottom-left, and their opposing edges). Each pixel is a weighted average based on
its distance from the edges.

**DC (mode 1):** The average of the left column and top row reference samples
fills the entire block. For luma blocks smaller than 32x32, the top-left corner
and edges adjacent to reference samples are blended for smoother transitions.

**Angular (modes 2-34):** Directional prediction using the `intraPredAngle`
lookup table. Modes 2-17 are horizontal-dominant, modes 18-34 are
vertical-dominant. For modes with negative angles (11-25, excluding the exact
horizontal/vertical modes 10 and 26), the `invAngle` table projects reference
samples from the perpendicular edge. Sub-pixel positions use bilinear
interpolation: `pred = ((32-iFact)*ref[idx] + iFact*ref[idx+1] + 16) >> 5`.

### Residual Coding

After prediction, the residual (original minus predicted) is transform-coded:

1. **Forward DCT** (encoder only): Column pass with shift `log2N + bitDepth - 9`,
   then row pass with shift `log2N + 6`. The 4x4 intra luma case uses DST
   instead of DCT.

2. **Quantization** (encoder): `level = sign * ((abs(coeff) * scale + offset) >> qBits)`,
   using HM-compatible scale factors and a dead-zone offset of `171 << (qBits-9)`.

3. **CABAC coefficient coding**: Coefficients are grouped into 4x4 sub-blocks,
   scanned in reverse order. For each sub-block:
   - `coded_sub_block_flag` signals whether any coefficients are non-zero
   - `sig_coeff_flag` marks individual non-zero positions (44 contexts: 27 luma +
     15 chroma + 2 skip)
   - `coeff_abs_level_greater1_flag` and `greater2_flag` refine magnitudes
   - Sign bits are bypass-coded
   - `coeff_abs_level_remaining` uses truncated Rice + Exp-Golomb for large values

   For 4x4 luma TUs, the scan order (diagonal, horizontal, or vertical) depends
   on the intra mode. Middle sub-blocks use implicit significance: if
   `coded_sub_block_flag=1` but no coefficient at positions 1-15 is significant,
   position 0 is implicitly significant without a CABAC bin.

4. **Inverse quantization** (decoder): `d = (coeff * 16 * LevelScale[qp%6] << (qp/6) + round) >> bdShift`,
   where `LevelScale = {40, 45, 51, 57, 64, 72}` and `bdShift = bitDepth + log2TrafoSize - 5`.

5. **Inverse DCT/DST** (decoder): Column-first pass with `>>7` rounding, then
   row pass with `>>12` rounding. Values are clipped to 16-bit signed range
   between passes.

6. **Transform skip**: For 4x4 TUs with `transform_skip_flag=1`, the inverse
   transform is bypassed. Coefficients are scaled by a fixed bit-depth shift
   instead.

The reconstructed pixel is `clip(prediction + residual, 0, 255)`.

## P-Skip Frames

### Concept

P-skip frames contain no new pixel data. Every CU in the frame is a skip CU
that copies its block from the reference frame with zero motion. This is the
simplest form of inter prediction: no motion vectors, no residual, no transform
coefficients.

This is useful for extending video duration without increasing bitstream size
significantly. Between IDR keyframes, P-skip frames repeat the previous frame's
content unchanged.

### Decoding

When the decoder encounters a TRAIL_R or TRAIL_N NAL unit, it parses it as a
P-slice:

1. **Slice header**: Includes `pic_order_cnt_lsb` for frame ordering and a
   `short_term_ref_pic_set` identifying the reference frame (typically
   `deltaPOC = -1`).

2. **CABAC initialization**: P-slices use different context model initial values
   than I-slices. The 170 context models are re-initialized with P-slice tables,
   affecting `cu_skip_flag`, `merge_flag`, `merge_idx`, and `pred_mode_flag`
   contexts.

3. **CU decoding**: For each CTU, `cu_skip_flag` is decoded (3 contexts based on
   left/above neighbor availability). When the flag is 1, `merge_idx` is decoded
   as a truncated unary to select the merge candidate. No further syntax elements
   are parsed for that CU: no prediction mode, no partition mode, no residual.

4. **Reconstruction**: Each skip CU copies its luma and chroma blocks directly
   from the reference frame at the same position (zero motion vector). For 4:2:0,
   the chroma copy uses half the CU size at half the position.

### Encoding

The encoder generates P-skip frames with `EncodePSkipFrame()`:

1. **Slice header**: Written with `slice_type=1` (P-slice), the POC LSB, and an
   inline `short_term_ref_pic_set` referencing the previous frame.

2. **CTU encoding**: Every CTU writes `cu_skip_flag=1` and `merge_idx=0`. With
   `five_minus_max_num_merge_cand` set to make the merge list size 1, the
   `merge_idx` requires zero bins.

3. **No residual**: Since skip CUs have no residual, the bitstream after the
   slice header is minimal: just the skip and merge flags for each CTU.

### Reference Frame Management

The decoder maintains a single reference frame pointer. After decoding each
frame (IDR or P), the reconstructed frame becomes the new reference. There is no
multi-frame DPB (decoded picture buffer): only the most recent frame is kept.

This is sufficient for the supported use case: IDR frames followed by P-skip
frames that copy from the immediately preceding frame.

## VPS, SPS, and PPS

The decoder does not implement its own parameter set parsing. It relies on the
`github.com/Eyevinn/mp4ff` library to parse VPS, SPS, and PPS NAL units from
any compliant HEVC bitstream.

### Parsing Flow

When the decoder receives an Annex-B byte stream, it extracts NAL units and
dispatches by type:

```
VPS (NAL type 32) → stored in vpsMap (raw bytes, not fully parsed)
SPS (NAL type 33) → parsed via mp4ff hevc.ParseSPSNALUnit(), stored in spsMap by ID
PPS (NAL type 34) → parsed via mp4ff hevc.ParsePPSNALUnit(), stored in ppsMap by ID
IDR (NAL type 19/20) → decoded using active SPS/PPS
TRAIL_R/N (NAL type 0/1) → decoded as P-slice using active SPS/PPS
```

Multiple parameter sets can coexist (keyed by ID), though in practice the
encoder produces a single VPS/SPS/PPS triplet.

### SPS Parameters That Drive Decoding

The SPS controls the fundamental frame and block structure:

| Parameter | Effect |
|---|---|
| `PicWidthInLumaSamples`, `PicHeightInLumaSamples` | Frame dimensions, buffer allocation |
| `Log2MinLumaCodingBlockSizeMinus3` | Minimum CU size (controls quadtree leaf) |
| `Log2DiffMaxMinLumaCodingBlockSize` | CTU size = min CU size << this value |
| `Log2MinLumaTransformBlockSizeMinus2` | Minimum transform block size |
| `Log2DiffMaxMinLumaTransformBlockSize` | Maximum transform block size |
| `MaxTransformHierarchyDepthIntra` | How deep the transform quadtree can split |
| `SampleAdaptiveOffsetEnabledFlag` | Enables SAO filtering after reconstruction |
| `Log2MaxPicOrderCntLsbMinus4` | Bit width of POC LSB field in slice headers |
| `NumShortTermRefPicSets` | Number of short-term reference picture sets in SPS |

### PPS Parameters That Drive Decoding

The PPS controls slice-level coding options:

| Parameter | Effect |
|---|---|
| `InitQpMinus26` | Base QP for the slice (`SliceQpY = 26 + init_qp + slice_qp_delta`) |
| `SignDataHidingEnabledFlag` | Sign of last coefficient inferred from parity |
| `TransformSkipEnabledFlag` | Allows bypassing DCT for 4x4 TUs |
| `CuQpDeltaEnabledFlag` | Allows per-CU QP adjustment |
| `DiffCuQpDeltaDepth` | Granularity of CU QP delta groups |
| `DeblockingFilterDisabledFlag` | Disables the deblocking filter |
| `DeblockingFilterOverrideEnabledFlag` | Allows slice-level deblock override |
| `BetaOffsetDiv2`, `TcOffsetDiv2` | Deblocking filter strength offsets |
| `NumRefIdxL0DefaultActiveMinus1` | Default number of L0 references |

### Encoder Parameter Generation

The encoder generates minimal but valid parameter sets:

**VPS**: Main profile, Level 3.0, single layer, max DPB size 2, no reordering.

**SPS**: Dynamic width/height, 4:2:0 chroma, 8-bit depth, CTU size 16x16 (no
splitting), min transform size 4x4, max transform size 16x16. Most tools are
disabled: no SAO, no AMP, no PCM, no temporal MVP, no strong intra smoothing. If
a color space is specified (BT.601/BT.709/BT.2020), VUI parameters are included
with the appropriate colour_primaries, transfer_characteristics, and
matrix_coefficients values.

**PPS**: QP from the encode parameters, everything else disabled (no sign data
hiding, no transform skip, no CU QP delta, no deblocking, no tiles). Merge list
size is 5 (standard default) for IDR frames and effectively 1 for P-skip frames.

This minimal configuration means the encoder's bitstreams are simple to decode:
no optional tools are active, CTUs are never split, and every CTU uses DC
prediction with a single QP.

### Encoding with External VPS, SPS, and PPS

The encoder can produce IDR and P-skip slices that are compatible with
externally-provided parameter sets. This is useful when parameter sets come from
an external source such as an MP4 sample description or a third-party encoder,
and the application needs to inject new frames into an existing stream.

**API:**

```go
// Parse SPS/PPS from an existing bitstream
sps, _ := hevc.ParseSPSNALUnit(spsNALU)
pps, _ := hevc.ParsePPSNALUnit(ppsNALU, spsMap)

// Encode an IDR slice compatible with those parameter sets
idrBytes, _ := encode.EncodeIDRSliceFromSPSPPS(sps, pps, y, cb, cr)

// Encode a P-skip slice compatible with those parameter sets
pSkipBytes, _ := encode.EncodePSkipSliceFromSPSPPS(sps, pps, poc)
```

Both functions return Annex-B framed NAL units (IDR_W_RADL or TRAIL_R) without
VPS/SPS/PPS prefixed. The caller is responsible for providing the parameter set
NALUs separately (e.g., in the MP4 decoder configuration record or prepended to
the Annex-B stream).

**IDR slice (`EncodeIDRSliceFromSPSPPS`):** Encodes a full intra frame using DC
prediction, respecting the external PPS QP (`26 + init_qp_minus26`) and writing
the correct slice header fields: `slice_pic_parameter_set_id`,
`num_extra_slice_header_bits`, SAO flags (if enabled in SPS), deblocking filter
syntax (if controlled in PPS), and `slice_loop_filter_across_slices_enabled_flag`.
Currently requires CTU size 16 (`log2MinCbSize=4, log2Diff=0`); larger CTU sizes
are rejected with an error.

**P-skip slice (`EncodePSkipSliceFromSPSPPS`):** Encodes an all-skip frame with
the correct POC, short-term reference picture set (inline or SPS-indexed),
temporal MVP flag, SAO flags, CABAC init flag, chroma QP offsets, and deblocking
syntax, all derived from the external SPS/PPS. Supports arbitrary CTU sizes
since skip CUs do not require quadtree splitting.

**Validation:** Both functions reject unsupported PPS features: tiles,
dependent slice segments, and weighted prediction. The IDR function additionally
validates that the CTU size is 16.

**`FrameEncoder` methods:** The `FrameEncoder` type exposes
`EncodeIDRSliceFromSPSPPS` and `EncodePSkipSliceFromSPSPPS` methods that build
pixel data from the grid/color map before delegating to the standalone functions.
