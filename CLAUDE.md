# HEVC/H.265 Frame Decoder & Bitstream Generator in Pure Go

## Rules
- Never add "Co-Authored-By" lines to git commits or pull requests

## Project Status

Pure Go HEVC/H.265 decoder for IDR/CRA and P-skip frames, plus a bitstream
generator for producing valid HEVC test content from flat-color 16x16 CTU
grid patterns. Pixel-perfect match with FFmpeg decoding across 16+ golden
test cases (SAO, sign hiding, transform skip, deblocking, P-frames, varied QP).
Tiles and wavefront parallel processing are supported on both sides; the two
together are not, since no HEVC profile permits it. `hi265retile` stitches
several streams into one tiled picture by bitstream editing, with no re-encode,
and verifies the result with `pkg/decoder` in-process. Scaling lists are supported
for the default matrices. Everything the decoder cannot do — bit depth other than
8, chroma other than 4:2:0, PCM, transquant bypass, explicit scaling lists, the
range extension residual tools, inter pictures with motion — is refused with an
error naming the tool rather than decoded into a plausible wrong picture.

### Key Reference Files
- Standard: `references/ISO_IEC_DIS_23008-2_Ed5_2022.pdf`
- Sister project: `../hi264` (H.264 encoder/decoder)

## CLI

```bash
# Decode HEVC Annex-B to raw YUV
go run ./cmd/hi265dec input.265 output.yuv

# Generate HEVC bitstream from grid pattern
go run ./cmd/hi265gen -gp "AB,CD" -gc A=16,128,128 -gc B=235,128,128 -o test.265

# Generate a tiled picture: one independent slice segment per tile
go run ./cmd/hi265gen -gp "AB,CD" -gc A=16,128,128 -gc B=235,128,128 -w 128 -h 128 -tiles 2x2 -o tiled.265

# Generate a wavefront picture: one CABAC substream per CTU row (excludes -tiles)
go run ./cmd/hi265gen -gp "AB,CD" -gc A=16,128,128 -gc B=235,128,128 -w 128 -h 128 -wpp -o wpp.265

# Generate with a per-picture Time Code SEI (payload type 136)
go run ./cmd/hi265gen -smpte -w 192 -h 96 -n 75 -fps 25 -timecode -o timecode.265

# Generate gray IDR frame from external VPS/SPS/PPS (any chroma format/bit depth)
go run ./cmd/hi265gray -f params.json -o gray.265

# Generate a gray CRA refresh frame at a chosen POC (GDR splice point)
go run ./cmd/hi265gray -f params.json -cra -poc 42 -o gray_cra.265

# Stitch several streams into one tiled picture, and check every tile against
# its own decode (-decoder auto|hi265|ffmpeg; inter content needs ffmpeg)
go run ./cmd/hi265retile -grid 2x2 -verify -o merged.265 a.265 b.265 c.265 d.265

# Dump the NAL/SPS/PPS/slice structure of any Annex-B file
go run ./cmd/hi265inspect merged.265
```

## Retiling API

`pkg/retile` stitches N Annex-B streams into one tiled picture: a merged
SPS/PPS, one rewritten slice header per tile, CABAC payloads copied verbatim.

Inputs must share every coding tool (only picture size and level may differ),
be CTB-aligned, code one picture per slice segment, have tiles/WPP off and no
conformance window; all of that is refused with a message naming the property.
Inter inputs must additionally be motion-constrained (MCTS), which is not
visible in the bitstream — `Verify` is the only proof of it, and it needs
ffmpeg there, since `pkg/decoder` reconstructs only zero-motion skip CUs.
