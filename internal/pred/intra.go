// Package pred implements HEVC intra prediction modes.
package pred

// PredictDC performs DC intra prediction for a block of the given size.
// After reference sample substitution, all neighbors are valid.
// Always sums both left and top (substituted) reference samples.
// Applies edge filtering for luma (cIdx=0) when size < 32.
// Returns prediction samples as a flat slice [size*size].
func PredictDC(size int, neighbors *Neighbors, bitDepth int, isLuma bool) []int32 {
	pred := make([]int32, size*size)

	if neighbors == nil {
		dcVal := int32(1 << (bitDepth - 1))
		for i := range pred {
			pred[i] = dcVal
		}
		return pred
	}

	// DC value: average of all left[0..size-1] and top[0..size-1]
	log2Size := 0
	for (1 << log2Size) < size {
		log2Size++
	}

	sum := int32(0)
	for i := range size {
		sum += int32(neighbors.Left[i])
		sum += int32(neighbors.Top[i])
	}
	dcVal := (sum + int32(size)) >> (log2Size + 1)

	// Fill all samples with DC
	for i := range pred {
		pred[i] = dcVal
	}

	// Edge filtering for luma only (size < 32)
	// HEVC spec 8.4.4.2.4: blend edge samples with reference samples
	if isLuma && size < 32 {
		// Top-left corner: blend with left[0] and top[0]
		pred[0] = (int32(neighbors.Left[0]) + 2*dcVal + int32(neighbors.Top[0]) + 2) >> 2
		// Top row: blend with top reference
		for x := 1; x < size; x++ {
			pred[x] = (int32(neighbors.Top[x]) + 3*dcVal + 2) >> 2
		}
		// Left column: blend with left reference
		for y := 1; y < size; y++ {
			pred[y*size] = (int32(neighbors.Left[y]) + 3*dcVal + 2) >> 2
		}
	}

	return pred
}

// PredictPlanar performs Planar intra prediction (mode 0) for a block of the given size.
// When no neighbors are available, falls back to DC-like behavior.
// After reference sample substitution, all samples in neighbors are valid.
func PredictPlanar(size int, neighbors *Neighbors, bitDepth int) []int32 {
	pred := make([]int32, size*size)

	if neighbors == nil {
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
	// After substitution, all reference samples are valid
	topRight := int32(neighbors.Top[size])
	bottomLeft := int32(neighbors.Left[size])

	for y := range size {
		for x := range size {
			top := int32(neighbors.Top[x])
			left := int32(neighbors.Left[y])

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
	// 35 entries indexed by mode 0-34
	intraPredAngle := [...]int{
		0, 0, 32, 26, 21, 17, 13, 9, 5, 2, 0, -2, -5, -9, -13, -17, -21, -26,
		-32, -26, -21, -17, -13, -9, -5, -2, 0, 2, 5, 9, 13, 17, 21, 26, 32,
	}

	// Inverse angle table for negative angles
	// 35 entries indexed by mode 0-34; only used for modes 11-25 (negative angles)
	invAngle := [...]int{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		-4096, -1638, -910, -630, -482, -390, -315,
		-256, -315, -390, -482, -630, -910, -1638, -4096,
		0, 0, 0, 0, 0, 0, 0, 0, 0,
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
				idx := -1 + ((i*invA + 128) >> 8)
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
				idx := -1 + ((i*invA + 128) >> 8)
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

// FilterRefSamples applies the HEVC spec 8.4.4.2.3 reference sample filtering
// in-place. filterFlag depends on mode, block size, and component type.
// strongSmooth enables strong intra smoothing for 32x32 luma (SPS flag).
func FilterRefSamples(n *Neighbors, mode, size int, isLuma bool, strongSmooth bool) {
	if n == nil || size <= 4 {
		return
	}

	// DC mode (1): never filter
	if mode == 1 {
		return
	}

	// Planar mode (0): always filter for size >= 8
	filterFlag := false
	if mode == 0 {
		filterFlag = true
	} else {
		// Angular modes: check distance from horizontal (10) and vertical (26)
		distH := mode - 10
		if distH < 0 {
			distH = -distH
		}
		distV := mode - 26
		if distV < 0 {
			distV = -distV
		}
		minDist := distH
		if distV < minDist {
			minDist = distV
		}

		// Table 8-3: intraHorVerDistThres
		var thresh int
		switch size {
		case 8:
			thresh = 7
		case 16:
			thresh = 1
		case 32:
			thresh = 0
		default:
			return // shouldn't happen for valid HEVC
		}
		filterFlag = minDist > thresh
	}

	if !filterFlag {
		return
	}

	nS := 2 * size // number of reference samples on each side

	// Strong intra smoothing: bilinear interpolation for 32x32 luma
	if strongSmooth && isLuma && size == 32 {
		threshold := 1 << 3 // 1 << (BitDepthY - 5) for 8-bit
		topLeft := int(n.TopLeft)
		topRight := int(n.Top[nS-1])    // p[63][-1]
		bottomLeft := int(n.Left[nS-1]) // p[-1][63]

		topMid := int(n.Top[size-1])   // p[31][-1]
		leftMid := int(n.Left[size-1]) // p[-1][31]

		topSmooth := abs(topLeft+topRight-2*topMid) < threshold
		leftSmooth := abs(topLeft+bottomLeft-2*leftMid) < threshold

		if topSmooth && leftSmooth {
			// Bilinear interpolation
			filtTop := make([]uint8, nS)
			filtLeft := make([]uint8, nS)
			for i := 0; i < nS-1; i++ {
				filtTop[i] = uint8(clip3(0, 255, (topLeft*(nS-1-i)+(i+1)*topRight+size)/(nS)))
				filtLeft[i] = uint8(clip3(0, 255, (topLeft*(nS-1-i)+(i+1)*bottomLeft+size)/(nS)))
			}
			filtTop[nS-1] = n.Top[nS-1]
			filtLeft[nS-1] = n.Left[nS-1]
			copy(n.Top, filtTop)
			copy(n.Left, filtLeft)
			// TopLeft stays unchanged
			return
		}
	}

	// Regular 3-tap [1,2,1] filter
	filtTop := make([]uint8, nS)
	filtLeft := make([]uint8, nS)

	// Corner: pF[-1][-1] = (p[-1][0] + 2*p[-1][-1] + p[0][-1] + 2) >> 2
	filtTopLeft := uint8((int(n.Left[0]) + 2*int(n.TopLeft) + int(n.Top[0]) + 2) >> 2)

	// Top row: pF[x][-1] = (p[x-1][-1] + 2*p[x][-1] + p[x+1][-1] + 2) >> 2
	filtTop[0] = uint8((int(n.TopLeft) + 2*int(n.Top[0]) + int(n.Top[1]) + 2) >> 2)
	for i := 1; i < nS-1; i++ {
		filtTop[i] = uint8((int(n.Top[i-1]) + 2*int(n.Top[i]) + int(n.Top[i+1]) + 2) >> 2)
	}
	filtTop[nS-1] = n.Top[nS-1] // last sample unfiltered

	// Left column: pF[-1][y] = (p[-1][y-1] + 2*p[-1][y] + p[-1][y+1] + 2) >> 2
	filtLeft[0] = uint8((int(n.TopLeft) + 2*int(n.Left[0]) + int(n.Left[1]) + 2) >> 2)
	for i := 1; i < nS-1; i++ {
		filtLeft[i] = uint8((int(n.Left[i-1]) + 2*int(n.Left[i]) + int(n.Left[i+1]) + 2) >> 2)
	}
	filtLeft[nS-1] = n.Left[nS-1] // last sample unfiltered

	n.TopLeft = filtTopLeft
	copy(n.Top, filtTop)
	copy(n.Left, filtLeft)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
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
