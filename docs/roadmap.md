# hi265 roadmap — parity with hi264, then ahead

Status: phases 0, 1, 2, 3 and 4.1/4.3 are complete; 4.2 and 5.x remain. Baseline
was hi265 v0.1.0 against hi264 v0.10.0. This document plans the work to reach
parity and then move ahead using HEVC-specific capabilities that H.264 cannot
express. Items are marked **fixed**/**done** as they land, with what was actually
measured, so the history of why each change exists stays with the plan.

Phases are ordered by dependency. Sizes are rough: **S** ≈ half a day,
**M** ≈ 1–2 days, **L** ≈ 3+ days.

---

## Phase 0 — Correctness first (blocks everything else) — **complete**

Nothing else was worth building until generated bitstreams decoded identically in
a conforming decoder, and until real streams decoded at all.

All twenty-two items are closed. The phase started with two known defects and
grew: each fix made the next one visible, and the conformance harness (0.2) is
what turned "FFmpeg disagrees" into a specific spec clause every time. Generated
content, real x265 output and tiled streams are all bit-exact against FFmpeg now.

One thing remains, narrowed to something specific rather than left vague:

- **A conformance window on the encoder side** (see 0.8): the decoder applies
  one, but the generator cannot emit dimensions finer than a multiple of 8, so
  those are rejected with a clear error instead.

Ordered by dependency, the fixes were: 0.1 → 0.5 → 0.2 (the harness) → 0.6, 0.7
(which the harness localised) → 0.8 → 0.9, 0.12 → 0.10, 0.11 → 0.13 → 0.14 →
0.15 → 0.16 → 0.17 → 0.18 → 0.19 → 0.20 → 0.21 → 0.22. The three
small items 0.3, 0.4 and 0.11 were housekeeping alongside.

### 0.1 MPM candidate-B CTB boundary rule (S) — **fixed**

Spec 8.4.2 forces `candIntraPredModeB = INTRA_DC` when `yPb − 1` lies outside
the current CTB ("intra mode prediction does not cross the vertical CTB
boundary"). The rule is missing in both:

- `pkg/encode/slice.go:294` — `deriveMPM`, uses the actual above mode
- `internal/slice/slicedata.go:409` — `candB := modeMap.get(px, py-1)`

Encoder and decoder share the omission, so they agree with each other and
disagree with every conforming decoder. With CTU=16 and CU=16 the above
neighbour is *always* in a different CTB, so it misfires on every CU below the
first CTU row that selected mode 10 or 26.

Measured on a plain 192x96 SMPTE frame from `hi265gen`, mean absolute error
versus the intended pattern:

| decoder | mean | max |
|---|---|---|
| FFmpeg | 18.5 | 109 |
| `hi265dec` | 0.31 | 4 |

With the rule applied in the encoder, FFmpeg's decode drops to mean 0.23 /
max 3 — pure quantization error. Applying it in the decoder keeps all existing
golden tests green: the rule fires only twice across all 18 test streams, both
times on flat-colour content where the mode does not change pixels. That is
also why the goldens never caught it.

Fix both sites, then add a golden test that actually exercises it (see 0.2).

### 0.2 Generator conformance harness (M) — **done**

The golden tests validate the *decoder* against FFmpeg on x265-produced
streams. Nothing validates the *encoder* against FFmpeg. 0.1 is exactly the
class of bug that slips through: a shared encoder/decoder assumption that is
self-consistent and non-conformant.

Add `pkg/encode`-level tests that, for each of a set of patterns
(SMPTE bars, grid, counter overlay, multi-CTU-row, several QPs, P-skip
sequences):

1. generate the bitstream,
2. decode it with FFmpeg (skip the test when FFmpeg is absent),
3. decode it with `hi265dec`,
4. assert FFmpeg ≡ `hi265dec` exactly, and both within quantization tolerance
   of the intended pattern from `hi265gen`'s own `.yuv` output.

Step 4's first assertion is the one that matters — it is what 0.1 violated.

Landed as `pkg/encode/conformance_test.go`: 11 cases (flat, tiled, SMPTE, QP
20/26/40, one and multiple CTU rows, P-skip structures, a digit overlay and a
diagonal), each decoded by FFmpeg and by `pkg/decoder` and asserted
byte-identical, then both checked against the intended pattern. Failures name
the plane, sample count, max delta and the CTU of the worst sample. Skips
cleanly when `ffmpeg`/`x265` are absent.

The harness was validated by reverse-applying 0.1, which makes the SMPTE cases
fail at max delta 107 and mean 18.41 — reproducing the original bug. It then
immediately earned its keep by localising 0.6 and 0.7.

### 0.3 README flag drift (S) — **fixed**

Every `hi265gen` example in `README.md` failed as written: the documented `-f`,
`-grid`, `-c`, `-digits` are actually `-gi`, `-gp`, `-gc`, `-text` (and `-f` is
now the output format). The examples and the flag table are regenerated from
`parseOptions`, and every example in the README is run as part of verifying a
docs change — which is how the `-w 640 -h 360` crash behind 0.8 was found.

### 0.5 Deblocking was enabled in the generated PPS by accident (S) — **done**

The PPS wrote `deblocking_filter_control_present_flag = 0`, which infers
`pps_deblocking_filter_disabled_flag = 0` — i.e. deblocking **on**, the opposite
of what the slice-header writers and their comments assume. Now signalled
explicitly as disabled. Effect on FFmpeg-vs-`hi265dec` agreement for generated
streams:

| pattern | before | after |
|---|---|---|
| tiled two-colour grid, 192x96 | 7184 samples differ, max 4 | exact |
| SMPTE bars, 192x96 | 1104 differ, max 2 | exact |
| SMPTE + `%03d` overlay | luma 5163 / max 75, chroma 824 / max 4 | luma 2112 / max 76, chroma exact |
| 3-frame P-skip sequence | chroma drift growing 4 → 12 → 20 per frame | no drift |

### 0.6 Missing intra boundary filter for modes 10 and 26 (M) — **fixed**

Not residual coding at all, as first suspected: the final step of spec
8.4.4.2.6 was missing from `internal/pred.PredictAngular`. For luma blocks
below 32x32 it nudges the first column (mode 26, vertical) or first row
(mode 10, horizontal) by half the gradient of the perpendicular neighbours:

    mode 26:  pred[0][y] = Clip1Y(p[0][-1]  + ((p[-1][y] - p[-1][-1]) >> 1))
    mode 10:  pred[x][0] = Clip1Y(p[-1][0]  + ((p[x][-1] - p[-1][-1]) >> 1))

Both the encoder and the decoder call that one function, so they agreed with
each other and with nothing else. The term is exactly zero on column-uniform
content for mode 26 and on row-uniform content for mode 10 — which is why flat
colours, colour bars and smooth gradients were unaffected, and why 16 goldens
plus 9 of 11 conformance cases missed it. The measured deltas matched the
missing term exactly, three CTUs for three.

Fixed; both reproduction cases (digit overlay, diagonal) are now bit-exact
against FFmpeg. It also improved decoding of real x265 content, where the same
signature appeared on the first row/column of transform blocks.

### 0.7 Deblocking normal filter gated on the clipped delta (M) — **fixed**

`internal/deblock/deblock.go` clipped delta to `[-tC, tC]` and only then tested
`abs(delta) < 10*tC`. Clipping first makes the gate vacuous — the clipped value
is at most tC, always below 10*tC — so every edge passing the `d < beta`
decision got filtered, including the hard edges spec 8.7.2.5.7 deliberately
leaves alone. The chroma path has no such gate and was already correct, which
matches chroma always having been exact.

Isolated on a white/black split frame, where deblocking-disabled decodes
bit-exactly at every QP:

| QP | deblocking off | deblocking on (before fix) |
|---|---|---|
| 22 / 26 | exact | 128 samples, max 1 |
| 30 / 34 | exact | 256 samples, max 2–3 |
| 40 | exact | 256 samples, max 5 |

On a static P-frame run the error compounded — each frame re-filtering an
already-over-smoothed edge — giving worst luma deltas of 1, 3, 5, 7 across four
frames at QP 26 and 3, 7, 11, 15 at QP 34, unbounded on a long static run.

Fixed by gating on the raw delta and clipping inside the branch. All six
conformance cases are now bit-exact. The golden deblock streams stayed green
throughout because they are flat or smooth-gradient content, where the raw delta
never approaches 10*tC; the `--qp 40` choice in the project notes turns out to
be unrelated — on this content the error grew with QP.

### 0.8 Frame heights not a multiple of the CTU size (M) — **fixed**

`hi265gen -w 1920 -h 1080` panicked with an index out of range, as did every
size that is not a multiple of 16: 640x360, 320x232, 128x72. The decoder was
worse — it panicked on a real x265 640x360 stream and produced garbage from CTU
row 15 on another. Both of the most common heights in use, 1080 and 360, are
8 mod 16.

Three parts to the fix:

1. **Decoder.** Spec 7.3.8.4 codes `split_cu_flag` only when the CU lies
   entirely inside the picture; a CU crossing the right or bottom edge is split
   with no flag, inferred while the size stays above MinCbSizeY. The decoder
   read the flag unconditionally, desynchronising on the first partial CTU. Its
   CU-depth and intra-mode map writes are now bounds-checked as well — a
   malformed stream should not be able to panic a decoder.
2. **Encoder.** One shared `encodeCodingQuadtree` writes the flag exactly where
   the spec codes it and calls back for each CU a decoder will parse, so the IDR
   and P-skip paths cannot diverge. `chooseCodingLayout` drops MinCbSizeY to 8
   when either dimension is not a multiple of 16, in one place consulted by both
   the SPS writer and the slice data.
3. **Two bugs only a mixed-CU-size picture could expose**: `part_mode` is only
   coded at the minimum CB size (7.3.8.5) and was written unconditionally, which
   desynchronises CABAC for a 16x16 CU when MinCbSizeY is 8; and the encoder's
   intra mode map was keyed by CU index (`x/cuSize`), addressing the wrong block
   as soon as 16x16 and 8x8 CUs coexist. It is now addressed by pixel at 4x4
   granularity, mirroring the decoder.

Verified against FFmpeg — every case bit-exact and within 1 of the intended
pattern:

| size | minCbSize | result |
|---|---|---|
| 1920x1080 | 8 | exact |
| 640x360 | 8 | exact |
| 320x232 | 8 | exact |
| 128x72 | 8 | exact |
| 640x360 with P-skip | 8 | exact, appended frames exact copies |
| 640x360 with `-8x8` | 8 | exact |
| 1920x1088, 640x352 | 16 | exact, bitstreams unchanged |

Two real x265 streams at 640x360 and 1920x1080 that previously panicked the
decoder now decode bit-exactly. Sizes that are multiples of 16 are unchanged
byte for byte.

Remaining: dimensions that are not a multiple of 8 cannot be expressed with a
16x16 CTU at all — 8 is the smallest legal MinCbSizeY. They now return an error
naming the nearest usable size instead of panicking. Supporting them needs a
conformance window (`conf_win_*_offset`), which also means teaching the decoder
to crop on output; that is a separate piece of work and nothing needs it yet.

### 0.9 WPP streams (M) — **fixed**

`hi265dec` could not decode `testdata/hevc_1idr_1p.mp4`, failing with
`slice header: expected stop bit 1, got 0`. The slice header carries
`num_entry_point_offsets` (plus `offset_len_minus1` and the offsets) whenever
tiles or wavefront parallel processing is enabled (spec 7.3.6.1); the decoder
never read them, so the header misaligned. x265 enables WPP by default, so this
was most real-world HEVC.

Three parts to the fix:

1. **Header.** One shared `finishSliceHeader` parses the entry point offsets,
   the slice segment header extension and `byte_alignment` for both the IRAP and
   trailing-slice paths, which had duplicated that tail. It also stopped
   ignoring `slice_segment_header_extension_length`.
2. **Substreams.** With WPP each CTU row is its own CABAC substream: the
   arithmetic engine restarts at the row's entry point and the context state
   comes from a snapshot taken after the **second** CTU of the row above, not
   from the end of that row (spec 9.3.1). A picture one CTU wide has no such
   snapshot and starts each row fresh; `qPY_PREV` resets to SliceQpY per row.
3. **Offsets count emulation prevention bytes** (7.4.7.1), but the substreams
   are split out of the stripped buffer. On flat content, which escapes often,
   the raw offsets ran well past the end of it — 287 into a 231-byte buffer. The
   offsets are now translated using the positions of the removed bytes.

Verified bit-exact against FFmpeg on flat content, all with WPP on:

| case | result |
|---|---|
| CTU 16, 640x352 (22 CTU rows) | exact |
| CTU 32 / CTU 64 | exact |
| single CTU column (no snapshot to inherit) | exact |
| `cu_qp_delta` enabled | exact |
| x265 defaults (WPP + SAO + sign hiding + 32x32 TBs), 256x192 | exact |
| x265 defaults, 1280x720 | exact |

**Tiles** were left for later here, and are done as of 0.13 — including the
per-tile CABAC reset for a slice segment that spans several tiles.

### 0.11 Chroma QP table was off by one at qPi 34 (S) — **fixed**

Spec Table 8-10 maps qPi 34 to chroma QP 33; both copies of the mapping said 32,
giving a chroma-only mismatch against FFmpeg at exactly QP 34 (mean 1.6, max 14)
while every neighbouring QP was exact. The table lived twice, once in
`pkg/encode` and once in `pkg/decoder`, which is what allowed the drift; it now
lives in `internal/transform` with a test pinning the whole table and
monotonicity.

### 0.12 Conformance window was ignored (S) — **fixed**

Found while fixing 0.9. An encoder that pads the coded picture up to a whole
number of CTUs signals the padding in the SPS conformance window — x265 codes a
360-line source as 368 lines — and the decoder returned the coded size, so the
output had the wrong dimensions and every comparison after the first row of
padding was meaningless. The output is now cropped while the full coded picture
stays as the prediction reference. On x265 colour bars this took 640x360 from
30723 differing samples (max 196) to 1112 (max 3), and 1920x1080 from 99760
(max 197) to 3336 (max 12) — the remainder being 0.10.

### 0.13 Tiles decoding (L) — **fixed**

The tiles half of 0.9, planned in `docs/tiles-decoding.md` and taken as far as
its items T1–T4b. Before this, every tiled stream failed on its *second* slice
segment with `slice header: expected stop bit 1, got 0`, because
`slice_segment_address` was never read.

Seven things landed, the first five in that order out of necessity rather than
taste:

1. **A picture from several slice segments.** `first_slice_segment_in_pic_flag`
   opens a picture, the next first-flag or the end of the NAL stream closes it,
   and only then do the loop filters, the reference update and the conformance
   window crop run (`pkg/decoder/picture.go`). Multi-slice pictures used to come
   out as one bogus frame per slice.
2. **Spec 6.5.1 scan tables** in `internal/tiles`: `colBd`/`rowBd`,
   `CtbAddrRsToTs`, `CtbAddrTsToRs`, `TileId`, with uniform spacing as the
   `(i+1)*n/N - i*n/N` formula rather than an equal division — 5 CTBs in two
   columns is 2 then 3.
3. **Slice segment header**: `dependent_slice_segment_flag`,
   `slice_segment_address` and the `num_extra_slice_header_bits` reserved flags,
   none of which were read.
4. **Tile scan iteration** bounded by the segment, replacing a raster walk of
   the whole picture.
5. **Neighbour availability** (spec 6.4.1). The raster CTB address comparison in
   `buildRefSamples` is gone: availability is now "inside the picture and
   already reconstructed", with the reconstruction map cleared at each segment
   start. Without this the work above was invisible — a 2x2 picture decoded tile
   0 perfectly and the other three as garbage, since every CTB of tile 1 has a
   lower-addressed CTB in tile 0 to mispredict from. The SAO merge flags were
   gated the same way, which matters more: they are *parsed* conditionally, so a
   bin read across a tile or slice boundary desyncs CABAC.

6. **Loop filters that stop at boundaries.** With
   `loop_filter_across_tiles_enabled_flag` equal to 0, neither deblocking nor
   SAO may reach across a tile edge, and the same holds at a slice edge whose
   `slice_loop_filter_across_slices_enabled_flag` is 0. `internal/loopfilter`
   holds the answer for both filters — tiles and slices are whole CTBs, so "may
   these two samples be filtered together?" is a question about their two CTBs.
   Deblocking clears the affected edge flags before filtering (the spec 8.7.2
   `filterEdgeFlag` derivation); SAO leaves a sample alone when either end of
   its edge offset class is unavailable (8.7.3.2). The per-slice deblocking
   parameters became real lookups at the same time: beta and tC offsets from the
   slice holding the q side of the edge, and a slice with deblocking disabled
   contributes no edges while still contributing its QPs to a neighbour's.

   `slice_loop_filter_across_slices_enabled_flag` was read and discarded, and
   keyed on the SPS SAO flag rather than the two slice SAO flags; it is now used,
   and inferred from the PPS when absent (spec 7.4.7.1). It is not decoration:
   x265 varies it per frame, so in `grid.265` it is 1 in three pictures and 0 in
   two, verbatim through the stitch.

7. **Several tiles in one slice segment**, which is what `kvazaar --tiles` emits
   by default and what most encoders do — the retiler's one-slice-per-tile shape
   is the unusual one. Each tile of the segment is a substream reached through an
   entry point offset, and three things reset at its first CTB: the CABAC
   contexts (spec 9.3.1), `qPY_PREV` (8.6.1), and neighbour availability, since
   nothing in an earlier tile may be predicted from (6.4.1). All three are
   load-bearing and each is pinned by a committed vector — the QP one needed a
   delta-QP map to become observable at all, since without `cu_qp_delta` the
   predicted QP never moves.

Measured with kvazaar (`--tiles WxH [--slices tiles]`, the shape x265 cannot
produce at all) and with `hevc-retiler` output:

| stream | before | after T1–T4 | after T5 |
|---|---|---|---|
| tiled, both loop filters off, 2x2 and non-equal 2x1 | header error | exact | exact |
| tiled with SAO on, 256x256 | header error | 169 samples differ | **exact** |
| tiled with deblocking on, 256x256 | header error | 1 972 differ | **exact** |
| retiler `merged`, `grid`, `h`, `nu` (5 frames each) | header error | 2 871–14 651 differ | **exact** |
| the same streams' standalone inputs | exact | exact | exact |
| several tiles per segment: filters off, deblocking, SAO, both, non-equal 2x1, delta-QP | refused | refused | **exact** |

The middle column's differences all sat within the loop filter's reach of a tile
seam and nowhere else, which is what identified the remaining work as exactly
the boundary rules. Removing those rules again reproduces those counts to the
sample, so nothing else about the filters changed.

The check the caller cares about now runs without ffmpeg: each tile's
sub-rectangle of a merged picture equals that input's standalone decode under
hi265 alone, every frame. That is `retile -verify`'s whole test.

Dependent slice segments and WPP across several slice segments followed in 0.14.
What stays refused is tiles combined with WPP, which no HEVC profile permits, so
it is deliberate rather than pending.

Eight golden vectors — five with one slice segment per tile, three with every
tile in one segment — plus unit tests for the scan tables and the boundary
rules; `tools/gen_tiles_bitstreams.sh` regenerates them all.

### 0.14 Slice segment structure (M) — **fixed**

A slice is one independent slice segment plus any number of dependent ones
(spec 7.4.7.1), and 0.13 handled only the independent kind. Two real shapes were
refused: `kvazaar --slices wpp`, which puts every CTB row in a dependent segment,
and `x265 --wpp --slices N`, which cuts a picture into N independent slices that
each carry their own wavefront substreams.

**Dependent segments.** Their header holds nothing but the address, so the slice
type, QP, SAO flags and loop filter parameters all come from the independent
segment that opened the slice. What used to be per-segment is now per-slice: the
CABAC contexts are stored when a segment ends and resumed when the next one
begins (spec 9.3.2.4), and QP prediction, the intra mode and CU depth maps and
the sample availability map all carry over, because spec 6.4.1 makes a neighbour
available when it is in the same *slice* — a boundary inside a slice is not a
prediction boundary. The SAO merge flags are gated on `SliceAddrRs`, the slice's
first CTB rather than the segment's, which is what spec 7.3.8.3 actually says.
And the loop filters see a dependent boundary as interior, since the segment
shares its slice's entry in the boundary map.

**Wavefront processing across segments.** Entry point offsets are relative to
the segment, so a slice beginning part-way down the picture indexes them from its
own first row. The snapshot taken after the second CTB of a row lives in the
slice state, so it survives a dependent segment boundary — and the row-start sync
is gated on availability, so the first row of an *independent* slice starts from
initial contexts instead of inheriting the previous slice's snapshot.

One bug worth recording, because it is the kind that hides: the code had a single
`wpp` flag meaning both "wavefront rules apply" and "this segment has
substreams". Those differ. A dependent segment holding one row needs the context
rules — snapshot, and sync at the row start — but carries no entry point offsets,
so with the flags conflated it took no snapshot at all, and the next segment
synced from a two-rows-stale one. It decoded 20 CTBs into a 16-CTB picture.

Two pieces of encoder reality had to be accommodated:

- kvazaar writes an entry point offset for every CTB row of the **picture** into
  the first slice segment, even when the following rows live in their own
  dependent segments. Offsets describing data the segment does not contain are
  now ignored rather than rejected; a segment that really runs out of substreams
  still says so.
- x265 `--slices 2` on a picture too small to split emits a second slice NAL with
  a header and no payload. That is refused with "slice segment at CTB n carries
  no data"; ffmpeg rejects the same stream with "Overread slice header by 5 bits".

Measured, all against FFmpeg: `kvazaar --slices wpp` at 128x128 and 256x256, the
same with deblocking on, and `x265 --wpp --slices 2` — every one bit-exact, with
no regression across the 22 tiled and non-tiled streams from 0.13. Three
mutations confirm the rules are load-bearing: sharing slice state across a slice
boundary breaks the x265 vector (24 548 samples), clearing availability at a
dependent boundary breaks the kvazaar ones (12 286–73 920), and treating a
dependent boundary as a slice boundary for the loop filters breaks the deblocked
vector (104). Three golden vectors came with it, regenerated by
`tools/gen_slice_bitstreams.sh`.

Still missing: a dependent segment that begins **mid-row**, which is the one path
the resume-from-stored-contexts branch exists for and which neither encoder here
emits. And tiles combined with WPP, which no profile permits.

### 0.15 Refusing what cannot be reconstructed (S) — **fixed**

Only zero-motion skip CUs are reconstructed, and a P or B picture with real
motion used to decode **without any error at all**: `cu_skip_flag` came back 0,
the CU was then parsed as if it were intra, and the result was a picture that
looked plausible and was wrong — 62 to 90 % of bytes off, growing frame by frame.
A caller could not tell that apart from a successful decode, which makes any
differential check built on this decoder untrustworthy for inter content.

Three things landed:

- **`pred_mode_flag`** is parsed for a non-skipped CU in a P or B slice, and an
  inter CU errors with its position: "inter CU at (304,120): motion compensation
  is not implemented, only zero-motion skip CUs decode".
- **`cu_skip_flag`'s context** now counts how many of the left and above
  neighbours were themselves skipped (spec 9.3.4.2.2), from a per-minimum-CB map
  that reads unavailable exactly where spec 6.4.1 says a neighbour is — undecoded,
  or in another slice or tile. It used to ask whether those positions were inside
  the *picture*, which agrees with the spec only for all-skip content in a single
  slice with no tiles. That is precisely what the P-frame tests contained, so
  nothing caught it.
- **`pred_weight_table`** is parsed rather than skipped, and
  **`ref_pic_lists_modification`** is refused when `NumPocTotalCurr` says it is
  present — which meant counting `NumPocTotalCurr` from the reference picture
  set, inline or taken from the SPS.

The weighted prediction gap was the surprise. x265 enables it **by default**, so
every P slice from default settings carries the table; skipping it left the
reader mid-header and the entry point offsets that follow read as garbage —
71713 into a 482-byte payload. `testdata/hevc_1idr_1p.mp4`, a real 720p encode
committed to this repo, has never decoded past its first picture for that reason.
Its IDR is bit-exact against FFmpeg; its P picture is now refused at the inter CU
at (304,120), which is also the proof that the header parses. **The 0.10 table
below claims that file decodes "exact": that was only ever true of its first
picture.**

A stream that signals no actual weight decodes on, since default weights predict
the plain reference sample, which is what a zero-motion skip copy produces.

Two regression tests: the library refuses a small committed kvazaar P-frame
vector, and the CLI refuses that mp4 while decoding its first picture with
`-n 1`, asserting the CU position so a header misparse cannot pass as the same
failure.

### 0.16 Encoder-side tiles (M) — **fixed**

`pkg/encode` refused `tiles_enabled_flag` outright, so the two things the tools
exist for could not be done to a tiled stream: no gray CRA splice for GDR, no
`hi265-mp4-extend` freeze, and no way to generate a tiled test vector in-repo.

Every path now emits one independent slice segment per tile — the shape that
needs no entry point offsets, since CABAC initialises and terminates at a segment
boundary anyway. A `segment` value carries which tile a slice is writing, and
each writer bounds its CTB loop by that region and terminates there. That covers
`EncodeGrayIDRSliceFromSPSPPS`, `EncodeGrayCRASliceFromSPSPPS`,
`EncodePSkipSliceFromSPSPPS`, `EncodeIDRSliceFromSPSPPS` and
`EncodeCRASliceFromSPSPPS`, plus the generated parameter sets: `EncodeParams`
gained `TileCols`/`TileRows`, the PPS writer emits the tile syntax with
`loop_filter_across_tiles_enabled_flag = 0`, and `hi265gen -tiles CxR` exposes
it. The API shape did not change: several NALUs concatenated are still one
Annex-B stream, and `avc.ConvertByteStreamToNaluSample` splits them correctly for
the MP4 path.

Two things had to match the decoder exactly, and one of them was already wrong:

- **`cu_skip_flag`'s context** must stop counting neighbours at the tile edge,
  since the decoder resets availability there.
- **Reference sample availability.** `pkg/encode` kept its own copy of
  `buildRefSamples`, still comparing raster CTB addresses — the rule the decoder
  dropped in 0.13. So the first CU of every tile after the first predicted from
  the neighbouring tile's samples: from a *fresh* encoder frame those read as 0,
  the residual was coded against a prediction of 0, and the decoder — correctly
  substituting 128 — reconstructed 255 where the source was 235. A flat grey
  picture came out with three of its four tiles saturated, while both decoders
  agreed with each other, which is exactly the failure mode a duplicated
  implementation produces. The two now share `internal/pred.BuildRefSamples`,
  1907 bytes of duplication deleted.

Measured: generated tiled pictures at 2x2, 4x1 and 1x8 decode bit-exactly in
FFmpeg and in `pkg/decoder`, and match their source pattern to within the same
±2 as the untiled encode. Gray IDR, gray CRA and P-skip against a tiled external
PPS decode to uniform mid-grey, with the P-skip picture identical to the IDR
before it.

**What this surfaced: WPP emission.** A PPS with
`entropy_coding_sync_enabled_flag` set — x265's default — needs one substream per
CTB row with entry point offsets to match. The gray encoder ignored the flag and
emitted a single continuous substream. FFmpeg accepted such a stream without a
word and decoded it to garbage: 73 % of a 1280x720 case came out zeros instead of
mid-grey. Only byte lengths were ever asserted, in tests whose comment claimed
they had been verified pixel-perfect. Those parameter sets were refused here, and
the affected tests changed to assert the refusal; 0.17 implements the emission
and turns them back into content checks.

### 0.17 Encoder-side WPP (M) — **fixed**

`pkg/encode` refused `entropy_coding_sync_enabled_flag`, which is x265's
default. So the two things the tools exist for could not be done to a
default-settings encode: no gray CRA splice for GDR, no `hi265-mp4-extend`
freeze. 0.16 left this as the biggest gap in the encoder, and it was.

The slice data is now emitted as one CABAC substream per CTB row, with the
entry point offsets to reach them. `segment.encodeCtbs` owns the whole CTB loop
for all three writers — gray, grid IDR/CRA and P-skip — so the substream
splitting and the context synchronisation live in one place instead of three:
at the end of a row it writes `end_of_subset_one_bit`, closes the substream on a
byte boundary and starts a fresh arithmetic coder seeded from the snapshot taken
after the second CTB of the row above (spec 9.3.1), falling back to initial
values where there is no second CTB to snapshot. `writeEntryPointOffsets` writes
their lengths, which forced the header to be written *after* the data it
describes. `EncodeParams` gained `WPP`, the PPS writer emits the flag, and
`hi265gen -wpp` exposes it. Tiles with WPP is refused on both sides now — no
HEVC profile permits it.

**One defect, and it took FFmpeg to see it.** The terminating bin's flush
already ends in a forced one bit; spec 9.3.4.3.5's note says that bit is the
`rbsp_stop_one_bit` when it ends a slice segment, and it is equally the
`alignment_bit_equal_to_one` of the `byte_alignment()` that follows
`end_of_subset_one_bit`. Writing a second one bit — the obvious reading of
7.3.2.10 on its own — pushed every substream after the first whose flush landed
on a byte boundary one byte along.

What makes it worth recording is that **no round trip through `pkg/decoder`
could have caught it**: the decoder navigates the slice data by the entry point
offsets alone, so it lands on the right byte whatever the substream ends with.
Encoder and decoder agreed, bit-exactly, on a stream FFmpeg decoded to garbage
from CTB row 4 down. FFmpeg reads a single-slice WPP picture sequentially — take
the terminating bin, realign, carry on — so it only accepts what is byte-exactly
right. Its *threaded* path, which does use the offsets, agreed with us
throughout; the disagreement was visible only with `-threads 1`. That is now
`TestGeneratedWppAgainstFFmpeg`, and reintroducing the extra bit fails six of its
nine cases while every internal round trip still passes.

Measured, all against FFmpeg and bit-exact: generated WPP pictures at 128x128,
16x256 (one CTB column, so every row re-initialises), 32x128, 256x16 (one row,
no substream boundary at all), the ragged 120x72, 8x8 CUs, QP 10 and QP 40, and
an IDR followed by two P-skips. Gray IDR, gray CRA and P-skip against real x265
default-settings parameter sets decode to uniform mid-grey at 256x192, 640x352,
1280x720 and 1920x1080 — the 1280x720 case that used to come out 73 % zeros
included — and `AppendEmptyFrames` now freezes a wavefront stream instead of
refusing it. The gray length goldens for the eight WPP resolutions were rerecorded;
they had been lengths of streams that did not decode.

### 0.18 Encoder-side cu_qp_delta (S) — **fixed**

`pkg/encode` wrote no `cu_qp_delta_abs` at all. A PPS with
`cu_qp_delta_enabled_flag` set — which is what x265 writes whenever rate control
may vary the QP, so every CRF or bitrate-targeted encode — was accepted anyway
and the grid IDR/CRA writer produced a stream missing the element spec 7.3.8.10
codes at the first transform unit of every quantization group that carries a
coefficient. CABAC desynchronises from that point on. Nothing caught it because
the generated parameter sets leave the flag clear and no fixture had it set.

Measured before the fix, on streams the encoder happily produced: a 120x72 case
missed 1514 samples, an 8x8-quantization-group case 24431 of 24576, and one
128x128 case failed outright in `pkg/decoder` rather than merely decoding wrong.

`quantGroups` now tracks the group: `startGroup` clears `IsCuQpDeltaCoded` at
every coding quadtree node at least `Log2MinCuQpDeltaSize` across (spec 7.3.8.4),
after the picture-bounds check because 7.3.8.4 does not recurse into a node
starting outside the picture, and `writeDelta` writes the element between
`cbf_luma` and the residual for the first unit in the group that codes anything.
A nil `*quantGroups` is a PPS with the flag clear, and both methods take a nil
receiver so the call sites stay clean.

**The value written is always zero, and that is not a shortcut.** Every CU this
encoder writes is quantized at `SliceQpY`, so `qPY_PRED` — the average of the QPs
left of and above the group origin, each falling back to the previous group's
(spec 8.6.1) — is `SliceQpY` too, and `QpY = qPY_PRED + 0` is exactly the QP the
residual was quantized with. Spec 9.3.3.10 binarises `cu_qp_delta_abs` as a
truncated Rice prefix, so zero is one context-coded bin and no sign flag. A
non-zero delta would mean choosing a QP per group, which is a rate control
decision this generator does not make; the binarisation for larger magnitudes is
deliberately not written rather than written untested.

Nothing is needed for tiles or wavefront rows, where spec 8.6.1 restarts QP
prediction: `diff_cu_qp_delta_depth` cannot exceed
`log2_diff_max_min_luma_coding_block_size`, so a group is never larger than a CTB
and every CTB begins one anyway. The gray and P-skip writers need nothing either,
and now say why: a gray picture has every `cbf` clear and a skipped CU has no
transform tree, so neither ever reaches a transform unit that codes the element.
`testdata/slices_wpp_2slices_256x128.265` has the flag set, so the gray and
P-skip tests were already covering that incidentally; `TestGrayAndPSkipNeedNoCuQpDelta`
now states it.

Measured after: quantization groups of 16x16 and of 8x8, at minimum CB sizes 8
and 16, with a short bottom CTB row and a narrow right CTB column, at QP 10, 26
and 40 — all bit-exact against FFmpeg — plus `testdata/cuqp_crf32_128x128.265`,
a real x265 CRF-32 encode at a 16x16 CTU, for parameter sets this repo did not
write itself. Removing the emission again fails all seven pattern cases, four of
the eight FFmpeg cases and the real-PPS case.

**What this did not fix, found while testing it.** Two other things an external
PPS can ask for and this encoder still ignores rather than refuses:
`sign_data_hiding_enabled_flag`, which x265 sets by default and which changes how
the last sign of a coefficient group is coded, and `pps_cb_qp_offset` /
`pps_cr_qp_offset`, which the chroma path never adds to the luma QP. A
default-settings x265 PPS therefore still cannot drive the grid IDR writer, and
says so only for `weighted_pred_flag`, which is refused. The fixture above has
sign hiding switched off for exactly this reason.

And separately, **the grid entry points take a frame with the wrong stride when
the picture width is not a multiple of 16.** `yuv.BuildFrame` lays a grid out at
`grid.Width*16` samples per row and `EncodeIDRSliceFromSPSPPS` then resets the
frame's declared width to the SPS's, after which the encoder walks the source
planes at the narrower stride. Measured with `hi265gen -smpte` against its own raw
output: 128x80 and 112x48 match to within ±1, 128x72 (a short bottom CTB row, which
strides correctly) also ±1, while 120x80 differs on 14050 of 14400 samples at max
delta 177 and 120x72 on 12602 of 12960. Only the width matters. This is why the
narrow-right-column case above is checked against FFmpeg but not against the
pattern it was built from.

### 0.19 A picture width that is not a multiple of the CTU (S) — **fixed**

Found while testing 0.18, and the only item on this list that was a live defect
in a documented flag rather than a missing feature. `hi265gen -w 120` coded the
wrong samples, silently.

A grid cell is one CTU, so `yuv.BuildFrame` lays a grid out at `grid.Width*16`
samples per row. `GenerateIDR`, `EncodeIDRSliceFromSPSPPS` and
`EncodeCRASliceFromSPSPPS` then set the frame's `Width` to the picture's and
handed the *planes* straight to the slice writers, which index them as
`src[y*width+x]`. For any picture whose width is not a multiple of 16 the buffer
is wider than that, so every row was read one CTU remainder further along than the
one before: the encoder coded a picture sheared sideways, faithfully.

`frame.Frame` already draws the distinction the bug ignored — `Width` is the
visible picture, `StrideY` the buffer — and `YUV420Bytes` crops on it, which is
why the raw `.yuv` output was right all along and only the `.265` was wrong.
`gridSource` now does that same crop once at each entry point and returns packed
planes, so the two agree by construction. A grid too small for the picture is an
error rather than a read past the buffer.

Measured with `hi265gen -smpte` against the generator's own raw output, before
and after:

| size | before | after |
|---|---|---|
| 120x72 | 12602 of 12960 differ, max delta 177 | 456, max delta 1 |
| 120x80 | 14050 of 14400 differ, max delta 177 | 480, max delta 1 |
| 128x72 | 568, max delta 1 | unchanged |
| 128x80 | 576, max delta 1 | unchanged |

Only the width was ever affected: a short bottom CTB row strides correctly, which
is why 1920x1080 and 640x360 — ragged in height, aligned in width — were fine and
nothing caught this. Widths that are multiples of 16 are byte-identical to before.

Four conformance cases now cover it (`tiles_ragged_width` 120x72,
`smpte_ragged_width` 200x104, `tiles_ragged_width_tiny` 24x24 and
`smpte_ragged_width_pskip`), and they had to use a pattern that varies
horizontally: the two decoders agree on a sheared picture, so only the check
against the intended pattern catches it, and a flat pattern shears invisibly.
`TestGridSourceRepacksToPictureWidth` pins the repacking itself against the
frame's strided layout. Restoring the old behaviour fails exactly those four
conformance cases and four of the five unit cases.

### 0.20 Encoder-side sign data hiding (M) — **fixed**

`pkg/encode` ignored `sign_data_hiding_enabled_flag`, which x265 sets by default.
It was the last thing standing between the grid IDR/CRA writer and a
default-settings x265 PPS, and 0.18 recorded it while implementing `cu_qp_delta`.

Where a sub-block's significant coefficients span more than three scan positions,
spec 7.3.8.11 codes no `coeff_sign_flag` for the lowest-frequency one: the decoder
takes that sign from the parity of the sub-block's absolute levels, an odd sum
meaning negative. The encoder wrote the sign anyway, so the decoder read a bin
that was never coded for it and everything after was misaligned.

Two halves, and both are needed. `encodeResidualCoding` now skips the sign, and
`hideSignParity` makes the parity say what the sign was — moving one level by a
step when it does not already. It only ever *increases* a magnitude: by then every
`sig_coeff_flag` of the sub-block has been written, and a decrement could reach
zero and leave the significance map, `firstScanPos`, `lastScanPosInSb` and hence
the decision to hide a sign at all disagreeing with what was coded. Which
coefficient it picks hardly matters for distortion — every coefficient of a
transform block shares one quantizer step here, so a step on any single one costs
the same, and the dead-zone quantizer biases levels low so increasing tends to
move toward the unquantized value. HM does a rate-distortion search instead, which
would need the pre-quantization coefficients threaded into the residual writer for
a gain of at most one step on one coefficient.

The adjustment happens *inside* `encodeResidualCoding`, which now modifies its
`levels` argument in place, because the caller reconstructs from that same slice
straight afterwards. Reconstructing from the levels as handed in would predict
later blocks from samples no decoder will ever have.

Measured, before and after, on the two new fixtures with the two patterns dense
enough to reach the path:

| | honoured | ignored (before) |
|---|---|---|
| `patDiagonal` | max delta 3, mean 0.14 | max delta 217, mean 11.7 |
| `patSMPTEText` | max delta 5, mean 0.34 | max delta 220, mean 35.6 |

and against the constant-QP fixture the desynchronisation was bad enough that the
slice terminated early and `pkg/decoder` refused the picture outright — "covered
by 20 CTBs, expected 72". All four combinations are now bit-exact against FFmpeg.

**Most of what this generator makes cannot reach sign hiding**, which is worth
recording because it is what made the gap invisible and what would make a test
vacuous. A flat CTU predicted from a uniform edge leaves a constant residual, and
the transform of a constant is one DC coefficient, so the span is zero. Measured
over every pattern, QP 4 to 40: `patFlat`, `patTiles` and `patSMPTE` never exceed
a span of 0; `patSMPTEText` and `patDiagonal` reach 9 with a 16x16 CU and only 3
with 8x8 CUs. So sign hiding fires only on those two patterns at `minCb` 16 — 32
and 36 sub-blocks respectively, of which 18 and 20 need a parity adjustment.
`TestSignHidingFiresOnlyOnDenseContent` asserts both directions, so the tests
cannot quietly stop exercising the feature.

**And a note on what the two-decoder comparison cannot see.** Omitting the right
sign but leaving the parity wrong is a well-formed bitstream: FFmpeg and
`pkg/decoder` read it identically, both arriving at the same wrong sign, and the
FFmpeg comparison passes. Only the check against the source pattern fails.
Verified by breaking exactly that. This is the third time in this stretch of work
that agreement between two decoders was not enough — the WPP byte alignment
(0.17) and the ragged-width shear (0.19) were the others.

### 0.21 Chroma QP offsets (S) — **fixed**

`pps_cb_qp_offset` and `pps_cr_qp_offset` were ignored, and this turned out to be
a **decoder** defect as much as the encoder gap 0.18 recorded. Spec 8.6.1 adds
those offsets, plus the slice-level pair, to the luma QP before Table 8-10 maps it
to a chroma QP; spec 8.7.2.5.5 folds the picture-level pair into the chroma
deblocking edge as well. Every chroma QP in the codebase came from the luma QP
alone, and the slice-level offsets were read out of the header and thrown away.

Both sides agreed with each other, so nothing looked wrong until a stream with a
non-zero offset was decoded against FFmpeg. On an x265 vector with
`--cbqpoffs 6 --crqpoffs -6` the luma plane was perfect and **every single chroma
sample was wrong**, by up to 71. With `--cbqpoffs 12 --crqpoffs 12`, up to 56.

`transform.ChromaQP(qpY, offset)` is now the one derivation: add, clip to the
range Table 8-10 is indexed over, map. The clip matters — the picture and slice
offsets together reach ±24, which without it indexes past the table and, at the
bottom, goes negative. `ChromaQPOffsets` carries the pair, with `For(comp)`
picking a component's, since getting that backwards is invisible whenever the two
are equal, which is most streams. The decoder applies the combined offsets when
scaling a residual and the picture-level pair when deblocking; the encoder applies
the picture-level pair, writing the slice-level ones as zero.

**Deblocking needed restructuring, not just an offset.** It computed one `tC` and
filtered both components with it. That is only right while the two offsets are
equal: spec 8.7.2.5.5 puts `pps_cb_qp_offset` in the Cb edge's QP and
`pps_cr_qp_offset` in the Cr edge's, so `tC` is now derived inside the per-
component loop.

Also refused rather than ignored, on both sides:
`chroma_qp_offset_list_enabled_flag`. A per-CU offset needs
`cu_chroma_qp_offset_flag` and `cu_chroma_qp_offset_idx` in the transform unit
(spec 7.3.8.10), which neither side reads or writes, so decoding past them would
produce exactly the plausible-looking wrong picture this entry is about.

Measured after: four x265 vectors with offsets of 6/−6, −5/4 and 12/12, with
deblocking on, decode bit-exactly against FFmpeg; the grid encoder writing against
those same parameter sets is bit-exact too, and lands within 4 of its source
pattern where ignoring the offsets put it 39 to 115 away.

**Two tests, two blind spots, and they are opposite.** An encoder-only error —
quantizing chroma at a QP the decoder will not use — leaves a syntactically
perfect bitstream, so FFmpeg and `pkg/decoder` agree bit for bit on the wrong
picture and only the comparison against the source catches it. Break the shared
derivation instead and both our sides go wrong together, the round trip through
our own decoder reproduces the source exactly, and only FFmpeg notices. Both
directions were verified by breaking them. That makes four items in this stretch
where agreement between two decoders was not sufficient — after the WPP byte
alignment (0.17), the ragged-width shear (0.19) and the sign-hiding parity (0.20)
— and it is the first where the *source* comparison is the one with the blind
spot.

### 0.22 Scaling lists, and refusing what is left (M) — **fixed**

Prompted by a question — what is missing for decoding IDR and CRA pictures as
thumbnails — which turned into an audit of what the decoder reads from the
parameter sets at all. It never looked at `bit_depth_luma_minus8`,
`chroma_format_idc`, `scaling_list_enabled_flag`, `pcm_enabled_flag` or
`transquant_bypass_enabled_flag`. Measured, on x265 streams built to use each:

| tool | before | after |
|---|---|---|
| scaling lists, QP 12 | 2870 of 49152 samples wrong, max delta 2 | bit-exact |
| scaling lists, QP 22 | 102 wrong, max delta 2 | bit-exact |
| `transquant_bypass` (`--lossless`) | 12254 of 12288 wrong, max delta 217 | refused |
| 10-bit (Main 10) | 4682 wrong, max delta 3 | refused |

The two extremes are both dangerous in their own way. Lossless produced obvious
noise, but still returned a frame. A 10-bit stream came out **three values off** —
a picture that looks entirely correct, which for a thumbnail is worse than an
error, because nothing downstream would ever question it.

**Scaling lists are now supported**, for the default matrices. `scaling_list_data`
that carries explicit weights is refused instead: mp4ff skips past that syntax
without keeping the values, so reading them means parsing the SPS or PPS a second
time here, and switching the lists on and taking the defaults is what an encoder
does when simply asked for them. `transform.DefaultScalingLists` builds Table 7-6
once and shares it; `Dequantize` takes the matrix, or nil for the flat 16 that
costs no lookup. Note why this hid so well: the default 4x4 matrix **is** flat, so
a picture coded entirely in small transform blocks decodes identically either way,
and the first stream tried had exactly that shape and passed.

Everything else the decoder cannot do is now named rather than decoded past:
bit depth, chroma format, `separate_colour_plane_flag`, PCM, transquant bypass,
explicit scaling lists, and the range extension tools that change how
`residual_coding` is parsed — `persistent_rice_adaptation`,
`cabac_bypass_alignment`, `implicit_rdpcm`, `explicit_rdpcm`,
`extended_precision_processing` and `cross_component_prediction`. Motion
compensation was already refused as of 0.15. **The decoder now either decodes a
stream correctly or says which tool stopped it.**

The encoder gained the matching refusals, and they differ per writer, which is the
interesting part. The gray writer codes no coefficients and no residual, so bit
depth, chroma format, scaling lists and the chroma QP offsets cannot reach it —
that is the whole point of it, and it still accepts any of them. What does reach
it is syntax in the coding unit itself: `cu_transquant_bypass_flag` precedes
`cu_skip_flag` for every CU (spec 7.3.8.5) and `pcm_flag` sits in the intra one, so
both are refused for every writer including gray. Scaling lists, bit depth, chroma
format and the residual-coding extensions are refused only where residuals are
written, which is the grid IDR and CRA path.

Fixtures: `testdata/scalinglist_q12_256x128.265` with its golden, coded at QP 12
on purpose, since a coarse quantizer leaves too few large-block coefficients for
the weights to matter — QP 32 passes with the lists ignored.

### 0.10 Real-world content decoding (L) — **fixed**

x265 output was not bit-exact, and got worse with detail and scale: a 720p
`testsrc2` frame at x265's defaults had 1379837 of 1382400 samples differing at
max 245, and rate-controlled streams panicked on a negative QP. Six defects plus
one missing derivation, none of them visible to the existing tests — the
generator emits flat colours, three intra modes and one transform size, while a
real encoder uses everything.

1. **Chroma reference samples were filtered.** Spec 8.4.4.2.3 invokes the
   smoothing filter for luma only (or 4:4:4 chroma, unsupported here). The
   DC/horizontal/vertical modes a flat-colour generator picks are exempt either
   way, so only planar and diagonal chroma showed it.
2. **The chroma residual of a 4x4 luma group was parsed at the first transform
   unit.** Spec 7.3.8.10 codes it at `blkIdx == 3`, the last one, for the area at
   (xBase, yBase). Only 4x4 luma TBs reach this path, which the generator never
   produces.
3. **The 4x4 `sig_coeff_flag` context map was indexed by scan order.** Spec
   9.3.4.2.5 has one position map (Table 9-42) with no scan dependence. The
   decoded bins often still came out right and only the arithmetic decoder's
   state diverged, so the damage appeared bins later — the hardest of the six to
   localise.
4. **`IntraPredModeC` came from the transform block's own PU.** Spec 8.4.3
   derives it from `IntraPredModeY` at the CU's top-left; with an NxN partition
   the four luma PUs share one chroma block. Every quadrant but the first got the
   wrong mode, including modes the chroma syntax cannot express — 23 from a table
   of {0, 26, 10, 1} was the giveaway.
5. **Chroma deblocking filtered four samples per call** while the caller walked
   the 4x4 luma grid, two chroma samples apart, so every chroma line was filtered
   twice — the second time from already-filtered input.
6. **The luma strong/normal filter decision** tested `(dp+dq) < beta >> 2` where
   spec 8.7.2.5.6 tests `2*(dp+dq)`, choosing the bilinear filter where the
   normal one belongs.

And the **QP prediction of spec 8.6.1**, which was a single running value. That
models a quantization group equal to the CTB but not a smaller one, where the
prediction averages the QPs left of and above the group origin and falls back to
the previous group only where those are unavailable or in another CTB. x265
defaults to a 32x32 group with a 64x64 CTB, so every rate-controlled encode
drifted — far enough to derive a negative QP and panic.

Verified against FFmpeg:

| case | before | after |
|---|---|---|
| sweep: 3 contents x 5 rate modes x 4 geometries | mostly desync | **60/60 bit-exact** |
| `testsrc2` 720p, x265 full defaults | 1379837 differ, max 245 | exact |
| `testdata/hevc_1idr_1p.mp4` (real 720p encode), first picture | panic | exact (its P picture needs motion compensation — see 0.15) |
| colour bars 640x352 / 640x360 / 1920x1080 | 1311–99760 differ | exact |
| `gradients`, `testsrc2` 128x64, QP 26/34 | 29–1624 differ | exact |

48 of the sweep configurations are now a regression test
(`TestDecodeRealContent`), skipped when x265 or FFmpeg is absent.

What is still out of scope: **P and B frames with real motion** — only
zero-motion skip is implemented, so inter pictures beyond a freeze are far off,
as designed. Worse, such a picture decodes without any error at all; see the
tiles document's T8. Tiles have since landed as far as 0.13.

### 0.4 `hi265dec` argument handling (S) — **fixed**

`go run ./cmd/hi265dec in.265 -o out.yuv` silently ignored `-o` — Go's `flag`
stops at the first positional. Flags and positionals are now parsed interleaved,
and the output may be given either with `-o` or as a second positional (matching
hi264dec); giving it both ways is an error rather than a silent preference.

---

## Phase 1 — CRA insertion at a chosen POC (the GDR primitive) — **complete**

This is the highest-value new capability and has no H.264 equivalent, so it is
parity and "ahead" at once.

Today the encoder emits exactly two slice NAL types (`pkg/encode/nalu.go:9`):
`IDR_W_RADL` (19) and `TRAIL_R` (1). The decoder dispatch
(`pkg/decoder/decoder.go:71`) handles 19/20 and 0/1. No CRA, no BLA.

Why CRA matters for Gradual Decoder Refresh: a mid-stream CRA derives its POC
MSB normally from `prevTid0Pic`, so with the right `slice_pic_order_cnt_lsb` it
splices into a running stream **without resetting POC**. An IDR cannot do that
— it forces POC to 0 and breaks the POC continuity of everything that follows.

### 1.1 CRA slice encoding (M)

The header machinery already exists — `encodePSkipSliceWithParams`
(`pkg/encode/slice.go:714`) writes precisely the fields an IDR header omits and
a CRA needs. Work:

- add `naluCRA = 21` to `pkg/encode/nalu.go`
- add to `idrSliceParams` / `graySliceParams`: `poc`, `log2MaxPicOrderCntLsb`,
  `numShortTermRefPicSets`, `longTermRefPicsPresent`, `spsTemporalMvpEnabled`
  (all available on mp4ff's `hevc.SPS`)
- in the header, after `no_output_of_prior_pics_flag` (still present — CRA is
  an IRAP): `slice_pic_order_cnt_lsb`, an inline empty short-term RPS
  (`num_negative_pics = 0`, `num_positive_pics = 0`), the long-term RPS when
  `sps.LongTermRefPicsPresentFlag`, then `slice_temporal_mvp_enabled_flag` when
  `sps.SpsTemporalMvpEnabledFlag`
- new exported entry points:
  - `EncodeCRASliceFromSPSPPS(sps, pps, grid, colors, poc)`
  - `EncodeGrayCRASliceFromSPSPPS(sps, pps, poc)`

`hi265gray`'s hardcoded `mpm_idx = 1` stays correct under the 0.1 fix (left is
DC, above forced to DC, so MPM = {0, 1, 26} with DC at index 1), and uniform
gray decodes identically under any mode — so gray output is unaffected by 0.1.

### 1.2 CRA decoding (S)

Add `case hevc.NALU_CRA` (and optionally `NALU_BLA_*`, 16–18) to the decoder
dispatch, parsing the non-IDR IRAP header fields and then decoding as intra.
Treat it as a reference-frame reset for subsequent P-skip frames.

### 1.3 CLI (S)

- `hi265gray -cra -poc N` — the GDR refresh frame, any chroma format / bit depth
- `hi265gen -cra-interval N` — CRA keyframes instead of IDR, POC running
  continuously across the whole sequence

### 1.4 Verification (S)

- `mp4ff-nallister -annexb -c hevc` shows `RAP_CRA_21`
- FFmpeg decodes CRA + trailing empty frames to the intended pixels
- parse the output back with `hevc.ParseSliceHeader` and assert POC continuity
  across the splice
- splice test: generate a stream, inject a gray CRA at POC k, confirm FFmpeg
  decodes from that point with no POC discontinuity warnings

---

## Phase 2 — Time Code SEI — **complete**

### Can we reuse hi264's pic_timing? Partly — and the differences favour hi265.

| piece | reuse |
|---|---|
| SEI *syntax* | **No.** HEVC moved the SMPTE timecode out of pic_timing (SEI 1) into the **Time Code SEI, payload type 136** (D.2.27). hi264's `picTimingMessage` builds `sei.PicTimingAvcSEI`, which does not apply. |
| payload construction | **Already done for us.** mp4ff (already a dependency) ships `sei.TimeCodeSEI` + `sei.ClockTS` with `Payload()` and `DecodeTimeCodeSEI` in `sei/sei136.go`. No bit-level code to write. |
| API shape | **Yes, directly.** Mirror `PicTiming` / `GeneratePicTimingSEI` / `BuildPicTimingSEINALU` / `writeSEIValue` from `hi264/pkg/encode/sei.go` — the payloadType/payloadSize/`rbsp_trailing_bits` wrapper is identical. |
| NALU framing | **Yes.** hi265's `WriteNALU` / `buildNALU` already do the 2-byte HEVC header + EBSP; use NAL type **39** (`PREFIX_SEI`). |
| timecode arithmetic | **Ported, not bumped.** `yuv.Timecode(frame, rate, dropFrame)` (24-hour wrap, drop-frame counting) lives in hi264's `pkg/yuv/format.go`, but the pinned v0.10.0 exports only `FormatText`. Rather than depend on an unreleased hi264, it lives in `pkg/timecode` here, together with a `FormatText` that takes a `dropFrame` argument so overlay and SEI can never disagree. |
| SPS/VUI prerequisite | **Dropped — simpler than hi264.** AVC clock timestamps require `pic_struct_present_flag` in the VUI; SEI 136 has no such dependency, so hi265 needs no SPS change and no `PicStructPresent` config plumbing. |

Verified end-to-end before planning: a hand-built SEI 136 prefix NALU injected
into an unmodified `hi265gen` stream reads back as

```
$ ffprobe -show_entries frame_tags=timecode ...
TAG:timecode=00:00:03:07
side_data_type=SMPTE 12-1 timecode

$ mp4ff-nallister -annexb -c hevc -sei 1 tc.265
Sample 1, pts=0: VPS_32, SPS_33, PPS_34, SEI_39 (11B), RAP_IDR_19
  * SEITimeCodeType (136), size=6, time=00:00:03:07 offset=0
```

### 2.1 `pkg/encode/sei.go` (S)

`TimeCode` struct (H/M/S/Frames/Dropped), `GenerateTimeCodeSEI` (Annex-B) and
`BuildTimeCodeSEINALU` (raw, for MP4 sample assembly), both wrapping
`sei.TimeCodeSEI`. `counting_type` = 1 normally, 4 for NTSC drop-frame.

### 2.2 Generator wiring (M)

- `-timecode` — emit one SEI 136 per picture, for IDR, CRA and P-skip alike
- `-start-frame N` — offset frame counters, timecodes, and for MP4 the `tfdt`
  and fragment `sequence_number`, so independently generated segments
  concatenate into one continuous sequence
- fractional `-fps`: `25`, `30000/1001`, `29.97`/`59.94`/`23.976`; MP4 timescale
  = numerator, sample duration = denominator
- `-drop-frame` — drop-frame counting, valid only at 29.97/59.94

### 2.3 Tests (S)

Assert the ffprobe `timecode` tag per frame, and round-trip through
`sei.DecodeTimeCodeSEI`. Cover 25, 30000/1001 and drop-frame at a minute
boundary.

---

## Phase 3 — `hi265-mp4-extend` — **complete**

hi264's flagship tool has no hi265 equivalent, though the API pieces
(`EncodeIDRSliceFromSPSPPS`, `EncodePSkipSliceFromSPSPPS`,
`EncodeGrayIDRSliceFromSPSPPS`) already exist.

### 3.1 `pkg/encode/extend.go` (M)

Port from `hi264/pkg/encode/extend.go`, with HEVC differences:

- `LastFrameState(annexB) (poc int, nalType hevc.NaluType, err error)` — use
  mp4ff's `hevc.ParseSliceHeader`, which exposes `PicOrderCntLsb` and
  `SliceType`. That parser is the one dependency this tool has on mp4ff beyond
  parameter sets, and it needs **v0.56.0 or newer**: earlier releases miss the
  spec 7.4.7.1 inference for `slice_deblocking_filter_disabled_flag`, so every
  x265 `--no-deblock` stream — `testdata/sincos_128x64.265` included — was
  refused with "alignment bit is not equal to one"
  (`TestAppendEmptyFramesOnRealNoDeblockStream` pins it)
- POC stride is **1** per appended picture (HEVC counts pictures), not AVC's 2
- `AppendEmptyFrames(annexB, count)` — P-skip freeze continuing the source POC
- `AppendRefreshFrames(annexB, count, mode)` with `mode ∈ {PSkip, GrayCRA,
  BlackIDR}` — the CRA mode is the one hi264 structurally cannot offer

### 3.2 CLI (M)

Mirror `hi264-mp4-extend`: `-frames N`, `-gray-cra` / `-black-idr`, positional
`<init.mp4> <in.m4s> <out.m4s>`. Read parameter sets from the init segment's
`hvcC`, keep per-sample duration, mark CRA/IDR samples as sync samples.

### 3.3 Tests (S)

Extend a real segment, verify `cat init.mp4 out.m4s | ffmpeg` decodes the full
sample count, and that the appended span is pixel-identical to the last source
frame (freeze) or to gray (CRA refresh).

---

## Phase 4 — CLI parity

### 4.1 `hi265dec` (M) — **done**, except `-no-deblock`

- MP4 / `.m4v` / `.m4s` input via mp4ff, progressive and fragmented, parameter
  sets from `hvcC`. Samples decode in order rather than jumping between sync
  samples, so P-skip frames resolve against their reference.
- `-n` frame limit; image output writes one numbered file per frame
- `.y4m` and `.jpg` output, plus `-q`, `-colorspace`, `-full-range`, `-version`
- Still to do: `-no-deblock`

Real-world MP4 input is blocked by 0.9 (WPP), not by the MP4 plumbing.

### 4.2 `hi265gen` (M)

- PNG/JPEG image as background, downsampled to block resolution
- `cmd/hi265psnr` (or reuse hi264's `rawpsnr`) — there is an untracked `psnr`
  binary in the repo root but no committed command

### 4.3 8x8 coding granularity (M) — **done**

Parity with hi264's `-8x8` / `@8x8`: four characters per 16x16 block instead of
one, so grid patterns and digits get real spatial detail. Today
`encodeIDRCU` hardcodes a single 16x16 luma TU and an 8x8 chroma TU per CTU
(`pkg/encode/slice.go:252` and `:275`).

Most of the machinery is already size-generic: `encodeResidualCoding` takes
`log2TrafoSize`, and `forwardDCT`/`quantize`/`getDCTMatrix` cover 4/8/16/32.
Two routes to four 8x8 blocks per CTU:

- **Route A — `split_cu_flag`, `minCb = 8`** (recommended). SPS
  `log2_min_luma_coding_block_size_minus3 = 0`, CTU stays 16. Write
  `split_cu_flag = 1` at depth 0; the four children are ordinary 2Nx2N CUs, each
  with its own intra mode, one 8x8 luma TB and 4x4 chroma TBs. The existing
  per-CU encoder generalises by parameterising size — no `part_mode` NxN path,
  no `IntraSplitFlag` handling. The IDR path needs the `split_cu_flag` writer
  that `encodePSkipSliceData` (`:841`) already has, and the P-skip path keeps
  working unchanged (`split_cu_flag = 0` at depth 0 gives one 16x16 skip CU).
- **Route B — `PART_NxN` at a 16x16 CU.** Keeps `minCb = 16`; `IntraSplitFlag`
  then forces the transform split, yielding the same TB structure (four 8x8
  luma, four 4x4 chroma) under one CU header. More encoder-side special-casing
  for equivalent output.

The one genuinely new piece either way: **mode-dependent scan derivation**.
`scanIdx` is hardcoded to 0 at both call sites, which is correct only while
luma TBs are 16x16 and chroma TBs are 8x8. Spec 7.4.9.11 makes the scan
mode-dependent for `log2TrafoSize == 2` (both components) and for
`log2TrafoSize == 3` when `cIdx == 0` — so 8x8 luma *and* 4x4 chroma both need
it: modes 6–14 → vertical (2), 22–30 → horizontal (1), else diagonal. Mind the
two traps already recorded for the decoder: the `last_sig_coeff_x`/`y` swap for
vertical scan, and that this applies to 8x8 luma, not just 4x4.

Useful synergy with 0.1: with four 8x8 CUs inside a 16x16 CTB, the top two CUs
must force `candB = INTRA_DC` while the bottom two legitimately use the above
CU's mode. That makes an 8x8 pattern the sharpest golden test for the MPM fix —
it exercises both sides of the branch in one frame, which no current test does.

**Landed** as Route A behind `-8x8` / `Use8x8CU`, with the 16x16 default path
byte-identical (pinned by a digest test). FFmpeg agrees bit-exactly at QP
20/26/30/34/40, with P-skip frames, and through external SPS/PPS.

Doing it surfaced four more conformance bugs, all now fixed:

- mode-dependent `scanIdx` derivation covered only 4x4 luma. Extended to 8x8
  luma and 4x4 chroma (the latter from `IntraPredModeC`), **and the mapping was
  inverted** — modes 6–14 select vertical, 22–30 horizontal.
- the vertical-scan `last_sig_coeff` x/y swap was missing on both sides.
- the TB's luma mode indexed the per-PU array unconditionally, reading mode 0
  from unset entries of a 2Nx2N CU.
- decoder MPM `candModeList[1]` computed `2+((candA-2+29)%32)`; spec 8.4.2 says
  `2+((candA+29)%32)`. Two steps off whenever both neighbours predict the same
  angular mode, which four 8x8 CUs per CTB makes common. The encoder already had
  it right, so this was a straight encoder/decoder mismatch.

Instrumenting all 18 golden streams found exactly **one** 4x4 block that takes a
mode-dependent scan, which is why none of this was constrained before.

Still to do: `-8x8` changes the coding structure, not the content. Wiring 8x8
*pixels* through needs `PlaneGrid` threaded into `GenerateIDR` and all four raw
output paths, or `-8x8` would mean different pixels for `.265` than for `.yuv`.
The vendored hi264 does support the `@8x8` gridimg directive, so the helpers are
there.

---

## Phase 5 — Ahead of hi264

hi264 is permanently 8-bit 4:2:0; these are the items HEVC lets us take
further.

### 5.1 High bit depth / non-4:2:0 decoding (L)

`hi265gray` already generates 4:2:0/4:2:2/4:4:4 at 8/10/12-bit, but the decoder
is 8-bit 4:2:0 only, so we cannot verify our own output without FFmpeg. Widen
`frame.Frame` planes to `uint16`, thread `bitDepth` through prediction,
transform, deblocking and SAO. This is the single biggest differentiator.

### 5.2 High bit depth generation beyond gray (M)

`gray.go`'s CU tree is already chroma-format and bit-depth agnostic. Generalise
it from "uniform gray" to "flat colour per CTU" and the grid generator works at
10-bit 4:2:2 — a class of test content hi264 cannot produce at all.

### 5.3 Stream structure niceties (S each)

- AUD (35), EOS (36), EOB (37) emission — useful for splicing conformance
- `-cra-interval` combined with `-idr-interval` for mixed IRAP patterns
- Recovery-point-style SEI for GDR bootstrapping documentation

### 5.4 Test-coverage parity (M)

hi264 has 41+ golden cases, hi265 has 16. The Phase 0.2 harness is the vehicle:
add cases per feature (CRA, timecode, extend, bit depths) as those land.

---

## Phase 6 — Tile stitching (`hi265retile`) — **complete**

`hevc-retiler` was a separate repository next to this one. It is now `pkg/retile`
and `cmd/hi265retile`, moved with no behaviour change: four reference stitches —
2x1 intra, 2x1 P-frame, 1x2 non-uniform, 2x2 — are byte-identical to what the
standalone tool produced from the same inputs.

Three things made it belong here rather than beside. The low-level primitives
were split across the repository boundary: `encode.InsertEBSP` here and
`RemoveEmulationPrevention` there are exact inverses, `encode.BitWriter` here and
a bit reader there are counterparts. Both now sit next to each other in
`pkg/encode`. `pkg/decoder` decodes exactly the tiling shape the stitcher emits
as of 0.13, so verification became a same-repo API relationship rather than a
subprocess. And at ~900 lines it did not earn its own CI, changelog and release
tags, next to a repo that already hosts a bitstream-editing CLI of the same
species in `hi265-mp4-extend`.

### 6.1 The move (M) — **done**

`pkg/retile` holds the SPS/PPS bit-splice and the slice-header rewrite;
`pkg/encode` gained `BitReader` and `RemoveEmulationPrevention`; `cmd/inspect`
became `hi265inspect`, which does what `spsdump` does but on a file given as an
argument. The old `anb.Split` is gone in favour of the same
`avc.ExtractNalusFromByteStream` that `pkg/decoder` already uses, trimming the
trailing zeros mp4ff leaves attached to the last NAL — a slice payload is copied
verbatim, so stray bytes would land inside the restitched NAL.

The mp4ff `replace` directive the old repo needed is gone with it: it pointed at
master for the slice-header inference fixes of Eyevinn/mp4ff#558, which shipped
in v0.56.0 — the version this repo already required. Do not lower it.

### 6.2 An API, and refusing what cannot tile (M) — **done**

The stitching lived in the CLI's `main()`, so it could only be tested by running
the binary. It is `retile.Stitch` now, and the gaps in what it accepted became
visible as soon as it was testable. Each of these produced a structurally valid
stream that decoded to the wrong picture, silently:

| shape | why it cannot work |
|---|---|
| coding tools differing between inputs | one merged SPS/PPS describes every tile, so some tile is decoded with another's tools |
| a differing `init_qp_minus26` | a slice QP is a delta from it and those bits are copied verbatim |
| more than one slice segment per picture | the header is copied from just after `slice_pic_parameter_set_id`, which in a later segment is where `dependent_slice_segment_flag` and `slice_segment_address` sit |
| a conformance window | the merged SPS applies one window to the whole picture, so only the last tile row and column would crop |
| two distinct SPSs or PPSs in one input | only the first was kept, and the rest silently ignored |

Only the picture size and the level may legitimately differ per tile. The
refusal names the syntax element it tripped on. A differing VUI is reported
rather than refused, since it changes how the picture is displayed and not how
it decodes.

### 6.3 Verification without ffmpeg (S) — **done**

This is the payoff of 0.13 and the one place the move added behaviour.
`-verify` decodes the stitch and compares every tile against its standalone
decode; it is the only proof that inter inputs really were motion-constrained,
since MCTS appears nowhere in the bitstream. Measured on retiler-produced
streams (2026-08-22), `pkg/decoder` matches ffmpeg byte for byte on 2x1 intra
with deblocking off and on, 2x2 intra at 512x512, and a 1x2 non-uniform
192x64 — and fails on kvazaar P-frames, as it must, since it reconstructs only
zero-motion skip CUs.

So `-decoder` selects: `hi265` in-process, needing no external binary; `ffmpeg`,
the cross-check that would catch a bug shared by the stitcher and
`pkg/decoder`; `auto`, the first falling back to the second. A stitch of inter
pictures makes `auto` reach for ffmpeg and makes `hi265` fail naming the
limitation, rather than report a pass nobody made. Motion compensation is not
part of this phase.

### 6.4 Tests (S) — **done**

The old repo shipped zero Go tests; its whole verification story was `demo.sh`.
Now: digests over four stitches so a change to the bit-splice has to be
deliberate, each tile's crop compared to its standalone decode through
`pkg/decoder` in-process, geometry for uniform and non-uniform grids, and unit
coverage of every refusal. Five committed inputs under `testdata/retile_*`,
regenerated by `tools/gen_retile_bitstreams.sh`. The negative case — a non-MCTS
P-frame stitch that verification must reject — stays in `tools/retile_demo.sh`,
where it needs kvazaar rather than a large committed fixture.

---

## What is left

Everything through Phase 3 is done, plus 4.1, 4.3 and Phase 6. Remaining, smallest first:

- **`hi265dec -no-deblock`** (S) — the only gap left in 4.1.
- **4.2 `hi265gen`** (M) — PNG/JPEG image as background, and a committed PSNR
  command (there is an untracked `psnr` binary in the repo root but no `cmd/`).
- **8x8 *content*** (in 4.3) — `-8x8` changes the coding structure today, not the
  pixels. Wiring 8x8 pixels through needs `PlaneGrid` threaded into
  `GenerateIDR` and all four raw output paths, or `-8x8` would mean different
  pixels for `.265` than for `.yuv`. The vendored hi264 does support the `@8x8`
  gridimg directive, so the helpers exist.
- **`hi265gen -cra-interval`** (in 1.3) — CRA keyframes instead of IDR, with POC
  running continuously. The slice encoding it needs is already there.
- **A dependent slice segment beginning mid-row** (in 0.14) — the resume-from-
  stored-contexts path exists and is spec-shaped, but no encoder to hand emits
  such a stream, so it is untested by any fixture. Everything else about slices
  and tiles is bit-exact.
- **Motion compensation** — P and B pictures with real motion are refused with a
  clear message as of 0.15, rather than decoded wrongly, but they are still out of
  reach. This is the one item that would make hi265 a general decoder rather than
  an intra-plus-freeze one. It is not needed for decoding IRAP pictures as
  thumbnails, which is what 0.22 was done for.
- **Coding tools that are refused rather than supported** (0.22) — PCM,
  transquant bypass (lossless), explicit `scaling_list_data`, and the range
  extension residual-coding tools. Each is a self-contained piece of work now that
  the refusal marks exactly where it would go. Lossless is the most likely to be
  wanted, being screen-content territory.
- **Encoder-side conformance window** (in 0.8) — the decoder applies one; the
  generator still rejects dimensions finer than a multiple of 8.
- **5.1 high bit depth decoding** (L) — the real differentiator, and the one item
  that makes hi265 clearly ahead of hi264 rather than level with it. `hi265gray`
  already generates 4:2:0/4:2:2/4:4:4 at 8/10/12-bit, but the decoder is 8-bit
  4:2:0, so our own output cannot be verified without FFmpeg. As of 0.22 such a
  stream is refused rather than decoded three values off, which makes this the
  largest remaining gap for real-world input: Main 10 is common in anything
  HDR-adjacent, so a thumbnailer meets it early.
- **5.2, 5.3, 5.4** — follow 5.1.

## Suggested order

1. **0.1 + 0.2 + 0.3 + 0.4** — one PR. Ship a trustworthy generator first.
2. **1.1 → 1.4** — CRA at a chosen POC. Answers the GDR need directly.
3. **2.1 → 2.3** — Time Code SEI. Small and self-contained.
4. **3.1 → 3.3** — `hi265-mp4-extend`, now able to append a gray CRA.
5. **4.1 / 4.2** — CLI parity, mostly mechanical.
6. **5.1** — high bit depth decoding, the real "ahead" item.

**4.3 (8x8) is independent of 1–3** and can be pulled forward to right after
Phase 0 whenever finer patterns matter more than CRA/timecode — it needs only
the scan derivation plus quadtree plumbing, and it doubles as the best
regression test for 0.1.

Dependencies worth noting up front: Phase 3's CRA mode needs Phase 1 (done);
Phase 5.2 is much easier once 5.1 gives a decoder that can verify the output.
Phase 2 needed `yuv.Timecode`, which the pinned hi264 v0.10.0 does not export —
resolved by porting it into `pkg/timecode` rather than depending on an unreleased
hi264.
