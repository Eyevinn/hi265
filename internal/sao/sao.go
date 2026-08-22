// Package sao implements the HEVC Sample Adaptive Offset filter (spec 8.7.3).
package sao

import (
	"github.com/Eyevinn/hi265/internal/loopfilter"
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// Apply applies SAO filtering to the reconstructed frame after deblocking.
// SAO reads from the pre-SAO reconstructed picture, so we make copies of each plane.
//
// bounds says which neighbouring samples the edge offset mode may look at: one
// in another tile, or in another slice that disallows filtering across its
// boundary, is unavailable and leaves the sample unfiltered (spec 8.7.3.2). It
// must not be nil.
func Apply(f *frame.Frame, saoParams []slice.SaoParams, log2CtbSize int,
	bounds *loopfilter.Boundaries) {
	ctbSize := 1 << log2CtbSize
	ctbsX := (f.Width + ctbSize - 1) / ctbSize
	ctbsY := (f.Height + ctbSize - 1) / ctbSize

	// Copy original planes for reading (SAO writes to f, reads from originals)
	origY := make([]uint8, len(f.Y))
	copy(origY, f.Y)
	origCb := make([]uint8, len(f.Cb))
	copy(origCb, f.Cb)
	origCr := make([]uint8, len(f.Cr))
	copy(origCr, f.Cr)

	for ctbAddrRS := range ctbsX * ctbsY {
		cx := (ctbAddrRS % ctbsX) * ctbSize
		cy := (ctbAddrRS / ctbsX) * ctbSize
		sao := saoParams[ctbAddrRS]

		// Luma
		if sao.TypeIdx[0] != 0 {
			w := min(ctbSize, f.Width-cx)
			h := min(ctbSize, f.Height-cy)
			applyCTU(origY, f.Y, f.StrideY, f.Width, f.Height, cx, cy, w, h,
				sao.TypeIdx[0], sao.Offsets[0], sao.BandPos[0], sao.EoClass[0],
				bounds.CanFilterLuma)
		}

		// Chroma
		chromaCtbSize := ctbSize / 2
		chromaCX := cx / 2
		chromaCY := cy / 2
		chromaW := f.Width / 2
		chromaH := f.Height / 2

		for comp := range 2 {
			cIdx := comp + 1
			if sao.TypeIdx[cIdx] == 0 {
				continue
			}
			var origPlane, dstPlane []uint8
			if comp == 0 {
				origPlane = origCb
				dstPlane = f.Cb
			} else {
				origPlane = origCr
				dstPlane = f.Cr
			}
			w := min(chromaCtbSize, chromaW-chromaCX)
			h := min(chromaCtbSize, chromaH-chromaCY)
			applyCTU(origPlane, dstPlane, f.StrideC, chromaW, chromaH, chromaCX, chromaCY, w, h,
				sao.TypeIdx[cIdx], sao.Offsets[cIdx], sao.BandPos[cIdx], sao.EoClass[cIdx],
				bounds.CanFilterChroma)
		}
	}
}

// applyCTU applies SAO to a single CTU region on a single plane.
// orig is the pre-SAO plane (read), dst is the output plane (write).
// canFilter reports whether a neighbouring sample may be read, in this plane's
// own coordinates.
func applyCTU(orig, dst []uint8, stride, picW, picH, x0, y0, w, h int,
	typeIdx int, offsets [4]int, bandPos, eoClass int,
	canFilter func(xA, yA, xB, yB int) bool) {

	if typeIdx == 1 {
		// Band offset reads no neighbours, so no boundary can restrict it.
		applyBandOffset(orig, dst, stride, x0, y0, w, h, offsets, bandPos)
	} else {
		applyEdgeOffset(orig, dst, stride, picW, picH, x0, y0, w, h, offsets, eoClass, canFilter)
	}
}

// applyBandOffset applies SAO Band Offset to a CTU region.
func applyBandOffset(orig, dst []uint8, stride, x0, y0, w, h int, offsets [4]int, bandPos int) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			val := int(orig[y*stride+x])
			band := val >> 3 // 32 bands, each 8 values wide
			idx := band - bandPos
			if idx >= 0 && idx < 4 {
				dst[y*stride+x] = uint8(clip(0, 255, val+offsets[idx]))
			}
		}
	}
}

// Edge offset direction vectors per eoClass:
// 0=horizontal, 1=vertical, 2=135-degree diagonal, 3=45-degree diagonal
var eoDX = [4]int{1, 0, 1, -1}
var eoDY = [4]int{0, 1, 1, 1}

// applyEdgeOffset applies SAO Edge Offset to a CTU region.
func applyEdgeOffset(orig, dst []uint8, stride, picW, picH, x0, y0, w, h int,
	offsets [4]int, eoClass int, canFilter func(xA, yA, xB, yB int) bool) {

	dx := eoDX[eoClass]
	dy := eoDY[eoClass]

	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			nx1 := x + dx
			ny1 := y + dy
			nx2 := x - dx
			ny2 := y - dy

			if nx1 < 0 || nx1 >= picW || ny1 < 0 || ny1 >= picH ||
				nx2 < 0 || nx2 >= picW || ny2 < 0 || ny2 >= picH {
				continue
			}
			// Either neighbour being across a boundary the filter may not reach
			// across leaves this sample alone (spec 8.7.3.2). Both directions
			// matter: the class is a line through the sample, so one end can sit
			// in the tile to the left and the other in the tile to the right.
			if !canFilter(x, y, nx1, ny1) || !canFilter(x, y, nx2, ny2) {
				continue
			}

			val := int(orig[y*stride+x])
			n1 := int(orig[ny1*stride+nx1])
			n2 := int(orig[ny2*stride+nx2])

			// Edge index per spec 8.7.3.3
			edgeIdx := sign(val-n1) + sign(val-n2) + 2
			if edgeIdx != 2 {
				var offset int
				switch edgeIdx {
				case 0:
					offset = offsets[0]
				case 1:
					offset = offsets[1]
				case 3:
					offset = offsets[2]
				case 4:
					offset = offsets[3]
				}
				dst[y*stride+x] = uint8(clip(0, 255, val+offset))
			}
		}
	}
}

func sign(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func clip(lo, hi, val int) int {
	if val < lo {
		return lo
	}
	if val > hi {
		return hi
	}
	return val
}
