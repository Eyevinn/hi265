// Package frame implements the frame buffer for HEVC/H.265 decoded pictures.
package frame

// Frame represents a decoded video frame with separate Y, Cb, Cr planes.
type Frame struct {
	Width   int // luma width in pixels
	Height  int // luma height in pixels
	Y       []uint8
	Cb      []uint8
	Cr      []uint8
	StrideY int // bytes per row for luma
	StrideC int // bytes per row for chroma
	// LumaDecoded tracks which luma pixels have been reconstructed,
	// enabling within-CTU neighbor availability for intra prediction.
	LumaDecoded []bool
}

// NewFrame creates a new frame buffer for the given dimensions.
// For 4:2:0 subsampling.
func NewFrame(width, height int) *Frame {
	// HEVC uses CTBs which can be 16, 32, or 64 pixels.
	// Align to the coded dimensions.
	codedWidth := width
	codedHeight := height
	chromaWidth := codedWidth / 2
	chromaHeight := codedHeight / 2

	return &Frame{
		Width:       width,
		Height:      height,
		Y:           make([]uint8, codedWidth*codedHeight),
		Cb:          make([]uint8, chromaWidth*chromaHeight),
		Cr:          make([]uint8, chromaWidth*chromaHeight),
		StrideY:     codedWidth,
		StrideC:     chromaWidth,
		LumaDecoded: make([]bool, codedWidth*codedHeight),
	}
}

// SetLumaPixel sets a single luma pixel.
func (f *Frame) SetLumaPixel(x, y int, val uint8) {
	f.Y[y*f.StrideY+x] = val
}

// GetLumaPixel gets a single luma pixel.
func (f *Frame) GetLumaPixel(x, y int) uint8 {
	return f.Y[y*f.StrideY+x]
}

// IsLumaDecoded returns whether the luma pixel at (x, y) has been reconstructed.
func (f *Frame) IsLumaDecoded(x, y int) bool {
	return f.LumaDecoded[y*f.StrideY+x]
}

// SetChromaPixel sets a single chroma pixel on the given component (0=Cb, 1=Cr).
func (f *Frame) SetChromaPixel(comp int, x, y int, val uint8) {
	if comp == 0 {
		f.Cb[y*f.StrideC+x] = val
	} else {
		f.Cr[y*f.StrideC+x] = val
	}
}

// GetChromaPixel gets a single chroma pixel.
func (f *Frame) GetChromaPixel(comp int, x, y int) uint8 {
	if comp == 0 {
		return f.Cb[y*f.StrideC+x]
	}
	return f.Cr[y*f.StrideC+x]
}

// SetLumaBlock sets a square luma block of the given size at pixel position (x0, y0).
func (f *Frame) SetLumaBlock(x0, y0, size int, block []int32) {
	for y := range size {
		for x := range size {
			val := block[y*size+x]
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			idx := (y0+y)*f.StrideY + x0 + x
			f.Y[idx] = uint8(val)
			f.LumaDecoded[idx] = true
		}
	}
}

// SetChromaBlock sets a square chroma block of the given size at pixel position (x0, y0).
func (f *Frame) SetChromaBlock(comp int, x0, y0, size int, block []int32) {
	plane := f.Cb
	if comp == 1 {
		plane = f.Cr
	}
	for y := range size {
		for x := range size {
			val := block[y*size+x]
			if val < 0 {
				val = 0
			} else if val > 255 {
				val = 255
			}
			plane[(y0+y)*f.StrideC+x0+x] = uint8(val)
		}
	}
}

// SetLumaDecoded marks a square region as decoded without setting pixel values.
func (f *Frame) SetLumaDecoded(x0, y0, size int) {
	for y := y0; y < y0+size && y < f.Height; y++ {
		for x := x0; x < x0+size && x < f.Width; x++ {
			f.LumaDecoded[y*f.StrideY+x] = true
		}
	}
}

// YUV420Bytes returns the frame data in I420 planar format (Y, then U, then V).
func (f *Frame) YUV420Bytes() []byte {
	lumaSize := f.Width * f.Height
	chromaSize := (f.Width / 2) * (f.Height / 2)
	result := make([]byte, lumaSize+2*chromaSize)

	// Copy luma, cropping to actual dimensions
	for y := range f.Height {
		copy(result[y*f.Width:], f.Y[y*f.StrideY:y*f.StrideY+f.Width])
	}

	// Copy Cb
	chromaW := f.Width / 2
	chromaH := f.Height / 2
	offset := lumaSize
	for y := range chromaH {
		copy(result[offset+y*chromaW:], f.Cb[y*f.StrideC:y*f.StrideC+chromaW])
	}

	// Copy Cr
	offset = lumaSize + chromaSize
	for y := range chromaH {
		copy(result[offset+y*chromaW:], f.Cr[y*f.StrideC:y*f.StrideC+chromaW])
	}

	return result
}
