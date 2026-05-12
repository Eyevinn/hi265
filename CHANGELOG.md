# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-05-12

Initial release of hi265 — a pure Go HEVC/H.265 frame decoder and bitstream
generator. Sister project to [hi264](https://github.com/Eyevinn/hi264).

### Added

#### Decoder (hi265dec)
- CABAC arithmetic engine and 170 context models (I-slice and P-slice init)
- CTU/CU/TU quadtree parsing with intra and inter (skip) prediction
- All 35 HEVC intra prediction modes (planar, DC, angular)
- Inverse quantization and transform (4x4, 8x8, 16x16 DCT)
- Deblocking filter (luma and chroma)
- Sample Adaptive Offset (SAO) — band offset and edge offset
- Sign data hiding and transform skip
- P-frame decoding (all-skip CUs with zero-motion copy)
- cu_qp_delta support
- Auto-detection of Annex-B input
- Raw YUV output

#### Encoder (hi265gen)
- HEVC IDR frame encoder with DC intra prediction (Main profile, CABAC)
- P-skip slice encoder (all-skip CUs copying from reference)
- Forward DCT and HM-compatible quantization (QP 0-51)
- VPS/SPS/PPS generation with VUI colour description
  (BT.601/BT.709/BT.2020, full/limited range)
- `FrameEncoder` type with grid-based API using hi264's `pkg/yuv` package
- Grid-based pattern input with RGB or YCbCr color specification
- `.gridimg` file support with `@rgb`, `@bt709`, `@bt2020` directives
- SMPTE 75% color bars, frame counter overlay, tiled backgrounds
- P-skip frame sequences with configurable IDR interval
- Filler NALUs for fixed bytes-per-picture output
- Multiple output formats: `.265`/`.hevc`, `.mp4`, `.y4m`, `.yuv`, `.png`, `.jpg`
- Fragmented MP4 (fMP4/CMAF) output with HEVC descriptor

#### Gray IDR generator (hi265gray)
- hi265gray CLI and `EncodeGrayIDRSliceFromSPSPPS` API for generating uniform
  mid-gray IDR frames from external VPS/SPS/PPS. Supports any chroma format
  (4:2:0, 4:2:2, 4:4:4) and bit depth (8, 10, 12). Uses DC prediction with
  zero residual and largest-possible CU encoding (~275 bytes for 1920x1080,
  ~40 us on Apple M4 Pro). Intended for bootstrapping GDR streams that lack
  IDR frames.

#### Verification
- 16 golden decoder test cases with pixel-perfect FFmpeg match
- Encoder round-trip tests (encode → decode byte-exact match)
- Multi-resolution gray IDR test covering 12 resolutions (64x64 to 1920x1088)
  with CTU=32 and CTU=64, verified pixel-perfect against FFmpeg decode
- Benchmark for gray IDR generation at 1920x1080 in three formats
  (4:2:0 8-bit, 4:2:0 10-bit, 4:2:2 10-bit)

[Unreleased]: https://github.com/Eyevinn/hi265/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Eyevinn/hi265/releases/tag/v0.1.0
