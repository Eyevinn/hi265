#!/bin/bash
# Regenerate the tiled test vectors under testdata/ and their golden YUVs.
#
# Requires `kvazaar` and `ffmpeg`. x265 cannot produce these: it offers
# wavefront parallel processing and slices, but not tiles.
#
# Two families, and the difference is the whole point of having both:
#
#   --tiles WxH --slices tiles : one independent slice segment per tile, no
#                                entry point offsets. What `hevc-retiler` emits.
#   --tiles WxH                : one slice segment covering every tile, reached
#                                through entry point offsets, with the CABAC
#                                contexts, QP prediction and neighbour
#                                availability restarting at each tile.
#                                kvazaar's default, and most encoders'.
#
# The first three vectors have both loop filters off, so they isolate tile
# geometry, tile scan order and neighbour availability. The last two turn on one
# filter each: those are the vectors that pin the boundary rules, since a filter
# that reached across a tile edge would change samples along the seam.
set -euo pipefail

TESTDATA="$(cd "$(dirname "$0")/../testdata" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# gen <name> <WxH> <tiles WxH> <frames> [extra kvazaar flags...]
# The caller passes the slicing mode, since that is what distinguishes the two
# families above.
gen() {
	local name="$1" res="$2" tiles="$3" frames="$4"
	shift 4

	ffmpeg -y -hide_banner -loglevel error -f lavfi \
		-i "testsrc2=size=$res:rate=5" -frames:v "$frames" \
		-pix_fmt yuv420p "$TMP/$name.yuv"

	kvazaar -i "$TMP/$name.yuv" --input-res "$res" --input-fps 5 -n "$frames" \
		-p 1 --preset veryfast --tiles "$tiles" \
		"$@" --level 3.1 -o "$TESTDATA/$name.265" >/dev/null 2>&1

	ffmpeg -y -hide_banner -loglevel error -i "$TESTDATA/$name.265" \
		-f rawvideo -pix_fmt yuv420p "$TESTDATA/golden/$name.yuv"

	echo "  $name: $res, ${frames}f, tiles $tiles" \
		"($(wc -c <"$TESTDATA/$name.265") bytes," \
		"golden $(wc -c <"$TESTDATA/golden/$name.yuv") bytes)"
}

echo "=== Generating tiled HEVC vectors and goldens ==="
echo "-- one slice segment per tile"
# Two tile columns across two frames: also covers finishing one picture when the
# next picture's first slice segment arrives.
gen tiles_2x1_2frames_128x64 128x64 2x1 2 --slices tiles --no-deblock --sao off
# A 2x2 grid: both a vertical and a horizontal tile boundary.
gen tiles_2x2_128x128 128x128 2x2 1 --slices tiles --no-deblock --sao off
# 192x64 is 3 CTBs wide, so two uniform tile columns are 1 CTB and 2 CTBs, not
# an equal division — this pins the spec 6.5.1 uniform spacing formula.
gen tiles_nonequal_192x64 192x64 2x1 1 --slices tiles --no-deblock --sao off
# Deblocking on, so the tile seams are where a filter that crossed them would
# show up. kvazaar's default is --deblock 0:0, i.e. enabled with zero offsets.
gen tiles_2x2_deblock_128x128 128x128 2x2 1 --slices tiles --sao off
# SAO on: its edge offset mode reads a neighbour on each side of the sample, so
# a sample next to a tile seam has an unavailable neighbour and stays put.
gen tiles_2x2_sao_128x128 128x128 2x2 1 --slices tiles --no-deblock --sao full

echo "-- several tiles in one slice segment (entry point offsets)"
# The substream switch, the CABAC re-initialisation and the availability reset
# at each tile boundary. Removing any of the three changes these pixels.
gen tiles_multi_2x2_128x128 128x128 2x2 1 --no-deblock --sao off
# Both loop filters on with one slice covering every tile: here the slice rule
# cannot protect a seam, because there is only one slice, so this pins the tile
# rule on its own.
gen tiles_multi_filters_2x2_128x128 128x128 2x2 1
# A delta-QP map turns on cu_qp_delta, which is what makes the per-tile reset of
# qPY_PREV (spec 8.6.1) observable; without it the QP never moves.
printf '2 2\n-8 8 6 -6\n' >"$TMP/roi.txt"
gen tiles_multi_qp_2x2_128x128 128x128 2x2 1 --no-deblock --sao off --roi "$TMP/roi.txt"
echo "Done."
