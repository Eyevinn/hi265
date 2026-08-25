# Test generation scripts

Scripts that regenerate the committed vectors under `testdata/`, plus the
end-to-end stitching demo. Each script's header explains *why* its vectors are
shaped the way they are — what each one isolates, and which flags are off to
isolate it. This file is the index: which script to run when a particular
golden test needs its input rebuilt.

| script | regenerates | needs |
|---|---|---|
| `gen_test_bitstream.sh` | `black_16x16` and its golden | ffmpeg **with libx265** |
| `gen_slice_bitstreams.sh` | the 3 `slices_*` vectors and goldens | ffmpeg, x265, kvazaar |
| `gen_tiles_bitstreams.sh` | the 8 `tiles_*` vectors and goldens | ffmpeg, kvazaar |
| `gen_retile_bitstreams.sh` | the 5 `retile_*` stitcher inputs | ffmpeg, x265, kvazaar |
| `gen_retile_inputs.sh` | nothing committed — one tile-ready input into a directory you name | ffmpeg, x265, kvazaar |
| `retile_demo.sh` | nothing committed — 5 end-to-end `hi265retile` scenarios in a temp dir, including a non-MCTS stitch that verification must reject | ffmpeg, x265, kvazaar |

x265 cannot produce tiles — it offers wavefront parallel processing and slices,
but not tiles — which is why the tiled families come from kvazaar.

## Vectors without a script

The scripts above cover 17 of the committed `.265` vectors. The rest were made
with one-off x265 invocations during the work that needed them; the parameter
conventions are recorded in `docs/roadmap.md` alongside the phase that added
each one.

For new cases, prefer the pattern the scripts do not use: a test that generates
its own vector and skips when the encoder is absent. `TestDecodeRealContent`,
`TestDecodeWPPStreams` and `TestDeblockExactness` do this, and it keeps the
repository free of a fixture whose provenance lives only in a shell script.

## A recurring trap

Many ffmpeg builds ship without libx265, so `-c:v libx265` fails on them —
check with `ffmpeg -encoders | grep libx265`. Only `gen_test_bitstream.sh`
depends on it; every other script drives the `x265` or `kvazaar` binary
directly and converts with ffmpeg afterwards, which is the more portable shape.

The conformance tests skip cleanly when these encoders are missing, and honour
`HI265_FFMPEG` and `HI265_X265` to point at specific binaries.
