#!/bin/bash
# Regenerate the multi-slice-segment test vectors under testdata/ and their
# golden YUVs. Requires `kvazaar`, `x265` and `ffmpeg`.
#
# A slice is one independent slice segment followed by zero or more dependent
# ones, and the two encoders here produce different shapes of that:
#
#   kvazaar --slices wpp : one *dependent* segment per CTB row. The segments
#                          share a slice, so neighbours across a segment
#                          boundary stay available, the CABAC contexts carry
#                          over, and the loop filters treat the boundary as
#                          interior. Each segment holds one row and therefore
#                          carries no entry point offsets of its own.
#   x265 --wpp --slices N : N *independent* slices, each covering whole CTB rows
#                          and carrying entry point offsets for them. Here the
#                          wavefront snapshot must NOT cross the slice boundary:
#                          the row above belongs to another slice, so the first
#                          row of each slice starts from initial contexts.
#
# Both shapes are small on purpose; the goldens are what dominates the repo size.
set -euo pipefail

TESTDATA="$(cd "$(dirname "$0")/../testdata" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

src() { # src <WxH>
	ffmpeg -y -hide_banner -loglevel error -f lavfi \
		-i "testsrc2=size=${1}:rate=5" -frames:v 1 -pix_fmt yuv420p "$TMP/in.yuv"
}

golden() { # golden <name>
	ffmpeg -y -hide_banner -loglevel error -i "$TESTDATA/$1.265" \
		-f rawvideo -pix_fmt yuv420p "$TESTDATA/golden/$1.yuv"
	echo "  $1: $(wc -c <"$TESTDATA/$1.265") bytes, golden $(wc -c <"$TESTDATA/golden/$1.yuv") bytes"
}

echo "=== Generating multi-slice-segment vectors and goldens ==="

# Dependent segments, one per CTB row.
src 128x128
kvazaar -i "$TMP/in.yuv" --input-res 128x128 --input-fps 5 -n 1 -p 1 \
	--preset veryfast --slices wpp --no-deblock --sao off --level 3.1 \
	-o "$TESTDATA/slices_dep_wpp_128x128.265" >/dev/null 2>&1
golden slices_dep_wpp_128x128

# The same with deblocking on: a dependent segment boundary is not a slice
# boundary, so the filter must run straight through it.
kvazaar -i "$TMP/in.yuv" --input-res 128x128 --input-fps 5 -n 1 -p 1 \
	--preset veryfast --slices wpp --sao off --level 3.1 \
	-o "$TESTDATA/slices_dep_wpp_deblock_128x128.265" >/dev/null 2>&1
golden slices_dep_wpp_deblock_128x128

# Two independent slices with wavefront parallel processing. 256x128 is two CTB
# rows, so --slices 2 gives one row each and the second slice cannot inherit the
# first's wavefront snapshot.
src 256x128
x265 --input "$TMP/in.yuv" --input-res 256x128 --fps 5 --frames 1 \
	--keyint 1 --no-open-gop --wpp --slices 2 --no-sao --no-deblock \
	--profile main --output "$TESTDATA/slices_wpp_2slices_256x128.265" >/dev/null 2>&1
golden slices_wpp_2slices_256x128
echo "Done."
