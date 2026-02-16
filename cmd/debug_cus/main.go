package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/hi265/internal/slice"
)

func main() {
	data, _ := os.ReadFile(os.Args[1])
	nalus := avc.ExtractNalusFromByteStream(data)
	spsMap := make(map[uint32]*hevc.SPS)
	ppsMap := make(map[uint32]*hevc.PPS)

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		t := hevc.GetNaluType(nalu[0])
		switch t {
		case hevc.NALU_SPS:
			sps, _ := hevc.ParseSPSNALUnit(nalu)
			spsMap[uint32(sps.SpsID)] = sps
		case hevc.NALU_PPS:
			pps, _ := hevc.ParsePPSNALUnit(nalu, spsMap)
			ppsMap[pps.PicParameterSetID] = pps
		case hevc.NALU_IDR_N_LP, hevc.NALU_IDR_W_RADL:
			r := bits.NewEBSPReader(bytes.NewReader(nalu))
			r.Read(16)
			r.ReadFlag()
			r.ReadFlag()
			ppsID := r.ReadExpGolomb()
			pps := ppsMap[uint32(ppsID)]
			sps := spsMap[pps.SeqParameterSetID]
			r.ReadExpGolomb()
			qpDelta := r.ReadSignedGolomb()
			sliceQPY := 26 + int(pps.InitQpMinus26) + qpDelta
			deblockDisabled := pps.DeblockingFilterDisabledFlag
			if pps.DeblockingFilterOverrideEnabledFlag {
				if r.ReadFlag() {
					deblockDisabled = r.ReadFlag()
					if !deblockDisabled {
						r.ReadSignedGolomb()
						r.ReadSignedGolomb()
					}
				}
			}
			if pps.LoopFilterAcrossSlicesEnabledFlag && (!deblockDisabled || sps.SampleAdaptiveOffsetEnabledFlag) {
				r.ReadFlag()
			}
			r.Read(1)
			bitsInByte := r.NrBitsRead() % 8
			if bitsInByte != 0 {
				r.Read(8 - bitsInByte)
			}
			headerSize := r.NrBytesRead()
			cabacData := removeEPB(nalu[headerSize:])

			picW := int(sps.PicWidthInLumaSamples)
			picH := int(sps.PicHeightInLumaSamples)
			log2CtbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3 + int(sps.Log2DiffMaxMinLumaCodingBlockSize)
			log2MinTrSize := int(sps.Log2MinLumaTransformBlockSizeMinus2) + 2
			log2MaxTrSize := log2MinTrSize + int(sps.Log2DiffMaxMinLumaTransformBlockSize)
			log2MinCbSize := int(sps.Log2MinLumaCodingBlockSizeMinus3) + 3

			sd, err := slice.DecodeSliceData(cabacData, slice.Params{
				SliceType:                       2, // I-slice
				SliceQPY:                        sliceQPY,
				PicWidth:                        picW,
				PicHeight:                       picH,
				Log2CtbSize:                     log2CtbSize,
				Log2MinCbSize:                   log2MinCbSize,
				Log2MinTrafoSize:                log2MinTrSize,
				Log2MaxTrafoSize:                log2MaxTrSize,
				MaxTransformHierarchyDepthIntra: int(sps.MaxTransformHierarchyDepthIntra),
				Trace:                           true,
			})
			if err != nil {
				fmt.Println("Decode error:", err)
				return
			}
			_ = sd
		}
	}
}

func removeEPB(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 3 {
			out = append(out, 0, 0)
			i += 3
		} else {
			out = append(out, data[i])
			i++
		}
	}
	return out
}
