// Package pred implements HEVC intra prediction. This file holds the reference
// sample construction that both the decoder and the encoder need: they must
// agree on it exactly, since a sample the encoder predicts from and the decoder
// does not is a mismatch neither side can detect.
package pred

// BuildRefSamples extracts and substitutes reference samples for intra prediction
// per HEVC spec section 8.4.4.2.2.
func BuildRefSamples(x0, y0, size, picW, picH int,
	getPixel func(x, y int) uint8, isDecoded func(x, y int) bool) *Neighbors {

	// A neighbouring sample is available when it is inside the picture and has
	// already been reconstructed *by this slice segment* (spec 6.4.1: a
	// neighbour in another slice or another tile is unavailable, whatever its
	// address). The reconstruction map is cleared at the start of every segment,
	// so asking it is the whole test — an earlier segment's samples sit in the
	// same buffer but read as undecoded, which is exactly what tiles need.
	isAvailable := func(px, py int) bool {
		if px < 0 || py < 0 || px >= picW || py >= picH {
			return false
		}
		return isDecoded(px, py)
	}

	hasAny := isAvailable(x0-1, y0) || isAvailable(x0, y0-1)
	if !hasAny {
		return nil
	}

	totalSamples := 4*size + 1
	ref := make([]uint8, totalSamples)
	avail := make([]bool, totalSamples)

	for i := range 2 * size {
		y := y0 + 2*size - 1 - i
		if isAvailable(x0-1, y) {
			ref[i] = getPixel(x0-1, y)
			avail[i] = true
		}
	}

	tlIdx := 2 * size
	if isAvailable(x0-1, y0-1) {
		ref[tlIdx] = getPixel(x0-1, y0-1)
		avail[tlIdx] = true
	}

	for i := range 2 * size {
		x := x0 + i
		if isAvailable(x, y0-1) {
			ref[tlIdx+1+i] = getPixel(x, y0-1)
			avail[tlIdx+1+i] = true
		}
	}

	firstAvail := -1
	for i := range totalSamples {
		if avail[i] {
			firstAvail = i
			break
		}
	}
	if firstAvail < 0 {
		return nil
	}

	for i := range firstAvail {
		ref[i] = ref[firstAvail]
	}
	for i := firstAvail + 1; i < totalSamples; i++ {
		if !avail[i] {
			ref[i] = ref[i-1]
		}
	}

	n := &Neighbors{
		TopAvail:  true,
		LeftAvail: true,
		Top:       make([]uint8, 2*size),
		Left:      make([]uint8, 2*size),
	}

	for i := range 2 * size {
		n.Left[i] = ref[2*size-1-i]
	}
	n.TopLeft = ref[tlIdx]
	copy(n.Top, ref[tlIdx+1:])

	return n
}
