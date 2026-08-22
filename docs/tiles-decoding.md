# Tiles decoding in `pkg/decoder`

**Status:** T1–T6 and T8 implemented; T7's remaining fixtures outstanding.
Both tiling shapes — one slice segment per tile, and every tile in one segment —
decode bit-exactly, loop filters included. Sizes use the roadmap's scale —
**S** ≈ half a day, **M** ≈ 1–2 days, **L** ≈ 3+ days.

This is the tiles half of roadmap 0.9. That entry fixed slice-header *parsing*
for streams with `tiles_enabled_flag` or `entropy_coding_sync_enabled_flag` set,
and then implemented WPP. Tiles decoding was deferred. This document argues for
doing it next, says exactly what it costs, and is honest about what it does and
does not buy.

## Why now: a concrete caller

`../hevc-retiler` stitches N independent HEVC streams into one tiled picture by
bitstream editing — new SPS/PPS, one rewritten slice header per tile, CABAC
payloads copied verbatim, no re-encode. It already depends on hi265 for
`pkg/encode`'s `BitWriter` and `InsertEBSP`.

Its correctness proof is `retile -verify` (`internal/verify/verify.go`): decode
the merged stream and each input, then compare each tile's sub-rectangle against
that input's standalone decode, every frame. A pass is the empirical statement
that the inputs really were tileable. That check shells out to **ffmpeg** — the
project's only external runtime dependency, and the only reason `retile -verify`
cannot run in CI on a bare Go toolchain.

A tiles-capable `pkg/decoder` would replace `decodeYUV420`'s `exec.Command` with
`decoder.New().DecodeAnnexB` plus `frame.YUV420Bytes()`. Both sides are Annex-B
already, so no MP4 plumbing is involved.

Tiles are also worth having on their own account: they are how real 4K/8K and
360-video streams are cut up, so this is the same class of "blocks most
real-world input" that motivated 0.9.

## Measured baseline (2026-08-22)

Everything below was checked against `../hevc-retiler/out`, decoding with
`cmd/hi265dec` and comparing to `ffmpeg -f rawvideo -pix_fmt yuv420p`.

| input | hi265 before T1–T3 |
|---|---|
| `a`, `b`, `q0`–`q3`, `nu00` — x265 all-intra tile *inputs* | **bit-exact vs ffmpeg**, all 5 frames |
| `merged`, `grid`, `pmerged`, `h`, `g` — retiler *merged* streams | `slice header: expected stop bit 1, got 0` |
| `pa`, `p0` — kvazaar MCTS P-frame inputs | **no error**; IDR bit-exact, P frames 40–90 % of bytes wrong |

Two details behind the middle row. The failure is on the **second** slice
segment: truncating `grid.265` to VPS/SPS/PPS plus one slice parses and decodes
fine, so the only slice-header gap is the unread `slice_segment_address`. And
that lone segment emits a whole 512x512 picture in which only tile 0's *first
CTB row* is correct — raster iteration runs off the end of the tile after four
CTBs — so T3 is load-bearing for the first tile too, not just for later ones.

The last row is the reason T8 exists.

## Measured after T1–T6

| stream | result |
|---|---|
| kvazaar `--tiles 2x2 --slices tiles`, both loop filters off | **bit-exact** (128x128, 256x256, ultrafast and veryfast) |
| kvazaar `--tiles 2x1` on 320x256 — 5 CTBs split 2+3 | **bit-exact** |
| kvazaar tiled, SAO on | **bit-exact** (was 169 samples off before T5) |
| kvazaar tiled, deblocking on | **bit-exact** (was 1 972) |
| retiler `merged` (2x1), `grid` (2x2), `h` (1024x256), `nu` (non-uniform) | **bit-exact**, all 5 frames each (was 2 871 – 14 651) |
| retiler all-intra inputs; kvazaar non-tiled controls | **bit-exact** — no regression |
| `pa`, `p0` | **refused** as of T8, naming the first inter CU; they used to decode wrong in silence |

T6 added the other tiling shape, every tile in a single slice segment, and it is
bit-exact across the same range: filters off, deblocking on, SAO on, both on, a
non-equal 2x1 split at 320x256, and a delta-QP map that makes `cu_qp_delta`
live. 22 streams in total decode bit-exactly across both shapes.

And the check the caller actually cares about, computed from hi265 decodes
alone — no ffmpeg anywhere — over `merged` (two tiles) and `grid` (four):

```
grid: tile @0,0 256x256 == q0.265 OK (5 frames)
grid: tile @256,0 256x256 == q1.265 OK (5 frames)
grid: tile @0,256 256x256 == q2.265 OK (5 frames)
grid: tile @256,256 256x256 == q3.265 OK (5 frames)
```

Every tile's sub-rectangle of the merged picture equals that input's standalone
decode, sample for sample. That is `retile -verify`'s entire test, and for
all-intra content it now runs on a bare Go toolchain.

What remains outside the supported set is refused rather than mis-decoded:

```
picture covered by 16 CTBs, expected 64: slice segments do not tile the picture
tiles combined with wavefront parallel processing is not supported
slice segment at CTB 2 carries no data
```

The last of those is x265's own degenerate output when `--slices 2` is asked of a
picture too small to split — ffmpeg rejects that stream too ("Overread slice
header by 5 bits").

## The narrow target

`hevc-retiler` emits a deliberately simple flavour of tiling. Supporting exactly
this is much less work than general tiles, and it is worth being explicit about
where the line falls.

| property | retiler's output | general HEVC |
|---|---|---|
| `tiles_enabled_flag` | 1 | 1 |
| spacing | uniform, or explicit `column_width_minus1` / `row_height_minus1` | same |
| `loop_filter_across_tiles_enabled_flag` | **0** | either |
| slice segments per picture | **one per tile** | any partition |
| `num_entry_point_offsets` | **0** in every slice | > 0 when a slice spans tiles |
| `dependent_slice_segment_flag` | 0 | either |
| `entropy_coding_sync_enabled_flag` | 0 | often 1 |

One slice segment per tile is the load-bearing simplification: CABAC
initialises and terminates at slice-segment boundaries anyway, so there is **no
per-tile context reset to implement inside a slice**, and no substreams to
split. `loop_filter_across_tiles = 0` is what makes each tile edge behave like a
picture edge, which is the whole basis of the stitching being valid.

## What hi265 has today

| piece | state |
|---|---|
| tile geometry in the PPS | **free.** mp4ff parses the whole tile block — `NumTileColumnsMinus1`, `NumTileRowsMinus1`, `UniformSpacingFlag`, `ColumnWidthMinus1`, `RowHeightMinus1`, `LoopFilterAcrossTilesEnabledFlag`. No upstream work needed. |
| `num_entry_point_offsets` in the slice header | **done by 0.9** — `finishSliceHeader` gates on `TilesEnabledFlag \|\| EntropyCodingSyncEnabledFlag`. |
| substream splitting | **done (0.9, generalised by T6)** — `splitSubstreams` splits at the entry point offsets for tiles and for WPP alike; what differs is the reset at each substream's first CTB. |
| spec 6.5.1 scan tables | **done (T2)** — `internal/tiles`, built from the PPS by `tileGrid` (`pkg/decoder/picture.go`). |
| a picture from several slice segments | **done (T1)** — `picture` in `pkg/decoder/picture.go`; `first_slice_segment_in_pic_flag` opens one, the next first-flag or the end of the stream closes it. Segments that do not add up to the whole picture are an error. |
| `dependent_slice_segment_flag`, `slice_segment_address`, `num_extra_slice_header_bits` | **done (T2)** — `parseSegmentAddress`; a dependent segment is refused rather than mis-parsed. |
| CTB iteration | **done (T3)** — `DecodeSliceData` walks tile scan from `SegmentAddressRS` and stops on `end_of_slice_segment_flag`. |
| neighbour availability | **done (T4, T6)** — `buildRefSamples` asks the reconstruction map, which is cleared at every slice segment *and* every tile boundary; the intra mode and CU depth maps reset with it. |
| SAO merge flag gating | **done (T4b)** — gated on same segment and same tile. |
| loop filters | **done (T5)** — `deblock.Apply` and `sao.Apply` run once per picture in `finishPicture` and take an `internal/loopfilter.Boundaries`, which knows each CTB's tile and slice and each slice's filter parameters. |
| inter CUs beyond zero-motion skip | **refused (T8)** — `pred_mode_flag` is parsed and an inter CU that is not a zero-motion skip errors with its position. |

## Work items

### T1 — Assemble a picture from several slice segments (M)

Today each VCL NAL is parsed, reconstructed, filtered, cropped and returned. For
a tiled picture that has to split in two: *decode a slice segment into the
current picture*, and *finish the picture* once the last segment is in.

Use `first_slice_segment_in_pic_flag` as the picture boundary: a set flag starts
a new `frame.Frame`, a clear flag decodes into the one in progress. Finish the
picture when the next first-flag arrives or the NAL stream ends, and only then
run the loop filters, set `d.refFrame`, apply `cropToConformanceWindow` and
append to `frames`.

This also fixes something orthogonal to tiles: multi-slice pictures without
tiles decode as several bogus frames today.

`sd.SaoParams` is already allocated picture-sized, so accumulating SAO
parameters across segments is natural — pass the picture's array in through
`Params` and let every segment index it by raster CTB address.

The other three per-picture maps inside `DecodeSliceData` — `modeMap`,
`depthMap` and `qpState` — must **not** be shared. Spec 6.4.1 makes a neighbour
in another slice unavailable, so a fresh map per segment is not a shortcut but
the correct answer: an undecoded neighbour and one in a different segment then
read alike. Sharing them would leak availability across a slice boundary.

**Status: done.** `Decoder.pic` (`pkg/decoder/picture.go`) holds the picture in
progress; `first_slice_segment_in_pic_flag` opens it and the next first-flag or
the end of the NAL stream closes it, at which point the loop filters, the
reference-frame update and the conformance-window crop run once. Multi-slice
pictures without tiles decode as one frame now instead of N bogus ones.

### T2 — Tile geometry and slice segment address (S)

Read `dependent_slice_segment_flag` and `slice_segment_address` (mp4ff's
`ParseSliceHeader` already does both, including the `Ceil(Log2(PicSizeInCtbsY))`
width, if we would rather use it than hand-roll it).

While in that part of the header: `num_extra_slice_header_bits` is not skipped
either. Both hand-rolled parsers go straight from `slice_pic_parameter_set_id`
to `slice_type`, missing the `slice_reserved_flag[i]` loop that sits between the
segment address and `slice_type`. x265 and kvazaar both emit 0, so this is
latent rather than live, but it belongs to the same edit.

Derive the spec 6.5.1 tables once per PPS: `colBd` / `rowBd` from either uniform
spacing or the explicit widths, then `CtbAddrRsToTs`, `CtbAddrTsToRs` and
`TileId`. Uniform spacing is the `(i+1)*cols/N - i*cols/N` formula, not equal
division — worth a test, since the two agree on most grids and differ on the
ones that matter.

**Status: done.** The tables live in `internal/tiles`, built from the mp4ff PPS
fields by `tileGrid` in `pkg/decoder/picture.go`. Dependent slice segments are
rejected with a clear error rather than mis-parsed.

### T3 — Iterate in tile scan, bounded by the slice segment (S/M)

`DecodeSliceData` starts at the segment's address and walks in **tile scan**
order (`CtbAddrTsToRs`), stopping on `end_of_slice_segment_flag`. That flag is
already decoded (`internal/slice/slicedata.go:169`) but the loop bound comes
from the picture size instead.

For the retiler shape, that is the whole of it: one slice per tile means the
scan never crosses a tile boundary, so contexts are initialised once per segment
exactly as they are now.

**Status: done.** `DecodeSliceData` takes `Grid` and `SegmentAddressRS` and
walks tile scan from the segment's first CTB. A segment that spans several tiles
(entry points present with tiles enabled) is refused — that is T6.

### T4 — Neighbour availability: same tile *and* same slice (M)

This is the correctness crux. `buildRefSamples` currently treats any neighbour
in a lower raster CTB address as available:

```go
nAddr := nCtbY*numCtbX + nCtbX
if nAddr < curAddr {
    return true
}
```

Spec 6.4.1 requires the neighbour to be in the same slice *and* the same tile,
and "earlier" means earlier in **tile scan**, not raster. Both halves are wrong
for a tiled picture: the CTB to the left of a tile's left edge lives in the
neighbouring tile and has a lower raster address, so it is wrongly deemed
available and every tile except the leftmost mispredicts its first column.

The fix is small and local — compare `CtbAddrRsToTs[nAddr] < CtbAddrRsToTs[curAddr]`,
and require `TileId[…]` and the owning slice-segment address to match. Passing
an availability predicate into `buildRefSamples` rather than the raw geometry
keeps its two call sites untouched.

**Status: done for the supported subset, and smaller than the above.** It turned
out that T1–T3 are unobservable without it — with the raster comparison in place
a 2x2 tiled picture decoded tile 0 perfectly and the other three tiles as
complete garbage, because every CTB in tile 1 has a lower-addressed CTB in tile
0 to predict from.

The shape the fix took is not the tile tables but the reconstruction map. The
raster comparison is **deleted**; `isAvailable` is now "inside the picture and
already reconstructed", and the map is cleared at the start of every slice
segment. For the subset we accept — one segment never spans more than one tile,
no dependent segments — "reconstructed earlier in this segment" is exactly spec
6.4.1's "same slice, same tile, earlier in decoding order", since within one
tile raster order and tile scan order agree. No tile tables are consulted at
reconstruction time at all, and single-segment pictures are unaffected: their
map says exactly what the address comparison used to.

Two pieces of the general rule are still missing, both only reachable with
several tiles in one slice segment (T6): the map would then need clearing at
each tile boundary inside the segment, and `cu_skip_flag`'s context still derives
its two increments from `x0 > 0` / `y0 > 0` rather than from neighbour
availability and the neighbours' skip flags (spec 9.3.4.2.2) — an approximation
that predates tiles and is wrong for a P slice at a tile edge.

### T4b — the parse-level half of availability (S)

Availability is not only a reconstruction question. `sao_merge_left_flag` and
`sao_merge_up_flag` are *present in the bitstream* only when the left/above CTB
is in the same slice segment **and** the same tile (spec 7.3.8.3). Gating them
on picture boundaries alone reads bins that were never coded, which is a CABAC
desync — garbage from that CTB on, not a wrong filter decision.

This one is live for any multi-slice picture, tiled or not, whenever SAO is on.
It is latent in the retiler streams only because everything retiler currently
produces is `--no-sao` / `sao off`.

**Status: done alongside T3**, since it needs exactly T3's inputs (the tile grid
plus the segment's first CTB) and leaving a known desync inside a code path that
T1–T3 had just opened up would have been worse than the fix.

### T5 — Loop filters that stop at tile boundaries (M)

With `loop_filter_across_tiles_enabled_flag = 0`, no deblocking or SAO may cross
a tile edge. If hi265 filters across it, a merged decode will differ from the
standalone decodes precisely at the tile seams — turning `retile -verify` into a
generator of false mismatches.

`deblock.Apply` builds edge flags on a 4x4 grid (`internal/deblock/deblock.go:45`);
the change is to clear the flags on edges whose two sides fall in different
tiles (or different slices, per `slice_loop_filter_across_slices_enabled_flag`).
SAO needs the same restriction on its neighbour fetches.

One nuance, smaller than it first looks: `Apply` takes a single scalar
`sliceQPY`, and a tiled picture has one QP *per slice* — retiler copies each
input's QP verbatim, so tiles routinely differ. But `Apply` already builds a
per-4x4 QP map out of `cu.QpY` and uses the scalar only to prefill blocks no CU
covers, so the picture-level call T1 makes is very nearly right already.

**Status: done.** The structure both filters need lives in
`internal/loopfilter`: which tile and which slice every CTB belongs to, plus
each slice's `slice_deblocking_filter_disabled_flag`, beta and tC offsets, and
`slice_loop_filter_across_slices_enabled_flag`. One question answers both
filters — *may these two samples be filtered together?* — because tiles and
slices are built from whole CTBs, so it is a question about the two CTBs that
contain them.

- **Deblocking** clears the edge flags on such a boundary before filtering,
  which is exactly the spec 8.7.2 `filterEdgeFlag` derivation, and chroma needs
  no second pass: a chroma edge lies between the same two CTBs as its luma edge.
  The per-slice parameters became real lookups at the same time: beta and tC
  offsets are taken from the slice containing the q side of each edge, and a
  slice with deblocking disabled contributes no edges at all while still
  contributing its QPs, which a neighbouring enabled slice's edge averages in.
- **SAO** leaves a sample unmodified when either end of its edge offset class
  is unavailable (spec 8.7.3.2). Both ends matter, since the class is a line
  through the sample and one end can sit in the tile to the left while the other
  sits in the tile to the right. Band offset reads no neighbours, so nothing
  restricts it.

The slice half of the rule needed one more parse fix:
`slice_loop_filter_across_slices_enabled_flag` was read and thrown away, and its
presence was keyed on the SPS SAO flag rather than the two *slice* SAO flags, so
a slice with SAO off and deblocking disabled would have read a bit that was
never coded. It is now inferred from the PPS when absent, per spec 7.4.7.1.

That per-slice flag is not decoration. In `hevc-retiler`'s `grid.265` it is 1 in
pictures 1, 2 and 5 and 0 in pictures 3 and 4 — x265 varies it per frame, and it
survives the stitch verbatim — so a decoder that assumed the PPS value would be
using the wrong rule on three fifths of that stream.

### T6 — Several tiles in one slice segment, via entry points (S)

Not needed for retiler, but this is the *common* shape everywhere else:
`kvazaar --tiles WxH` puts every tile in one slice segment by default, and so do
most encoders. `splitSubstreams` already turned entry point offsets into
substreams for WPP; tiles differ only in what happens when one begins —
contexts re-initialise unconditionally at each tile's first CTB (spec 9.3.1)
rather than being restored from the row above.

Three things reset at that boundary, and the description below is what landed:

- **CABAC contexts**, from `InitModels` (spec 9.3.1).
- **QP prediction.** `qPY_PREV` returns to `SliceQpY` at the first quantization
  group of a slice, of a tile, and of a CTB row under WPP (spec 8.6.1). The WPP
  case existed; the tile case is new, and shares one `resetToSliceQP` with it.
- **Neighbour availability.** Nothing in an earlier tile may be predicted from
  (spec 6.4.1), so the intra mode map and the CU depth map are cleared, and
  reconstruction clears the sample availability map when the CU stream crosses
  into another tile. This is T4's general rule arriving where it is actually
  needed: with several tiles in one segment, "decoded in this segment" stops
  meaning "same tile".

**Status: done.** Each of the three is load-bearing, and each is pinned by a
committed vector — removing the context re-initialisation or the availability
reset changes `tiles_multi_2x2_128x128`, and removing the QP reset changes
12 272 samples of `tiles_multi_qp_2x2_128x128`, whose delta-QP map is what makes
`cu_qp_delta` live in the first place.

What is still refused: **tiles combined with wavefront parallel processing**.
That would make each CTB row of each tile its own substream, with the snapshot
coming from the row above *within the same tile*. No HEVC profile allows the
combination — kvazaar calls it experimental and disables WPP when tiles are
enabled — so it errors rather than guesses.

### T7 — Tests (S)

The conformance harness from 0.2 is the right vehicle, with one addition: the
generator cannot produce tiled streams, so the fixtures have to come from an
encoder that can. **x265 cannot** — it offers WPP and slices, not tiles.
**kvazaar can**, and `--tiles WxH --slices tiles` produces precisely the
retiler shape (one independent segment per tile, no entry points), so the
fixtures need not come from `hevc-retiler` at all:

```
kvazaar -i in.yuv --input-res 256x256 -n 3 -p 1 --preset veryfast \
        --tiles 2x2 --slices tiles --no-deblock --sao off --level 3.1 -o t.265
```

- kvazaar with tiles, deblocking and SAO off, decoded by `pkg/decoder` and by
  FFmpeg, asserted identical. This isolates T1–T4 from T5: with both loop
  filters off, tile geometry and availability are the only things that can
  differ.
- The same content with deblocking on, to pin T5. Until T5 lands the expected
  result is "equal except within 4 luma / 2 chroma samples of a tile seam".
- A `hevc-retiler` output plus its inputs: assert each tile's crop equals the
  input's standalone decode. This is `retile -verify`'s check, run inside
  hi265's own test suite — and it fails loudly if T4 or T5 is wrong.
- A grid whose uniform spacing is not an equal division, to pin T2. 320x256 with
  a 64-luma CTB is 5 CTBs wide, so two uniform columns are 2 and 3 CTBs, not
  2.5 rounded either way: `--tiles 2x1` on that size is the fixture.

**Status: partly done.** Three committed golden vectors, generated by
`tools/gen_tiles_bitstreams.sh` and each under 25 kB of golden YUV:
`tiles_2x1_2frames_128x64` (two segments per picture, over two pictures, so the
close-on-next-first-flag path is covered), `tiles_2x2_128x128` (boundaries in
both directions) and `tiles_nonequal_192x64` (3 CTBs in two uniform columns —
1 then 2). `internal/tiles` has unit tests for the spec 6.5.1 tables directly,
including that 2x2 grid's tile scan and the non-equal uniform split.

Two more came with T5: `tiles_2x2_deblock_128x128` and
`tiles_2x2_sao_128x128`, the same 2x2 grid with one loop filter on at a time.
Both fail if the boundary rules are removed, which is the property a fixture for
this needs; the three unfiltered vectors stay green under that mutation, as they
should. `internal/loopfilter` also has direct unit tests for the two rules,
including that it is the *later* slice's flag which governs.

Still to add: the `hevc-retiler` output plus its inputs, for the cross-project
check in CI. A plain multi-slice, no-tiles vector turns out to be awkward to
generate — x265 refuses `--slices` without WPP, and kvazaar's `--slices wpp`
emits *dependent* segments — so both encoders here can only produce the
multi-segment shapes this decoder refuses. The tiled vectors cover T1 anyway.

One thing worth knowing when reading these two: in the kvazaar vectors the tile
seams are blocked by *both* rules, because kvazaar sets
`pps_loop_filter_across_slices_enabled_flag` to 0 and puts one slice in each
tile. The retiler streams are what exercise the tile rule alone — their slices
permit filtering across slice boundaries in three of five pictures, so with the
tile rule disabled they differ from FFmpeg in exactly those three.

### T8 — Refuse what the decoder cannot reconstruct (S)

Independent of tiles, and a prerequisite for hi265 being *usable* as a verify
backend rather than merely present.

`pkg/decoder` reconstructs inter CUs only as zero-motion skip copies. A P slice
whose CUs are not skipped falls through to the intra path, which mis-parses it
and returns a picture with **no error at all**: `pa.265` above is bit-exact on
its IDR and then 62 %, 82 %, 87 %, 90 % of bytes wrong on frames 1–4. A caller
cannot tell that apart from a successful decode.

**Status: done**, and it found more than expected. Three things landed:

- `pred_mode_flag` is parsed, and an inter CU that is not a zero-motion skip is
  refused with its position. `cu_skip_flag`'s context came with it: it now counts
  how many of the left and above neighbours were themselves skipped
  (spec 9.3.4.2.2) instead of asking whether those positions are inside the
  picture. The old approximation agreed with the spec only for all-skip content
  in a single slice, which is exactly what the tests contained.
- `pred_weight_table` is parsed rather than skipped. x265 turns weighted
  prediction **on by default**, so every default-settings P slice carries the
  table, and skipping it left the reader mid-header: the entry point offsets that
  follow read as garbage. A stream that signals no actual weight decodes on,
  since default weights predict the plain reference sample; one that signals a
  weight is refused.
- `ref_pic_lists_modification` is refused when `NumPocTotalCurr` says the syntax
  is present, which meant counting `NumPocTotalCurr` from the reference picture
  set — for the inline set and for one taken from the SPS.

What this bought, on `testdata/hevc_1idr_1p.mp4` — a real 720p x265 encode
committed to this repo: its IDR now decodes bit-exactly, and its P picture is
refused at the inter CU at (304,120) rather than at the first CTU of a
misparsed header. That file is in the roadmap's 0.10 table as decoding
"exact"; that claim was only ever true of its first picture.

This matters more than it looks for `retile -verify`. Zero-motion copy is
translation-invariant within a tile, so a hi265 that ignores motion vectors
would very likely report the merged and standalone decodes as *equal* on a
genuinely non-motion-constrained stream — passing `demo.sh`'s scenario 5, the
negative test, for the wrong reason. A loud refusal is the only safe behaviour
until real motion compensation exists.

## What this buys — and what it does not

**It buys the all-intra case, completely** — and as of T5 that is delivered,
not projected. Every all-intra input in `hevc-retiler/out` and every merged
stream built from them decodes bit-exactly against ffmpeg, and each tile of a
merged picture equals its input's standalone decode under hi265 alone. For a
stream of I-frames stitched from x265 inputs, `retile -verify` can drop ffmpeg
today: swap `decodeYUV420`'s `exec.Command` for `decoder.New().DecodeAnnexB`
plus `frame.YUV420Bytes()`.

The differential check `verify` performs is in any case weaker than
bit-exactness, which is a useful safety margin rather than the load-bearing
argument it looked like before those measurements. `verify` compares hi265's
decode of the merged stream against hi265's decode of each input. Retiler's
guarantee is that a tile's CABAC payload is bit-identical to the input's and
that the tile's decoding context — available neighbours, loop filter boundaries
— is identical to the standalone picture's. So the two decodes must agree for
*whatever* the decoder computes from a given (payload, context) pair, even where
that disagrees with ffmpeg: errors that are a pure function of payload and
available neighbours cancel on both sides.

What does *not* cancel is anything position-dependent — an availability rule
keyed on absolute CTB address, a filter that treats a tile edge as interior, a QP
carried per picture instead of per slice. Those are exactly T4 and T5, and a
mistake there surfaces as a spurious mismatch rather than a silent pass, which
makes the check pleasantly self-policing during bring-up.

**It does not buy the inter case, which is the interesting one.** The MCTS
question — do motion vectors and their interpolation footprints stay inside the
tile — only arises for P/B inputs, and `hevc-retiler`'s `demo.sh` deliberately
includes a negative scenario built from a non-motion-constrained encode that
`verify` must reject. `pkg/decoder` reconstructs only zero-motion skip CUs, so
kvazaar P-frames with real vectors are out of reach — measurably so, and
and it says so as of T8 rather than returning a wrong picture. That matters for
the caller: a decoder that ignored motion vectors could compare *equal* on a
genuinely non-MCTS stream and pass `retile -verify`'s negative test for the
wrong reason. It now refuses instead, which is the only safe answer until real
motion compensation exists.

So the honest recommendation for the caller is a decoder selector rather than a
replacement: use hi265 for all-intra scenarios, keep ffmpeg for P/B ones, and
where both are available run them as a cross-check. Retiring ffmpeg entirely
needs real motion compensation in `pkg/decoder` — a substantial piece of work
that is not on the roadmap today, and a far larger item than tiles.

## Suggested order

T1 → T2 → T3 landed together, and T4 and T4b came with them out of necessity
rather than choice: T3's tile tables are exactly what T4b's gating needs, and
without T4 the other three tiles of a 2x2 picture decode as garbage, so there
would have been nothing to verify.

T5 followed, and then T6 with T4's general rule folded in — the three per-tile
resets are unobservable one at a time, so they had to land together.

What is left, in the order that makes each step verifiable:

1. ~~**Dependent slice segments.**~~ **Done** — see roadmap 0.14. Availability,
   QP prediction, the CABAC contexts and the wavefront snapshot are now per
   *slice* rather than per segment, and the loop filters see a dependent boundary
   as interior. Both `kvazaar --slices wpp` and `x265 --wpp --slices N` decode
   bit-exactly.
2. ~~**T8.**~~ **Done** — see roadmap 0.15, including the `cu_skip_flag` context
   and two header fields the parser used to assume away.
3. **Encoder-side tiles.** `pkg/encode` refuses `TilesEnabledFlag` outright, so
   there is no gray CRA splice into a tiled stream, no `hi265-mp4-extend` on
   tiled content, and no in-repo tiled vectors. One slice segment per tile is
   mostly slice-header plumbing, and the finished decoder verifies it in-process.
4. **T7's last fixture**, a committed `hevc-retiler` output with its inputs, for
   the cross-project check in CI.

Tiles combined with WPP is the one decoding shape deliberately left out: no
profile permits it.

## A related mp4ff gap, now fixed and released

`hevc.ParseSliceHeader` used to omit the spec 7.4.7.1 inference that
`slice_deblocking_filter_disabled_flag`, when absent, equals
`pps_deblocking_filter_disabled_flag`. With
`pps_loop_filter_across_slices_enabled_flag = 1`, deblocking disabled in the PPS
and no slice override — what x265 `--no-deblock` produces — it read a
`slice_loop_filter_across_slices_enabled_flag` bit that is not there and then
failed `byte_alignment()`. hi265's own slice header parser always got this right
by seeding from the PPS default, which is why the decoder never saw it.

The decoder is not the whole of hi265, though. `pkg/encode/extend.go` uses
`hevc.ParseSliceHeader` to read the last picture's state, so
`hi265-mp4-extend` refused every x265 `--no-deblock` stream — including
`testdata/sincos_128x64.265`, one of this repo's own vectors — with "alignment
bit is not equal to one". Nothing caught it because the other tests extend
streams from our own generator, which does not have that shape.

Fixed in mp4ff (Eyevinn/mp4ff#558) along with the three other 7.4.7.1 inferences
that were also missing — `slice_loop_filter_across_slices_enabled_flag`, the two
deblocking offsets, and `pic_output_flag` — and **released in v0.56.0**, which
this repo now requires. `TestAppendEmptyFramesOnRealNoDeblockStream` pins it: the
test fails on v0.52.0 with that exact error and passes on v0.56.0.
