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

// Neighbors holds the reference samples for intra prediction.
type Neighbors struct {
	TopAvail bool
	LeftAvail bool
	Top      []uint8 // top neighbor samples [0..2*size-1]
	Left     []uint8 // left neighbor samples [0..2*size-1]
	TopLeft  uint8   // top-left corner sample
}
