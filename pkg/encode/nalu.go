package encode

import "io"

// HEVC NALU types
const (
	naluTrailR    = 1
	naluIDRWRadl  = 19
	naluVPS       = 32
	naluSPS       = 33
	naluPPS       = 34
)

// writeNALU writes a complete HEVC NALU to w: start code + 2-byte header + EBSP.
// HEVC NALU header (2 bytes):
//
//	byte 0: 0 | nal_unit_type(6) | nuh_layer_id_bit5(1)
//	byte 1: nuh_layer_id[4:0](5) | nuh_temporal_id_plus1(3)
//
// For our use: layer_id=0, temporal_id_plus1=1.
func writeNALU(w io.Writer, naluType int, rbsp []byte) {
	// Start code
	w.Write([]byte{0x00, 0x00, 0x00, 0x01})

	// 2-byte NALU header
	b0 := byte((naluType & 0x3F) << 1) // forbidden_zero_bit=0, nal_unit_type, nuh_layer_id bit5=0
	b1 := byte(1)                       // nuh_layer_id[4:0]=0, nuh_temporal_id_plus1=1
	w.Write([]byte{b0, b1})

	// RBSP with emulation prevention bytes
	w.Write(insertEBSP(rbsp))
}

// insertEBSP inserts emulation prevention bytes (0x03) after 0x00 0x00
// when the next byte is 0x00, 0x01, 0x02, or 0x03.
func insertEBSP(rbsp []byte) []byte {
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
