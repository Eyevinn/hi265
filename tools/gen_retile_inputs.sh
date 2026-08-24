#!/usr/bin/env bash
# Generate one tile-ready HEVC Annex-B video from an ffmpeg lavfi source, for
# feeding to hi265retile.
#
#   gen_retile_inputs.sh <outdir> <name> <WxH> <mode> <lavfi-source> [frames] [fps]
#
#   mode = intra   : all-intra (every frame IDR), encoded with x265.
#          pframe  : IDR + low-delay P-frames, motion-constrained (MCTS),
#                    encoded with kvazaar so motion vectors + interpolation
#                    never leave the frame -> safe to use as a tile.
#
# Output: <outdir>/<name>.265 and <outdir>/<name>.yuv. The caller owns <outdir>;
# nothing is written to the repository (out/ here is the Makefile's binary
# directory, not a media directory).
#
# W and H must be multiples of the 64-luma CTB size, which is both encoders'
# default: a picture that is not CTB-aligned cannot be a tile except in the last
# tile row or column, and the failure would otherwise surface much later.
set -euo pipefail

if [ $# -lt 5 ]; then
	echo "usage: $0 <outdir> <name> <WxH> <intra|pframe> <lavfi-source> [frames] [fps]" >&2
	exit 1
fi

outdir="$1"; name="$2"; res="$3"; mode="$4"; src="$5"; frames="${6:-5}"; fps="${7:-5}"

ctb=64
w="${res%%x*}"; h="${res##*x}"
if [ $((w % ctb)) -ne 0 ] || [ $((h % ctb)) -ne 0 ]; then
	echo "$0: $res is not a multiple of the $ctb-luma CTB size; it cannot be a tile" >&2
	exit 1
fi

mkdir -p "$outdir"
yuv="$outdir/$name.yuv"; o265="$outdir/$name.265"

ffmpeg -y -hide_banner -loglevel error -f lavfi -i "$src" -frames:v "$frames" \
	-pix_fmt yuv420p -f rawvideo "$yuv"

case "$mode" in
intra)
	# All-intra: no inter prediction => no motion vectors to confine.
	x265 --input-res "$res" --fps "$fps" --frames "$frames" \
		--keyint 1 --no-open-gop --no-wpp --no-sao --profile main \
		--input "$yuv" --output "$o265" >/dev/null 2>&1
	;;
pframe)
	# IDR + low-delay P (--gop 0 --no-bipred), single reference (--ref 1),
	# motion + interpolation footprint constrained within the frame
	# (--mv-constraint frametilemargin) => a valid motion-constrained tile.
	# WPP and temporal-MVP off keep the slice headers in the simple profile
	# the retiler rewrites; --level 3.1 avoids kvazaar's level-6.2 default.
	kvazaar -i "$yuv" --input-res "$res" --input-fps "$fps" -n "$frames" \
		--preset ultrafast --gop 0 --no-bipred --ref 1 \
		--mv-constraint frametilemargin --no-wpp --no-tmvp --sao off \
		--level 3.1 -o "$o265" >/dev/null 2>&1
	;;
*)
	echo "unknown mode: $mode (want intra|pframe)" >&2; exit 1 ;;
esac

bytes=$(wc -c <"$o265")
echo "  generated $o265 ($res, $frames frames, $mode, $bytes bytes)"
