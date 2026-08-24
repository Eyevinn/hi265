#!/usr/bin/env bash
# End-to-end demo: generate tile-ready HEVC videos, stitch them with
# hi265retile under several geometries, and let -verify decode and compare
# every tile to its source. The final scenario shows -verify catching a
# non-MCTS stitch.
#
# Requires ffmpeg, x265 and kvazaar on PATH. All media goes to a temp dir that
# is removed on exit; pass a directory as $1 to keep the output instead.
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"

if [ $# -ge 1 ]; then
	TMP="$1"; mkdir -p "$TMP"
	echo "keeping output in $TMP"
else
	TMP="$(mktemp -d)"
	trap 'rm -rf "$TMP"' EXIT
fi

gen() { "$REPO/tools/gen_retile_inputs.sh" "$TMP" "$@"; }
retile() { go run "$REPO/cmd/hi265retile" "$@"; }

echo "== Scenario 1: all-intra, vertical 2x1 (512x256 + 512x256 -> 512x512) =="
gen a 512x256 intra "testsrc2=size=512x256:rate=5"
gen b 512x256 intra "mandelbrot=size=512x256:rate=5"
retile -verify -o "$TMP/merged.265" "$TMP/a.265" "$TMP/b.265"

echo "== Scenario 2: all-intra, 2x2 grid (four 256x256 -> 512x512) =="
gen q0 256x256 intra "testsrc2=size=256x256:rate=5"
gen q1 256x256 intra "mandelbrot=size=256x256:rate=5"
gen q2 256x256 intra "rgbtestsrc=size=256x256:rate=5,format=yuv420p"
gen q3 256x256 intra "smptebars=size=256x256:rate=5,format=yuv420p"
retile -grid 2x2 -verify -o "$TMP/grid.265" \
	"$TMP/q0.265" "$TMP/q1.265" "$TMP/q2.265" "$TMP/q3.265"

echo "== Scenario 3: P-frame MCTS, vertical 2x1 (512x256 + 512x256 -> 512x512) =="
gen pa 512x256 pframe "testsrc2=size=512x256:rate=5"
gen pb 512x256 pframe "mandelbrot=size=512x256:rate=5"
retile -verify -o "$TMP/pmerged.265" "$TMP/pa.265" "$TMP/pb.265"

echo "== Scenario 4: P-frame MCTS, 2x2 grid (four 256x256 -> 512x512) =="
gen p0 256x256 pframe "testsrc2=size=256x256:rate=5"
gen p1 256x256 pframe "mandelbrot=size=256x256:rate=5"
gen p2 256x256 pframe "rgbtestsrc=size=256x256:rate=5,format=yuv420p"
gen p3 256x256 pframe "smptebars=size=256x256:rate=5,format=yuv420p"
retile -grid 2x2 -verify -o "$TMP/pgrid.265" \
	"$TMP/p0.265" "$TMP/p1.265" "$TMP/p2.265" "$TMP/p3.265"

echo "== Scenario 5: NEGATIVE - P-frames without MCTS; -verify must reject =="
# Same kvazaar settings as the pframe mode above but WITHOUT --mv-constraint,
# so motion may reference across the tile seam. The stitch is structurally
# valid but decodes wrong, and -verify catches it.
for pair in na:a nb:b; do
	n="${pair%%:*}"; yuv="${pair##*:}"
	kvazaar -i "$TMP/$yuv.yuv" --input-res 512x256 --input-fps 5 -n 5 \
		--preset ultrafast --gop 0 --no-bipred --ref 1 --no-wpp --no-tmvp \
		--sao off --level 3.1 -o "$TMP/$n.265" >/dev/null 2>&1 || true
done
if retile -verify -o "$TMP/nmerged.265" "$TMP/na.265" "$TMP/nb.265"; then
	echo "  UNEXPECTED: non-MCTS stitch passed verification"; exit 1
else
	echo "  EXPECTED: -verify rejected the non-MCTS stitch (good)"
fi

echo "== Render frame 3 of the P-frame grid for inspection =="
ffmpeg -y -hide_banner -loglevel error -i "$TMP/pgrid.265" \
	-vf "select=eq(n\,3)" -frames:v 1 "$TMP/pgrid_03.png"
echo "Done."
