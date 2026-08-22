# HEVC/H.265 Frame Decoder & Bitstream Generator in Pure Go

## Rules
- Never add "Co-Authored-By" lines to git commits or pull requests

## Project Status

Pure Go HEVC/H.265 decoder for IDR and P-skip frames, plus a bitstream
generator for producing valid HEVC test content from flat-color 16x16 CTU
grid patterns. Pixel-perfect match with FFmpeg decoding across 16+ golden
test cases (SAO, sign hiding, transform skip, deblocking, P-frames, varied QP).

### Dependencies
- `github.com/Eyevinn/mp4ff` — VPS/SPS/PPS parsing, NAL extraction, MP4 container
- `github.com/Eyevinn/hi264` — Grid/color/SMPTE utilities (`pkg/yuv`), frame type

### Key Reference Files
- Standard: `references/ISO_IEC_DIS_23008-2_Ed5_2022.pdf`
- Sister project: `../hi264` (H.264 encoder/decoder)

## Build & Test

```bash
go build ./...
go test ./...
```

### CLI

```bash
# Decode HEVC Annex-B to raw YUV
go run ./cmd/hi265dec input.265 output.yuv

# Generate HEVC bitstream from grid pattern
go run ./cmd/hi265gen -gp "AB,CD" -gc A=16,128,128 -gc B=235,128,128 -o test.265

# Generate a tiled picture: one independent slice segment per tile
go run ./cmd/hi265gen -gp "AB,CD" -gc A=16,128,128 -gc B=235,128,128 -w 128 -h 128 -tiles 2x2 -o tiled.265

# Generate with a per-picture Time Code SEI (payload type 136)
go run ./cmd/hi265gen -smpte -w 192 -h 96 -n 75 -fps 25 -timecode -o timecode.265

# Generate gray IDR frame from external VPS/SPS/PPS (any chroma format/bit depth)
go run ./cmd/hi265gray -f params.json -o gray.265

# Generate a gray CRA refresh frame at a chosen POC (GDR splice point)
go run ./cmd/hi265gray -f params.json -cra -poc 42 -o gray_cra.265
```

## Encoder API

Grid-based functions that produce Annex-B HEVC NALUs from flat-color CTU patterns:

- `GenerateVPSSPSPPS(p)` — VPS + SPS + PPS NALUs
- `GenerateIDR(p, grid, colors)` — IDR slice NALU
- `GeneratePSkip(p, poc)` — P-skip slice NALU

External SPS/PPS support (for injecting frames into existing streams):

- `EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)` — IDR slice compatible with external parameter sets
- `EncodePSkipSliceFromSPSPPS(sps, pps, poc)` — P-skip slice compatible with external parameter sets
- `EncodeGrayIDRSliceFromSPSPPS(sps, pps)` — Gray IDR slice (any chroma format/bit depth)
- `EncodeCRASliceFromSPSPPS(sps, pps, grid, colors, poc)` — CRA slice at a chosen POC (no POC reset)
- `EncodeGrayCRASliceFromSPSPPS(sps, pps, poc)` — Gray CRA slice, the GDR refresh primitive

`FrameEncoder` wraps these functions with a struct-based API for convenience.

## Architecture

```
pkg/decoder/       — Public: top-level decoder API (DecodeAnnexB)
pkg/encode/        — Public: bitstream generator API (GenerateIDR, GeneratePSkip, FrameEncoder)
pkg/frame/         — Public: Frame type (decoded output)
pkg/timecode/      — Public: SMPTE timecode arithmetic and text formatting
internal/cabac/    — Internal: CABAC arithmetic decoder and encoder engines
internal/context/  — Internal: Context model initialization (170 contexts)
internal/slice/    — Internal: Slice data parsing, CTU/CU/TU quadtree
internal/tiles/    — Internal: Tile geometry and tile scan tables (spec 6.5.1)
internal/transform/— Internal: Inverse quantization and transform (4x4, 8x8, 16x16)
internal/pred/     — Internal: Intra prediction modes (planar, DC, angular)
internal/deblock/  — Internal: Deblocking filter
internal/sao/      — Internal: Sample Adaptive Offset
internal/loopfilter/ — Internal: Tile/slice boundaries the loop filters stop at
cmd/hi265dec/      — CLI: decode HEVC from Annex-B or MP4 to YUV/Y4M/PNG/JPEG
cmd/hi265gen/      — CLI: generate HEVC bitstreams or raw images from grid patterns
cmd/hi265gray/     — CLI: generate gray IDR/CRA frames from external VPS/SPS/PPS
testdata/          — Test bitstreams and golden references
tools/             — Test generation scripts
```
