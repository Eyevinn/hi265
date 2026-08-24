#!/usr/bin/env bash
# Regenerate the tile-stitcher input vectors under testdata/.
#
# Requires `ffmpeg`, `x265` and `kvazaar`. These are *inputs* to hi265retile,
# not stitched output: the tests stitch them and compare against committed
# digests, so the stitch stays byte-stable and any change to the bit-splice has
# to be deliberate.
#
# Every input is one picture per slice segment with tiles and WPP off, which is
# what the splice requires. 128x64 and 64x64 are 2x1 and 1x1 CTBs at the 64-luma
# CTB size, so a 128-wide next to a 64-wide tile forces explicit
# column_width_minus1 rather than uniform spacing — a separate code path.
set -euo pipefail

TESTDATA="$(cd "$(dirname "$0")/../testdata" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FRAMES=2

# intra <name> <WxH> <lavfi source>: all-intra, so no motion to constrain.
intra() {
	local name="$1" res="$2" src="$3"
	ffmpeg -y -hide_banner -loglevel error -f lavfi -i "$src" -frames:v "$FRAMES" \
		-pix_fmt yuv420p -f rawvideo "$TMP/$name.yuv"
	x265 --input-res "$res" --fps 5 --frames "$FRAMES" --qp 30 \
		--keyint 1 --no-open-gop --no-wpp --no-sao --profile main \
		--input "$TMP/$name.yuv" --output "$TESTDATA/$name.265" >/dev/null 2>&1
	echo "  $name: $res, ${FRAMES}f intra ($(wc -c <"$TESTDATA/$name.265") bytes)"
}

# mcts <name> <WxH> <lavfi source>: IDR + low-delay P with motion and
# interpolation confined to the frame, which is what makes an inter picture
# usable as a tile at all. x265 cannot do this; kvazaar can.
mcts() {
	local name="$1" res="$2" src="$3"
	ffmpeg -y -hide_banner -loglevel error -f lavfi -i "$src" -frames:v "$FRAMES" \
		-pix_fmt yuv420p -f rawvideo "$TMP/$name.yuv"
	kvazaar -i "$TMP/$name.yuv" --input-res "$res" --input-fps 5 -n "$FRAMES" \
		--preset ultrafast --gop 0 --no-bipred --ref 1 --qp 30 \
		--mv-constraint frametilemargin --no-wpp --no-tmvp --sao off \
		--level 3.1 -o "$TESTDATA/$name.265" >/dev/null 2>&1
	echo "  $name: $res, ${FRAMES}f MCTS P ($(wc -c <"$TESTDATA/$name.265") bytes)"
}

echo "=== Generating hi265retile input vectors ==="
intra retile_a_128x64 128x64 "testsrc2=size=128x64:rate=5"
intra retile_b_128x64 128x64 "smptebars=size=128x64:rate=5"
intra retile_c_64x64   64x64 "mandelbrot=size=64x64:rate=5"
mcts  retile_pa_128x64 128x64 "testsrc2=size=128x64:rate=5"
mcts  retile_pb_128x64 128x64 "mandelbrot=size=128x64:rate=5"
echo "Done."
