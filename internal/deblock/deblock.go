// Package deblock implements the HEVC deblocking filter (spec 8.7.2).
package deblock

import (
	"github.com/Eyevinn/hi265/internal/slice"
	"github.com/Eyevinn/hi265/pkg/frame"
)

// HEVC spec Table 8-23: beta values indexed by qP_L + betaOffset, clipped to [0,51].
var betaTable = [52]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 20, 22, 24,
	26, 28, 30, 32, 34, 36, 38, 40, 42, 44, 46, 48, 50, 52, 54, 56,
	58, 60, 62, 64,
}

// HEVC spec Table 8-23: tC values indexed by qP_L + 2*(Bs-1) + tcOffset, clipped to [0,53].
var tcTable = [54]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 3,
	3, 3, 3, 4, 4, 4, 5, 5, 6, 6, 7, 8, 9, 10, 11, 13,
	14, 16, 18, 20, 22, 24,
}

// HEVC spec Table 8-22: chroma QP from luma QP.
var chromaQPTable = [52]int{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 29, 30,
	31, 32, 32, 33, 34, 34, 35, 35, 36, 36, 37, 37, 38, 39, 40, 41,
	42, 43, 44, 45,
}

func clip3(lo, hi, val int) int {
	if val < lo {
		return lo
	}
	if val > hi {
		return hi
	}
	return val
}

// Apply applies the HEVC deblocking filter to the reconstructed frame.
// For I-slices, Bs = 2 at all TU/CU boundary edges.
func Apply(f *frame.Frame, cus []slice.CodingUnit, sliceQPY, betaOffset, tcOffset int) {
	picW := f.Width
	picH := f.Height

	// Build edge flags on a 4x4 grid.
	// bit 0: vertical edge (left boundary of this 4x4 block)
	// bit 1: horizontal edge (top boundary of this 4x4 block)
	gridW := (picW + 3) / 4
	gridH := (picH + 3) / 4
	edgeFlags := make([]byte, gridW*gridH)

	// Build per-4x4-block QP map
	qpMap := make([]int, gridW*gridH)
	for i := range qpMap {
		qpMap[i] = sliceQPY
	}

	for _, cu := range cus {
		cuSize := 1 << cu.Log2CbSize
		markEdges(edgeFlags, gridW, gridH, cu.X0, cu.Y0, cuSize, cuSize, picW, picH)

		// Fill QP map with per-CU QP
		for y := cu.Y0 / 4; y < (cu.Y0+cuSize)/4 && y < gridH; y++ {
			for x := cu.X0 / 4; x < (cu.X0+cuSize)/4 && x < gridW; x++ {
				qpMap[y*gridW+x] = cu.QpY
			}
		}

		for _, tu := range cu.TransformUnits {
			trSize := 1 << tu.Log2TrSize
			markEdges(edgeFlags, gridW, gridH, tu.X0, tu.Y0, trSize, trSize, picW, picH)
		}
	}

	// Pass 1: Filter vertical edges (left-to-right, top-to-bottom)
	filterEdges(f, edgeFlags, gridW, gridH, qpMap, betaOffset, tcOffset, true)

	// Pass 2: Filter horizontal edges (top-to-bottom, left-to-right)
	filterEdges(f, edgeFlags, gridW, gridH, qpMap, betaOffset, tcOffset, false)
}

// markEdges sets edge flags for a block at (x0,y0) with given width/height.
func markEdges(edgeFlags []byte, gridW, gridH, x0, y0, w, h, _, _ int) {
	if x0 > 0 {
		gx := x0 / 4
		for gy := y0 / 4; gy < (y0+h)/4 && gy < gridH; gy++ {
			edgeFlags[gy*gridW+gx] |= 1
		}
	}
	if y0 > 0 {
		gy := y0 / 4
		for gx := x0 / 4; gx < (x0+w)/4 && gx < gridW; gx++ {
			edgeFlags[gy*gridW+gx] |= 2
		}
	}
}

// filterEdges filters all edges in one direction.
func filterEdges(f *frame.Frame, edgeFlags []byte, gridW, gridH int, qpMap []int, betaOffset, tcOffset int, vertical bool) {
	picW := f.Width
	picH := f.Height

	// Luma: filter on 8-pixel grid (every other 4x4 block)
	for gy := range gridH {
		for gx := range gridW {
			flags := edgeFlags[gy*gridW+gx]
			bit := byte(1) // vertical
			if !vertical {
				bit = 2 // horizontal
			}
			if flags&bit == 0 {
				continue
			}

			px := gx * 4
			py := gy * 4

			// Luma: only filter on 8-pixel grid
			if vertical && px%8 != 0 {
				continue
			}
			if !vertical && py%8 != 0 {
				continue
			}
			if px >= picW || py >= picH {
				continue
			}

			// For I-slice, Bs = 2
			bs := 2

			// QP is average of P and Q blocks
			qpQ := qpMap[gy*gridW+gx]
			var qpP int
			if vertical {
				qpP = qpMap[gy*gridW+(gx-1)]
			} else {
				qpP = qpMap[(gy-1)*gridW+gx]
			}
			qPL := (qpP + qpQ + 1) >> 1

			betaIdx := clip3(0, 51, qPL+betaOffset)
			tcIdx := clip3(0, 53, qPL+2*(bs-1)+tcOffset)
			beta := betaTable[betaIdx]
			tC := tcTable[tcIdx]

			if tC == 0 {
				continue
			}

			filterLumaEdge(f, px, py, vertical, beta, tC)
		}
	}

	// Chroma: filter on 8-pixel chroma grid = 16-pixel luma grid (4:2:0)
	chromaW := picW / 2
	chromaH := picH / 2
	for gy := range gridH {
		for gx := range gridW {
			flags := edgeFlags[gy*gridW+gx]
			bit := byte(1)
			if !vertical {
				bit = 2
			}
			if flags&bit == 0 {
				continue
			}

			px := gx * 4
			py := gy * 4

			// Chroma: only filter on 16-pixel luma grid (= 8-pixel chroma grid)
			if vertical && px%16 != 0 {
				continue
			}
			if !vertical && py%16 != 0 {
				continue
			}
			if px >= picW || py >= picH {
				continue
			}

			bs := 2
			qpQ := qpMap[gy*gridW+gx]
			var qpP int
			if vertical {
				qpP = qpMap[gy*gridW+(gx-1)]
			} else {
				qpP = qpMap[(gy-1)*gridW+gx]
			}
			qPL := (qpP + qpQ + 1) >> 1
			qPC := chromaQPFromLuma(qPL)
			tcIdx := clip3(0, 53, qPC+2*(bs-1)+tcOffset)
			tC := tcTable[tcIdx]
			if tC == 0 {
				continue
			}

			cx := px / 2
			cy := py / 2
			if cx >= chromaW || cy >= chromaH {
				continue
			}

			for comp := range 2 {
				filterChromaEdge(f, comp, cx, cy, vertical, tC)
			}
		}
	}
}

func chromaQPFromLuma(qpY int) int {
	if qpY < 0 {
		return qpY
	}
	if qpY >= 52 {
		return qpY - 6
	}
	return chromaQPTable[qpY]
}

// filterLumaEdge filters one 4-sample luma edge.
// For vertical edges: px is the column of the edge, samples are p3 p2 p1 p0 | q0 q1 q2 q3
// For horizontal edges: py is the row of the edge.
func filterLumaEdge(f *frame.Frame, px, py int, vertical bool, beta, tC int) {
	picW := f.Width
	picH := f.Height

	var p [4][4]int // p[line][distance from edge]
	var q [4][4]int

	for k := range 4 {
		for i := range 4 {
			var pX, pY, qX, qY int
			if vertical {
				pX = px - 1 - i
				pY = py + k
				qX = px + i
				qY = py + k
			} else {
				pX = px + k
				pY = py - 1 - i
				qX = px + k
				qY = py + i
			}
			if pX >= 0 && pX < picW && pY >= 0 && pY < picH {
				p[k][i] = int(f.Y[pY*f.StrideY+pX])
			}
			if qX >= 0 && qX < picW && qY >= 0 && qY < picH {
				q[k][i] = int(f.Y[qY*f.StrideY+qX])
			}
		}
	}

	// Filtering decision (HEVC spec 8.7.2.4.3)
	dp0 := abs(p[0][2] - 2*p[0][1] + p[0][0])
	dp3 := abs(p[3][2] - 2*p[3][1] + p[3][0])
	dq0 := abs(q[0][2] - 2*q[0][1] + q[0][0])
	dq3 := abs(q[3][2] - 2*q[3][1] + q[3][0])

	d := dp0 + dq0 + dp3 + dq3

	if d >= beta {
		return
	}

	// Combined strong filter decision for all 4 lines (matching FFmpeg/spec).
	// Strong filter requires conditions to pass for BOTH line 0 and line 3.
	d0 := dp0 + dq0
	d3 := dp3 + dq3
	useStrong := false
	if d0 < (beta>>2) && d3 < (beta>>2) {
		if abs(p[0][3]-p[0][0])+abs(q[0][0]-q[0][3]) < (beta>>3) &&
			abs(p[0][0]-q[0][0]) < ((5*tC+1)>>1) &&
			abs(p[3][3]-p[3][0])+abs(q[3][0]-q[3][3]) < (beta>>3) &&
			abs(p[3][0]-q[3][0]) < ((5*tC+1)>>1) {
			useStrong = true
		}
	}

	if useStrong {
		for k := range 4 {
			p0 := p[k][0]
			p1 := p[k][1]
			p2 := p[k][2]
			p3 := p[k][3]
			q0 := q[k][0]
			q1 := q[k][1]
			q2 := q[k][2]
			q3 := q[k][3]

			pNew0 := clip3(p0-2*tC, p0+2*tC, (p2+2*p1+2*p0+2*q0+q1+4)>>3)
			pNew1 := clip3(p1-2*tC, p1+2*tC, (p2+p1+p0+q0+2)>>2)
			pNew2 := clip3(p2-2*tC, p2+2*tC, (2*p3+3*p2+p1+p0+q0+4)>>3)
			qNew0 := clip3(q0-2*tC, q0+2*tC, (p1+2*p0+2*q0+2*q1+q2+4)>>3)
			qNew1 := clip3(q1-2*tC, q1+2*tC, (p0+q0+q1+q2+2)>>2)
			qNew2 := clip3(q2-2*tC, q2+2*tC, (p0+q0+q1+3*q2+2*q3+4)>>3)

			writeLumaPixel(f, px, py, k, vertical, -1, pNew0)
			writeLumaPixel(f, px, py, k, vertical, -2, pNew1)
			writeLumaPixel(f, px, py, k, vertical, -3, pNew2)
			writeLumaPixel(f, px, py, k, vertical, 0, qNew0)
			writeLumaPixel(f, px, py, k, vertical, 1, qNew1)
			writeLumaPixel(f, px, py, k, vertical, 2, qNew2)
		}
	} else {
		// Normal (weak) filter — combined dp/dq thresholds for dEp/dEq
		ndP := 1
		ndQ := 1
		if dp0+dp3 < (beta+(beta>>1))>>3 {
			ndP = 2
		}
		if dq0+dq3 < (beta+(beta>>1))>>3 {
			ndQ = 2
		}

		for k := range 4 {
			p0 := p[k][0]
			p1 := p[k][1]
			p2 := p[k][2]
			q0 := q[k][0]
			q1 := q[k][1]
			q2 := q[k][2]

			delta := clip3(-tC, tC, (9*(q0-p0)-3*(q1-p1)+8)>>4)
			if abs(delta) < tC*10 {
				pNew0 := clip3(0, 255, p0+delta)
				qNew0 := clip3(0, 255, q0-delta)
				writeLumaPixel(f, px, py, k, vertical, -1, pNew0)
				writeLumaPixel(f, px, py, k, vertical, 0, qNew0)

				if ndP > 1 {
					deltaP := clip3(-(tC>>1), tC>>1, (((p2+p0+1)>>1)-p1+delta)>>1)
					pNew1 := clip3(0, 255, p1+deltaP)
					writeLumaPixel(f, px, py, k, vertical, -2, pNew1)
				}
				if ndQ > 1 {
					deltaQ := clip3(-(tC>>1), tC>>1, (((q2+q0+1)>>1)-q1-delta)>>1)
					qNew1 := clip3(0, 255, q1+deltaQ)
					writeLumaPixel(f, px, py, k, vertical, 1, qNew1)
				}
			}
		}
	}
}

// writeLumaPixel writes a filtered luma sample.
// offset: negative = p side, 0+ = q side. -1=p0, -2=p1, etc.
func writeLumaPixel(f *frame.Frame, edgeX, edgeY, k int, vertical bool, offset, val int) {
	var x, y int
	if vertical {
		x = edgeX + offset
		y = edgeY + k
	} else {
		x = edgeX + k
		y = edgeY + offset
	}
	if x >= 0 && x < f.Width && y >= 0 && y < f.Height {
		f.Y[y*f.StrideY+x] = uint8(val)
	}
}

// filterChromaEdge filters one 4-sample chroma edge (Bs=2 only).
func filterChromaEdge(f *frame.Frame, comp, cx, cy int, vertical bool, tC int) {
	chromaW := f.Width / 2
	chromaH := f.Height / 2

	plane := f.Cb
	stride := f.StrideC
	if comp == 1 {
		plane = f.Cr
	}

	for k := range 4 {
		var p0x, p0y, p1x, p1y, q0x, q0y, q1x, q1y int
		if vertical {
			p1x = cx - 2
			p1y = cy + k
			p0x = cx - 1
			p0y = cy + k
			q0x = cx
			q0y = cy + k
			q1x = cx + 1
			q1y = cy + k
		} else {
			p1x = cx + k
			p1y = cy - 2
			p0x = cx + k
			p0y = cy - 1
			q0x = cx + k
			q0y = cy
			q1x = cx + k
			q1y = cy + 1
		}

		if p1x < 0 || p1y < 0 || q1x >= chromaW || q1y >= chromaH {
			continue
		}

		p0 := int(plane[p0y*stride+p0x])
		p1 := int(plane[p1y*stride+p1x])
		q0 := int(plane[q0y*stride+q0x])
		q1 := int(plane[q1y*stride+q1x])

		delta := clip3(-tC, tC, (4*(q0-p0)+p1-q1+4)>>3)
		plane[p0y*stride+p0x] = uint8(clip3(0, 255, p0+delta))
		plane[q0y*stride+q0x] = uint8(clip3(0, 255, q0-delta))
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
