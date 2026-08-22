#!/bin/bash
# Regenerate the tiled test vectors under testdata/ and their golden YUVs.
#
# Requires `kvazaar` and `ffmpeg`. x265 cannot produce these: it offers
# wavefront parallel processing and slices, but not tiles. kvazaar's
# "--tiles WxH --slices tiles" puts each tile in its own independent slice
# segment with no entry point offsets, which is the tiling that `hevc-retiler`
# emits and the only shape pkg/decoder supports (docs/tiles-decoding.md).
#
# The first three vectors have both loop filters off, so they isolate tile
# geometry, tile scan order and neighbour availability. The last two turn on one
# filter each: those are the vectors that pin the boundary rules, since a filter
# that reached across a tile edge would change samples along the seam.
set -euo pipefail

TESTDATA="$(cd "$(dirname "$0")/../testdata" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# gen <name> <WxH> <tiles WxH> <frames> [kvazaar filter flags...]
gen() {
	local name="$1" res="$2" tiles="$3" frames="$4"
	shift 4

	ffmpeg -y -hide_banner -loglevel error -f lavfi \
		-i "testsrc2=size=$res:rate=5" -frames:v "$frames" \
		-pix_fmt yuv420p "$TMP/$name.yuv"

	kvazaar -i "$TMP/$name.yuv" --input-res "$res" --input-fps 5 -n "$frames" \
		-p 1 --preset veryfast --tiles "$tiles" --slices tiles \
		"$@" --level 3.1 -o "$TESTDATA/$name.265" >/dev/null 2>&1

	ffmpeg -y -hide_banner -loglevel error -i "$TESTDATA/$name.265" \
		-f rawvideo -pix_fmt yuv420p "$TESTDATA/golden/$name.yuv"

	echo "  $name: $res, ${frames}f, tiles $tiles" \
		"($(wc -c <"$TESTDATA/$name.265") bytes," \
		"golden $(wc -c <"$TESTDATA/golden/$name.yuv") bytes)"
}

echo "=== Generating tiled HEVC vectors and goldens ==="
# Two tile columns across two frames: also covers finishing one picture when the
# next picture's first slice segment arrives.
gen tiles_2x1_2frames_128x64 128x64 2x1 2 --no-deblock --sao off
# A 2x2 grid: both a vertical and a horizontal tile boundary.
gen tiles_2x2_128x128 128x128 2x2 1 --no-deblock --sao off
# 192x64 is 3 CTBs wide, so two uniform tile columns are 1 CTB and 2 CTBs, not
# an equal division — this pins the spec 6.5.1 uniform spacing formula.
gen tiles_nonequal_192x64 192x64 2x1 1 --no-deblock --sao off
# Deblocking on, so the tile seams are where a filter that crossed them would
# show up. kvazaar's default is --deblock 0:0, i.e. enabled with zero offsets.
gen tiles_2x2_deblock_128x128 128x128 2x2 1 --sao off
# SAO on: its edge offset mode reads a neighbour on each side of the sample, so
# a sample next to a tile seam has an unavailable neighbour and stays put.
gen tiles_2x2_sao_128x128 128x128 2x2 1 --no-deblock --sao full
echo "Done."
