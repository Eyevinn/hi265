# HEVC/H.265 Frame Decoder in Pure Go

## Rules
- Never add "Co-Authored-By" lines to git commits or pull requests

## Project Status

Pure Go HEVC/H.265 decoder for IDR frames. Phase 1 target: decode a single
black 16x16 IDR frame, byte-exact match with FFmpeg.

### Dependencies
- `github.com/Eyevinn/mp4ff` - VPS/SPS/PPS/SliceHeader parsing, NAL extraction

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
go run ./cmd/hi265dec testdata/black_16x16.265 out.yuv
```

## Architecture

```
pkg/decoder/       - Public: top-level decoder API
pkg/frame/         - Public: Frame type (decoded output)
internal/cabac/    - Internal: CABAC arithmetic decoder (shared with H.264)
internal/context/  - Internal: HEVC context model initialization
internal/slice/    - Internal: Slice data parsing (CTU/CU/TU quadtree)
internal/transform/- Internal: HEVC inverse transform and dequantization
internal/pred/     - Internal: HEVC intra prediction
cmd/hi265dec/      - CLI: decode HEVC
testdata/          - Test bitstreams and golden references
tools/             - Test generation scripts
```
