package encode

import (
	"fmt"
	"io"
)

// HEVC NALU types
const (
	naluTrailR    = 1
	naluIDRWRadl  = 19
	naluCRA       = 21
	naluVPS       = 32
	naluSPS       = 33
	naluPPS       = 34
	NaluFiller    = 38
	naluPrefixSEI = 39
)

// WriteNALU writes a complete HEVC NALU to w: start code + 2-byte header + EBSP.
// HEVC NALU header (2 bytes):
//
//	byte 0: 0 | nal_unit_type(6) | nuh_layer_id_bit5(1)
//	byte 1: nuh_layer_id[4:0](5) | nuh_temporal_id_plus1(3)
//
// For our use: layer_id=0, temporal_id_plus1=1.
func WriteNALU(w io.Writer, naluType int, rbsp []byte) {
	// Start code
	_, _ = w.Write([]byte{0x00, 0x00, 0x00, 0x01})

	// 2-byte NALU header
	b0 := byte((naluType & 0x3F) << 1) // forbidden_zero_bit=0, nal_unit_type, nuh_layer_id bit5=0
	b1 := byte(1)                      // nuh_layer_id[4:0]=0, nuh_temporal_id_plus1=1
	_, _ = w.Write([]byte{b0, b1})

	// RBSP with emulation prevention bytes
	_, _ = w.Write(InsertEBSP(rbsp))
}

// buildNALU returns a raw NALU (2-byte header + EBSP, no start code) for MP4 use.
func buildNALU(naluType int, rbsp []byte) []byte {
	b0 := byte((naluType & 0x3F) << 1)
	b1 := byte(1)
	ebsp := InsertEBSP(rbsp)
	nalu := make([]byte, 2+len(ebsp))
	nalu[0] = b0
	nalu[1] = b1
	copy(nalu[2:], ebsp)
	return nalu
}

// InsertEBSP inserts emulation prevention bytes (0x03) after 0x00 0x00
// when the next byte is 0x00, 0x01, 0x02, or 0x03.
func InsertEBSP(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+len(rbsp)/256+1)
	zeroCount := 0
	for _, b := range rbsp {
		if zeroCount >= 2 && b <= 0x03 {
			out = append(out, 0x03)
			zeroCount = 0
		}
		out = append(out, b)
		if b == 0 {
			zeroCount++
		} else {
			zeroCount = 0
		}
	}
	return out
}

// FillerNALU returns a filler NALU of the exact given byte size (including start code).
// Minimum size is 7 bytes (4 start code + 2 header + 1 trailing 0x80).
func FillerNALU(size int) ([]byte, error) {
	minSize := 7
	if size < minSize {
		return nil, fmt.Errorf("filler NALU minimum size is %d bytes, got %d", minSize, size)
	}
	buf := make([]byte, size)
	// Start code
	buf[0] = 0x00
	buf[1] = 0x00
	buf[2] = 0x00
	buf[3] = 0x01
	// 2-byte HEVC NALU header for filler (type 38)
	buf[4] = byte((NaluFiller & 0x3F) << 1)
	buf[5] = 0x01
	// Fill payload with 0xFF
	for i := 6; i < size-1; i++ {
		buf[i] = 0xFF
	}
	// Trailing byte
	buf[size-1] = 0x80
	return buf, nil
}

// PadSlice appends a filler NALU to slice to reach targetBytes.
// If slice is already >= targetBytes, it is returned unchanged.
func PadSlice(slice []byte, targetBytes int) ([]byte, error) {
	if len(slice) >= targetBytes {
		return slice, nil
	}
	fillerSize := targetBytes - len(slice)
	filler, err := FillerNALU(fillerSize)
	if err != nil {
		return nil, fmt.Errorf("cannot pad: slice is %d bytes, target is %d, filler needs %d (min 7): %w",
			len(slice), targetBytes, fillerSize, err)
	}
	return append(slice, filler...), nil
}
