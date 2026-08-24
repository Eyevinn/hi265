package encode

// BitReader reads bits MSB-first from a byte slice holding an already
// de-escaped RBSP. It is the counterpart of BitWriter, and exists for the
// bit-splicing that tile stitching does: locate a syntax element, copy the
// bits before it verbatim, write a replacement, copy the rest.
//
// Deliberately not mp4ff's bits.EBSPReader: that one strips emulation
// prevention as it reads, which is wrong here because the caller has already
// de-escaped exactly the header region it wants to splice and must keep the
// payload boundary. This reader also exposes an absolute bit position (Pos)
// and SkipBits, which the splice needs and EBSPReader does not offer.
// Do not "simplify" this away.
type BitReader struct {
	data []byte
	pos  int // absolute bit position
}

// NewBitReader creates a BitReader over an RBSP byte slice.
func NewBitReader(data []byte) *BitReader { return &BitReader{data: data} }

// Pos returns the current absolute bit position.
func (r *BitReader) Pos() int { return r.pos }

// ReadBit reads a single bit. Reading past the end returns 0.
func (r *BitReader) ReadBit() uint {
	byteIdx := r.pos >> 3
	if byteIdx >= len(r.data) {
		r.pos++
		return 0
	}
	bit := uint(r.data[byteIdx]>>(7-(r.pos&7))) & 1
	r.pos++
	return bit
}

// ReadBits reads n bits as an unsigned value.
func (r *BitReader) ReadBits(n int) uint {
	var v uint
	for range n {
		v = (v << 1) | r.ReadBit()
	}
	return v
}

// ReadUE reads an unsigned Exp-Golomb value.
func (r *BitReader) ReadUE() uint {
	zeros := 0
	for r.ReadBit() == 0 {
		zeros++
		if zeros > 64 {
			return 0
		}
	}
	if zeros == 0 {
		return 0
	}
	return (1 << zeros) - 1 + r.ReadBits(zeros)
}

// ReadSE reads a signed Exp-Golomb value.
func (r *BitReader) ReadSE() int {
	ue := r.ReadUE()
	if ue&1 == 1 {
		return int((ue + 1) / 2)
	}
	return -int(ue / 2)
}

// SkipBits advances the position by n bits.
func (r *BitReader) SkipBits(n int) { r.pos += n }
