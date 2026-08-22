package cabac

// Encoder is the CABAC arithmetic encoding engine, the inverse of Decoder.
// The CABAC arithmetic engine is identical between H.264 and HEVC — same
// rangeTabLPS, transIdxMPS/LPS tables, same algorithm.
type Encoder struct {
	codILow         uint32 // lower bound of the arithmetic coding interval
	codIRange       uint16 // interval range
	firstBitFlag    bool   // first bit not yet emitted
	bitsOutstanding int    // pending carry bits
	buf             []byte // output bytes
	curByte         byte   // accumulates bits MSB-first
	bitPos          uint   // bits written in curByte (0-7)
}

// NewEncoder creates a new CABAC encoder with initial state per spec.
func NewEncoder() *Encoder {
	return &Encoder{
		codILow:      0,
		codIRange:    510,
		firstBitFlag: true,
		buf:          make([]byte, 0, 256),
	}
}

// EncodeDecision encodes a single binary decision using the given context model.
func (e *Encoder) EncodeDecision(binVal uint8, ctx *CtxState) {
	qCodIRangeIdx := (e.codIRange >> 6) & 3
	codIRangeLPS := rangeTabLPS[ctx.PStateIdx][qCodIRangeIdx]
	e.codIRange -= codIRangeLPS

	if binVal != ctx.ValMPS {
		// LPS path
		e.codILow += uint32(e.codIRange)
		e.codIRange = codIRangeLPS

		if ctx.PStateIdx == 0 {
			ctx.ValMPS = 1 - ctx.ValMPS
		}
		ctx.PStateIdx = transIdxLPS[ctx.PStateIdx]
	} else {
		// MPS path
		ctx.PStateIdx = transIdxMPS[ctx.PStateIdx]
	}

	e.renormalize()
}

// EncodeBypass encodes a single binary decision using equiprobable mode.
func (e *Encoder) EncodeBypass(binVal uint8) {
	e.codILow <<= 1
	if binVal != 0 {
		e.codILow += uint32(e.codIRange)
	}

	if e.codILow >= 1024 {
		e.putBit(1)
		e.codILow -= 1024
	} else if e.codILow < 512 {
		e.putBit(0)
	} else {
		e.codILow -= 512
		e.bitsOutstanding++
	}
}

// EncodeTerminate encodes the end_of_slice_flag.
func (e *Encoder) EncodeTerminate(binVal uint8) {
	e.codIRange -= 2
	if binVal != 0 {
		e.codILow += uint32(e.codIRange)
		e.codIRange = 2
		e.renormalize()
		e.putBit(uint8((e.codILow >> 9) & 1))
		v := ((e.codILow >> 7) & 3) | 1
		e.writeBitDirect(uint8((v >> 1) & 1))
		e.writeBitDirect(uint8(v & 1))
	} else {
		e.renormalize()
	}
}

// Flush finalizes the CABAC bitstream and returns the encoded bytes, zero-padded
// to a whole number of bytes.
//
// It is called after the terminating bin that ends a substream, whose flush
// (spec 9.3.4.3.5) already put a forced one bit at the end of the arithmetic
// coder's output. That bit is what byte_alignment() calls
// alignment_bit_equal_to_one at the end of a wavefront row or a tile, and what
// rbsp_trailing_bits() calls rbsp_stop_one_bit at the end of the slice segment;
// either way the only thing left to write is the zero padding.
func (e *Encoder) Flush() []byte {
	if e.bitPos > 0 {
		e.buf = append(e.buf, e.curByte)
	}
	return e.buf
}

// putBit outputs a bit with carry propagation.
func (e *Encoder) putBit(b uint8) {
	if e.firstBitFlag {
		e.firstBitFlag = false
	} else {
		e.writeBitDirect(b)
	}
	ob := uint8(1 - b)
	for e.bitsOutstanding > 0 {
		e.writeBitDirect(ob)
		e.bitsOutstanding--
	}
}

// renormalize performs the encoder renormalization loop.
func (e *Encoder) renormalize() {
	for e.codIRange < 256 {
		if e.codILow < 256 {
			e.putBit(0)
		} else if e.codILow >= 512 {
			e.putBit(1)
			e.codILow -= 512
		} else {
			e.codILow -= 256
			e.bitsOutstanding++
		}
		e.codIRange <<= 1
		e.codILow <<= 1
	}
}

// writeBitDirect appends a single bit to the output buffer, MSB first.
func (e *Encoder) writeBitDirect(b uint8) {
	e.curByte |= (b & 1) << (7 - e.bitPos)
	e.bitPos++
	if e.bitPos == 8 {
		e.buf = append(e.buf, e.curByte)
		e.curByte = 0
		e.bitPos = 0
	}
}
