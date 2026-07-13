![Logo](images/logo.png)

![Test](https://github.com/Eyevinn/hi265/workflows/Go/badge.svg)
[![Coverage Status](https://coveralls.io/repos/github/Eyevinn/hi265/badge.svg?branch=main)](https://coveralls.io/github/Eyevinn/hi265?branch=main)
[![Go Reference](https://pkg.go.dev/badge/github.com/Eyevinn/hi265.svg)](https://pkg.go.dev/github.com/Eyevinn/hi265)
[![license](https://img.shields.io/github/license/Eyevinn/hi265.svg)](https://github.com/Eyevinn/hi265/blob/main/LICENSE)
[![Badge OSC](https://img.shields.io/badge/Evaluate-24243B?style=for-the-badge&logo=data:image/svg+xml;base64,PHN2ZyB3aWR0aD0iMjQiIGhlaWdodD0iMjQiIHZpZXdCb3g9IjAgMCAyNCAyNCIgZmlsbD0ibm9uZSIgeG1sbnM9Imh0dHA6Ly93d3cudzMub3JnLzIwMDAvc3ZnIj4KPGNpcmNsZSBjeD0iMTIiIGN5PSIxMiIgcj0iMTIiIGZpbGw9InVybCgjcGFpbnQwX2xpbmVhcl8yODIxXzMxNjcyKSIvPgo8Y2lyY2xlIGN4PSIxMiIgY3k9IjEyIiByPSI3IiBzdHJva2U9ImJsYWNrIiBzdHJva2Utd2lkdGg9IjIiLz4KPGRlZnM+CjxsaW5lYXJHcmFkaWVudCBpZD0icGFpbnQwX2xpbmVhcl8yODIxXzMxNjcyIiB4MT0iMTIiIHkxPSIwIiB4Mj0iMTIiIHkyPSIyNCIgZ3JhZGllbnRVbml0cz0idXNlclNwYWNlT25Vc2UiPgo8c3RvcCBzdG9wLWNvbG9yPSIjQzE4M0ZGIi8+CjxzdG9wIG9mZnNldD0iMSIgc3RvcC1jb2xvcj0iIzREQzlGRiIvPgo8L2xpbmVhckdyYWRpZW50Pgo8L2RlZnM+Cjwvc3ZnPgo=)](https://app.osaas.io/browse/eyevinn-mp4ff)

## Pure Go HEVC/H.265 IDR Decoder & Bitstream Generator

A pure Go HEVC/H.265 decoder for IDR and P-skip frames, plus a bitstream
generator for producing valid HEVC test content from flat-color 16x16 CTU
patterns. Sister project to [hi264](https://github.com/Eyevinn/hi264) (H.264/AVC).

This is **not** a general-purpose video encoder — it does not accept arbitrary
pixel input or perform motion estimation. The encoder produces intra DC
prediction frames where each CTU is a single flat color, defined by a grid
pattern. This is useful for generating test bitstreams, color bars, frame
counters, and reference content for decoder verification.

Decoding is 8-bit 4:2:0 only. The gray IDR generator (`hi265gray`) supports
any chroma format and bit depth (4:2:0, 4:2:2, 4:4:4; 8-bit, 10-bit, 12-bit).

Pixel-perfect match with FFmpeg decoding across 16+ golden test cases covering
SAO, sign hiding, transform skip, deblocking, P-frames, varied QP ranges, and
complex content.

## Build & Test

```bash
go build ./...
go test ./...
```

## CLI Tools

### hi265dec — Decode HEVC from raw .265

```bash
# Decode HEVC Annex-B to raw YUV
go run ./cmd/hi265dec input.265 output.yuv
```

### hi265gen — HEVC bitstream generator for test content

Generates valid HEVC/H.265 bitstreams from grid-based patterns. Each character
in a grid maps to one 16x16 CTU filled with a single flat color, encoded as
intra with DC prediction. This is not a general-purpose encoder — it produces
test content from color patterns, not from arbitrary video frames.

Output formats:

| Extension | Format | Notes |
|---|---|---|
| `.265`/`.hevc` | Annex-B | Raw HEVC bitstream |
| `.mp4` | Fragmented MP4 | fMP4/CMAF with configurable fps and fragment duration |
| `.y4m` | Y4M | YUV4MPEG2 container |
| `.yuv` | Raw YUV | 4:2:0 planar |
| `.png` | PNG | Raw grid output (no HEVC encoding) |
| `.jpg` | JPEG | Raw grid output (`-q` for quality, default 85) |

Multi-frame sequences use P-skip frames
between IDR keyframes to copy the reference frame unchanged. Image formats
(YUV, Y4M, PNG, JPEG) output the grid pattern directly without HEVC encoding,
useful as reference images for decoder verification.

The grid and color systems are shared with
[hi264](https://github.com/Eyevinn/hi264) via its `pkg/yuv` package, so the
same `.gridimg` files and color specs work with both tools.

```bash
# Grid-only: single IDR frame from grid pattern (frame size = grid size)
go run ./cmd/hi265gen -f examples/logo.gridimg -o logo.265
go run ./cmd/hi265gen -grid "xy,yx" -c x=235,128,128 -c y=16,128,128 -o checker.265
go run ./cmd/hi265gen -grid "ab" -c a=255,0,0 -c b=0,0,255 -rgb -qp 20 -o test.265

# Counter: frame counter digits on solid background
go run ./cmd/hi265gen -w 192 -h 96 -n 10 -digits 3 -o counter.265

# With P-skip frames (IDR every 50 frames, P-skip copies between)
go run ./cmd/hi265gen -w 192 -h 96 -n 100 -digits 3 -idr-interval 50 -o counter.265

# Fragmented MP4 output (25 fps default, fragment every 25 frames)
go run ./cmd/hi265gen -w 192 -h 96 -n 50 -digits 3 -o counter.mp4

# MP4 with custom framerate and fragment duration
go run ./cmd/hi265gen -w 320 -h 240 -n 75 -digits 3 -fps 30 -frag-dur 30 -o counter.mp4

# Tiled: grid pattern tiled to fill custom dimensions, with optional counter
go run ./cmd/hi265gen -grid "xy,yx" -c x=235,128,128 -c y=16,128,128 -w 192 -h 96 -n 10 -digits 3 -o counter.265

# SMPTE color bars with counter overlay
go run ./cmd/hi265gen -smpte -w 192 -h 96 -n 10 -digits 3 -o smpte.265

# SMPTE bars with digit background box and explicit scale
go run ./cmd/hi265gen -smpte -w 352 -h 288 -n 1 -digits 2 -digit-scale 3 -digit-bg 0,0,0 -o smpte_big.265

# Fixed bytes per picture (pad with HEVC filler NALUs for CBR-like streams)
go run ./cmd/hi265gen -smpte -w 192 -h 96 -bpp 5000 -o padded.265
go run ./cmd/hi265gen -w 320 -h 240 -n 50 -digits 3 -bpp 8000 -o cbr_counter.mp4

# Raw image output (no HEVC encoding, useful as decoder reference)
go run ./cmd/hi265gen -f examples/logo.gridimg -o logo.png
go run ./cmd/hi265gen -f examples/logo.gridimg -o logo.yuv
go run ./cmd/hi265gen -f examples/logo.gridimg -q 95 -o logo.jpg
go run ./cmd/hi265gen -w 192 -h 96 -n 5 -digits 3 -o output.y4m
go run ./cmd/hi265gen -w 192 -h 96 -n 5 -digits 3 -o frame_%03d.png
```

```bash
# Color space: generate BT.709 stream (VUI signaled in SPS)
go run ./cmd/hi265gen -f examples/logo.gridimg -colorspace bt709 -o logo_709.265

# Full-range BT.709
go run ./cmd/hi265gen -smpte -w 320 -h 240 -colorspace bt709 -full-range -o smpte_709.265
```

Flags:

| Flag | Description | Default |
|---|---|---|
| `-f` | Grid image file (`.gridimg`) | — |
| `-grid` | Inline grid string (e.g. `"xy,yx"`) | — |
| `-c` | Color mapping (repeatable, e.g. `x=235,128,128`) | — |
| `-rgb` | Treat `-c` values as RGB instead of YCbCr | off |
| `-smpte` | Use built-in 75% SMPTE color bars pattern | off |
| `-w` | Frame width in pixels | grid width |
| `-h` | Frame height in pixels | grid height |
| `-n` | Number of frames | 1 |
| `-digits` | Counter digit count (0 = no counter) | 0 |
| `-digit-scale` | Digit scale factor (0 = auto-fit) | 0 |
| `-digit-bg` | Digit background box color (R,G,B) | none |
| `-fg` | Foreground color (R,G,B) | — |
| `-bg` | Background color (R,G,B) | — |
| `-qp` | Quantization parameter | 26 |
| `-q` | JPEG quality | 85 |
| `-idr-interval` | Frames between IDR keyframes (0 = all-IDR) | 0 |
| `-bpp` | Bytes per picture (filler NAL padding) | 0 (off) |
| `-colorspace` | Color space (`bt601`/`bt709`/`bt2020`) | `bt601` |
| `-full-range` | Full-range YCbCr (0-255) | off (limited) |
| `-fps` | MP4 framerate | 25 |
| `-frag-dur` | MP4 fragment duration in frames | 25 |
| `-o` | Output file | — |

### Constant bitrate testing with `-bpp`

The `-bpp` flag pads each picture to an exact byte count using HEVC filler data
NAL units (NAL type 38). This is useful for testing bitrate-sensitive scenarios
such as ABR ladder switching, buffer management, and segment size constraints.

The target bitrate in kbit/s is: `bpp * 8 * fps / 1000`. For example, `-bpp 5000`
at 25 fps gives 1000 kbit/s. An error is returned if a frame's encoded slice
already exceeds the target (use a higher QP or larger `-bpp` value).

A practical pattern is to use different background colors or patterns for
different bitrate tiers so the current quality level is visually obvious during
playback:

```bash
# 500 kbit/s tier — green background
go run ./cmd/hi265gen -w 320 -h 240 -n 50 -digits 3 -bg 0,128,0 -bpp 2500 -o low.mp4

# 1500 kbit/s tier — blue background
go run ./cmd/hi265gen -w 640 -h 360 -n 50 -digits 3 -bg 0,0,200 -bpp 7500 -o mid.mp4

# 3000 kbit/s tier — red background
go run ./cmd/hi265gen -w 1280 -h 720 -n 50 -digits 3 -bg 200,0,0 -bpp 15000 -o high.mp4
```

This makes it easy to verify that an ABR player switches between the correct
renditions — you can tell which bitrate tier is active just by looking at the
background color.

## Image File Format

The `.gridimg` format combines color definitions and a grid layout in one file.
This format is shared with [hi264](https://github.com/Eyevinn/hi264) — the same
files work with both `hi264gen` and `hi265gen`.

```
# Comments start with #
@rgb
@bt709
# Colors: char=v1,v2,v3 (YCbCr by default, RGB with @rgb directive or -rgb flag)
B=0,106,167
Y=254,204,0

BBBBBYYBBBBBBBBB
BBBBBYYBBBBBBBBB
YYYYYYYYYYYYYYYY
YYYYYYYYYYYYYYYY
BBBBBYYBBBBBBBBB
BBBBBYYBBBBBBBBB
```

Each character in the grid maps to one 16x16 CTU. Supported directives:
`@rgb` (treat values as RGB), `@bt601`/`@bt709`/`@bt2020` (color space for
RGB-to-YCbCr conversion). See `examples/` for complete examples.

## Example Patterns

The `examples/` directory contains `.gridimg` files:

| File | Description | Size (CTUs) |
|---|---|---|
| `logo.gridimg` | hi265 logo: SMPTE bars with text | 48x27 |

```bash
# Encode to HEVC
go run ./cmd/hi265gen -f examples/logo.gridimg -o logo.265

# Decode to raw YUV
go run ./cmd/hi265dec logo.265 logo.yuv

# Generate reference PNG for comparison (raw output, no HEVC)
go run ./cmd/hi265gen -f examples/logo.gridimg -o expected.png

# Cross-verify with FFmpeg (raw YUV)
go run ./cmd/hi265dec logo.265 logo.yuv
ffmpeg -i logo.265 -pix_fmt yuv420p -f rawvideo ff.yuv
cmp logo.yuv ff.yuv  # should be identical
```

### hi265gray — Gray IDR frame generator for GDR streams

Generates a uniform mid-gray IDR frame given external VPS, SPS, and PPS
parameter sets. Intended for bootstrapping decoders in Gradual Decode Refresh
(GDR) streams that lack IDR frames.

The gray frame uses DC prediction with zero residual, making it independent of
chroma format and bit depth — it works with 4:2:0, 4:2:2, 4:4:4 at any bit
depth. Each CTU is encoded as the largest possible CU (no quadtree splitting),
producing compact bitstreams (~275 bytes for 1920x1080, 2x smaller than x265).
The output is an Annex-B bitstream containing VPS + SPS + PPS + IDR slice.

```bash
# From a JSON parameter set file (extracts first VPS/SPS/PPS)
go run ./cmd/hi265gray -f params.json -o gray.265

# From hex strings
go run ./cmd/hi265gray -vps 40010c... -sps 4201... -pps 4401... -o gray.265
```

The input file (`-f`) is a JSON object with `vps`, `sps`, and `pps` hex strings:

```json
{"vps": "40010c01ffff...", "sps": "4201012408...", "pps": "4401c0764c..."}
```

Test parameter sets are included in `cmd/hi265gray/testdata/`:

| File | Format | Notes |
|---|---|---|
| `vps_sps_pps_420_8bit.json` | 4:2:0 8-bit | 128x64, CTU=64 |
| `vps_sps_pps_420_10bit.json` | 4:2:0 10-bit | 1920x1080, CTU=64 |
| `vps_sps_pps_422_10bit.json` | 4:2:2 10-bit | 1920x1080, CTU=64 |

## Library Usage

The `pkg/` packages provide a public API for use as a Go library. Implementation
details are in `internal/` and not accessible to external callers.

```go
import (
    "github.com/Eyevinn/hi265/pkg/decoder"
    "github.com/Eyevinn/hi265/pkg/encode"
    "github.com/Eyevinn/hi264/pkg/yuv"
)

// Decode an Annex-B byte stream (e.g. .265 file contents)
dec := decoder.New()
frames, err := dec.DecodeAnnexB(data)

// Generate HEVC from grid pattern (each cell = one 16x16 CTU)
grid, _ := yuv.ParseGrid("AB,CD")
colors := yuv.ColorMap{'A': yuv.Color{16, 128, 128}, 'B': yuv.Color{128, 128, 128}}
p := encode.EncodeParams{Width: 32, Height: 32, QP: 26}
vpsSPSPPS, _ := encode.GenerateVPSSPSPPS(p)
idrSlice, _ := encode.GenerateIDR(p, grid, colors)
annexB := append(vpsSPSPPS, idrSlice...)

// Generate a P-skip frame (copies IDR content unchanged)
pSkip, _ := encode.GeneratePSkip(p, 1)
annexB = append(annexB, pSkip...)

// Encode IDR and P-skip slices compatible with external SPS/PPS
// (e.g., parameter sets from an MP4 sample description or third-party encoder)
sps, _ := hevc.ParseSPSNALUnit(spsNALU)
pps, _ := hevc.ParsePPSNALUnit(ppsNALU, spsMap)
idrSlice, err := encode.EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)
pSkipSlice, err := encode.EncodePSkipSliceFromSPSPPS(sps, pps, poc)

// Generate a gray IDR frame from external SPS/PPS (any chroma format / bit depth)
// Useful for bootstrapping GDR streams that lack IDR frames.
grayIDR, err := encode.EncodeGrayIDRSliceFromSPSPPS(sps, pps)
```

### Appending frames to an existing bitstream

This example parses VPS/SPS/PPS from an existing HEVC bitstream, then appends
a black IDR frame and a P-skip frame that are compatible with the original
parameter sets:

```go
import (
    "github.com/Eyevinn/hi264/pkg/yuv"
    "github.com/Eyevinn/hi265/pkg/encode"
    "github.com/Eyevinn/mp4ff/avc"
    "github.com/Eyevinn/mp4ff/hevc"
)

// Parse parameter sets from the existing bitstream
nalus := avc.ExtractNalusFromByteStream(existingStream)
spsMap := make(map[uint32]*hevc.SPS)
var sps *hevc.SPS
var pps *hevc.PPS
for _, nalu := range nalus {
    if len(nalu) < 2 {
        continue
    }
    switch hevc.GetNaluType(nalu[0]) {
    case hevc.NALU_SPS:
        sps, _ = hevc.ParseSPSNALUnit(nalu)
        spsMap[uint32(sps.SpsID)] = sps
    case hevc.NALU_PPS:
        pps, _ = hevc.ParsePPSNALUnit(nalu, spsMap)
    }
}

// Create a black grid matching the SPS dimensions (each cell = one 16x16 CTU)
w := int(sps.PicWidthInLumaSamples)
h := int(sps.PicHeightInLumaSamples)
blackY := uint8(16)  // limited range black
if sps.VUI != nil && sps.VUI.VideoFullRangeFlag {
    blackY = 0       // full range black
}
grid, colors := yuv.SolidGrid(w, h, yuv.Color{blackY, 128, 128})

// Encode a black IDR slice using the existing parameter sets
idrSlice, _ := encode.EncodeIDRSliceFromSPSPPS(sps, pps, grid, colors)

// Encode a P-skip slice (copies the IDR frame unchanged)
pSkipSlice, _ := encode.EncodePSkipSliceFromSPSPPS(sps, pps, 2) // POC=2

// Append to the original stream
stream := append(existingStream, idrSlice...)
stream = append(stream, pSkipSlice...)
```

## Architecture

```
pkg/decoder/       — Public: top-level decoder API (DecodeAnnexB)
pkg/encode/        — Public: bitstream generator API (GenerateIDR, GeneratePSkip, FrameEncoder)
pkg/frame/         — Public: Frame type (decoded output)
internal/cabac/    — Internal: CABAC arithmetic decoder and encoder engines
internal/context/  — Internal: Context model initialization (170 contexts)
internal/slice/    — Internal: Slice data parsing, CTU/CU/TU quadtree
internal/transform/— Internal: Inverse quantization and transform (4x4, 8x8, 16x16)
internal/pred/     — Internal: Intra prediction modes (planar, DC, angular)
internal/deblock/  — Internal: Deblocking filter
internal/sao/      — Internal: Sample Adaptive Offset
cmd/hi265dec/      — CLI: decode HEVC from raw .265
cmd/hi265gen/      — CLI: generate HEVC bitstreams or raw images from grid patterns
cmd/hi265gray/     — CLI: generate gray IDR frames from external VPS/SPS/PPS
examples/          — Example grid image files
tools/             — Test generation scripts
testdata/          — Golden HEVC bitstreams for regression testing
```

## Related Projects

- [hi264](https://github.com/Eyevinn/hi264) — Sister project: pure Go H.264/AVC frame decoder & bitstream generator

## Dependencies

- [`github.com/Eyevinn/mp4ff`](https://github.com/Eyevinn/mp4ff) — VPS/SPS/PPS/SliceHeader parsing, MP4 container, HEVC descriptor, fragmented MP4 creation
- [`github.com/Eyevinn/hi264`](https://github.com/Eyevinn/hi264) — Grid/color/SMPTE/counter utilities (`pkg/yuv`), frame type (`pkg/frame`)

## Support

Join our [community on Slack](http://slack.streamingtech.se) where you can post any questions regarding any of our open source projects. Eyevinn's consulting business can also offer you:

* Further development of this component
* Customization and integration of this component into your platform
* Support and maintenance agreement

Contact [sales@eyevinn.se](mailto:sales@eyevinn.se) if you are interested.

## About Eyevinn Technology

[Eyevinn Technology](https://www.eyevinntechnology.se) is an independent consultant firm specialized in video and streaming. Independent in a way that we are not commercially tied to any platform or technology vendor. As our way to innovate and push the industry forward we develop proof-of-concepts and tools. The things we learn and the code we write we share with the industry in [blogs](https://dev.to/video) and by open sourcing the code we have written.

Want to know more about Eyevinn and how it is to work here. Contact us at work@eyevinn.se!
