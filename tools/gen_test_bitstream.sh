#!/bin/bash
# Generate minimal HEVC test bitstreams and golden YUV references.
#
# Requires ffmpeg built with libx265. Note that many ffmpeg builds are not —
# check with `ffmpeg -encoders | grep libx265`. Where it is missing, drive the
# `x265` binary directly and convert with ffmpeg afterwards; the tests that
# generate their own vectors (TestDecodeRealContent, TestDecodeWPPStreams,
# TestDeblockExactness) do exactly that and are the better model for new cases.
#
# The committed vectors under testdata/ were made with the parameters below.
# Feature flags are switched off here to isolate what each vector tests, not
# because the decoder needs them off: wavefront parallel processing, SAO, sign
# data hiding and deblocking are all supported and verified bit-exact against
# FFmpeg. Leave a flag on when the vector is meant to cover it.
set -euo pipefail

TESTDATA="$(cd "$(dirname "$0")/../testdata" && pwd)"

echo "=== Generating black 16x16 HEVC bitstream ==="
ffmpeg -y -f lavfi -i color=c=black:s=16x16:d=0.04 -frames:v 1 -c:v libx265 \
  -x265-params "keyint=1:min-keyint=1:no-open-gop=1:ctu=16:min-cu-size=8:qp=26:\
no-sao=1:no-deblock=1:no-signhide=1:no-info=1" \
  -f hevc "$TESTDATA/black_16x16.265"

echo "=== Generating golden YUV reference ==="
ffmpeg -y -i "$TESTDATA/black_16x16.265" \
  -f rawvideo -pix_fmt yuv420p "$TESTDATA/golden/black_16x16.yuv"

echo "=== Bitstream info ==="
ls -la "$TESTDATA/black_16x16.265"
xxd "$TESTDATA/black_16x16.265" | head -10

echo "=== Golden YUV info ==="
ls -la "$TESTDATA/golden/black_16x16.yuv"
xxd "$TESTDATA/golden/black_16x16.yuv" | head -5

echo "Done."
