// Package pred implements HEVC intra prediction modes.
package pred

// PredictDC performs DC intra prediction for a block of the given size.
// When no neighboring samples are available (first CTU), the prediction
// value is 1 << (bitDepth - 1) = 128 for 8-bit.
// Returns prediction samples as a flat slice [size*size].
func PredictDC(size int, neighbors *Neighbors, bitDepth int) []int32 {
	pred := make([]int32, size*size)
	var dcVal int32

	if neighbors == nil || (!neighbors.LeftAvail && !neighbors.TopAvail) {
		// No neighbors: use mid-range value
		dcVal = int32(1 << (bitDepth - 1))
	} else {
		sum := int32(0)
		count := 0
		if neighbors.LeftAvail {
			for i := range size {
				sum += int32(neighbors.Left[i])
				count++
			}
		}
		if neighbors.TopAvail {
			for i := range size {
				sum += int32(neighbors.Top[i])
				count++
			}
		}
		dcVal = (sum + int32(count/2)) / int32(count)
	}

	for i := range pred {
		pred[i] = dcVal
	}
	return pred
}

// PredictPlanar performs Planar intra prediction (mode 0) for a block of the given size.
// When no neighbors are available, falls back to DC-like behavior.
func PredictPlanar(size int, neighbors *Neighbors, bitDepth int) []int32 {
	pred := make([]int32, size*size)

	if neighbors == nil || (!neighbors.LeftAvail && !neighbors.TopAvail) {
		dcVal := int32(1 << (bitDepth - 1))
		for i := range pred {
			pred[i] = dcVal
		}
		return pred
	}

	log2Size := 0
	for (1 << log2Size) < size {
		log2Size++
	}

	// p[-1][nTbS] (top-right) and p[nTbS][-1] (bottom-left)
	var topRight, bottomLeft int32
	if neighbors.TopAvail && len(neighbors.Top) > size {
		topRight = int32(neighbors.Top[size])
	} else if neighbors.TopAvail {
		topRight = int32(neighbors.Top[size-1])
	} else {
		topRight = int32(1 << (bitDepth - 1))
	}

	if neighbors.LeftAvail && len(neighbors.Left) > size {
		bottomLeft = int32(neighbors.Left[size])
	} else if neighbors.LeftAvail {
		bottomLeft = int32(neighbors.Left[size-1])
	} else {
		bottomLeft = int32(1 << (bitDepth - 1))
	}

	for y := range size {
		for x := range size {
			var top, left int32
			if neighbors.TopAvail {
				top = int32(neighbors.Top[x])
			} else {
				top = int32(1 << (bitDepth - 1))
			}
			if neighbors.LeftAvail {
				left = int32(neighbors.Left[y])
			} else {
				left = int32(1 << (bitDepth - 1))
			}

			pred[y*size+x] = ((int32(size)-1-int32(x))*left +
				(int32(x)+1)*topRight +
				(int32(size)-1-int32(y))*top +
				(int32(y)+1)*bottomLeft +
				int32(size)) >> (log2Size + 1)
		}
	}
	return pred
}

// PredictAngular performs angular intra prediction (modes 2-34) per HEVC spec 8.4.4.2.6.
// Modes 2-17 are primarily horizontal, modes 18-34 are primarily vertical.
// Mode 10 = exact horizontal, mode 26 = exact vertical.
func PredictAngular(mode, size int, neighbors *Neighbors, bitDepth int) []int32 {
	p := make([]int32, size*size)

	if neighbors == nil {
		dcVal := int32(1 << (bitDepth - 1))
		for i := range p {
			p[i] = dcVal
		}
		return p
	}

	// Intra prediction angle table (spec Table 8-4)
	intraPredAngle := [...]int{
		0, 0, 32, 26, 21, 17, 13, 9, 5, 2, 0, -2, -5, -9, -13, -17, -21, -26, -32,
		-32, -26, -21, -17, -13, -9, -5, -2, 0, 2, 5, 9, 13, 17, 21, 26, 32,
	}

	// Inverse angle table for negative angles
	invAngle := [...]int{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		-4096, -1638, -910, -630, -482, -390, -315,
		-315, -390, -482, -630, -910, -1638, -4096,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}

	angle := intraPredAngle[mode]

	// Use offset to allow negative indices (for negative angle projection)
	off := size
	ref := make([]int32, 3*size+1+off)

	if mode >= 18 {
		// Vertical modes: main reference is top row
		// ref[0] = topLeft, ref[1..2*size] = top[0..2*size-1]
		ref[off+0] = int32(neighbors.TopLeft)
		for i := range 2 * size {
			if i < len(neighbors.Top) {
				ref[off+i+1] = int32(neighbors.Top[i])
			}
		}

		// If angle is negative, extend ref with projected left samples
		if angle < 0 {
			invA := invAngle[mode]
			nProjected := (size * angle) >> 5
			for i := nProjected; i < 0; i++ {
				idx := -1 + ((i * invA + 128) >> 8)
				if idx >= 0 && idx < len(neighbors.Left) {
					ref[off+i] = int32(neighbors.Left[idx])
				}
			}
		}

		for y := range size {
			iIdx := ((y + 1) * angle) >> 5
			iFact := ((y + 1) * angle) & 31
			for x := range size {
				refIdx := off + x + iIdx + 1
				if iFact != 0 {
					p[y*size+x] = (int32(32-iFact)*ref[refIdx] + int32(iFact)*ref[refIdx+1] + 16) >> 5
				} else {
					p[y*size+x] = ref[refIdx]
				}
			}
		}
	} else {
		// Horizontal modes (2-17): main reference is left column
		// ref[0] = topLeft, ref[1..2*size] = left[0..2*size-1]
		ref[off+0] = int32(neighbors.TopLeft)
		for i := range 2 * size {
			if i < len(neighbors.Left) {
				ref[off+i+1] = int32(neighbors.Left[i])
			}
		}

		// If angle is negative, extend ref with projected top samples
		if angle < 0 {
			invA := invAngle[mode]
			nProjected := (size * angle) >> 5
			for i := nProjected; i < 0; i++ {
				idx := -1 + ((i * invA + 128) >> 8)
				if idx >= 0 && idx < len(neighbors.Top) {
					ref[off+i] = int32(neighbors.Top[idx])
				}
			}
		}

		// For horizontal modes, swap x and y in the formula
		for y := range size {
			for x := range size {
				iIdx := ((x + 1) * angle) >> 5
				iFact := ((x + 1) * angle) & 31
				refIdx := off + y + iIdx + 1
				if iFact != 0 {
					p[y*size+x] = (int32(32-iFact)*ref[refIdx] + int32(iFact)*ref[refIdx+1] + 16) >> 5
				} else {
					p[y*size+x] = ref[refIdx]
				}
			}
		}
	}

	return p
}

// Neighbors holds the reference samples for intra prediction.
type Neighbors struct {
	TopAvail  bool
	LeftAvail bool
	Top       []uint8 // top neighbor samples [0..2*size-1]
	Left      []uint8 // left neighbor samples [0..2*size-1]
	TopLeft   uint8   // top-left corner sample
}
