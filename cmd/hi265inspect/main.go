// Command hi265inspect dumps the NAL structure of an HEVC Annex-B file, parsing
// VPS/SPS/PPS and slice headers via mp4ff to reveal exactly what must be
// rewritten for tile stitching.
package main

import (
	"fmt"
	"os"

	"github.com/Eyevinn/hi265/pkg/retile"
	"github.com/Eyevinn/mp4ff/hevc"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hi265inspect file.265")
		os.Exit(1)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	nalus := retile.SplitAnnexB(data)

	spsMap := map[uint32]*hevc.SPS{}
	ppsMap := map[uint32]*hevc.PPS{}

	for i, n := range nalus {
		t := hevc.GetNaluType(n[0])
		fmt.Printf("[%2d] %-18s len=%d\n", i, t.String(), len(n))
		switch t {
		case hevc.NALU_SPS:
			sps, err := hevc.ParseSPSNALUnit(n)
			if err != nil {
				fmt.Println("     SPS parse err:", err)
				continue
			}
			spsMap[uint32(sps.SpsID)] = sps
			ctb := 1 << (sps.Log2MinLumaCodingBlockSizeMinus3 + 3 + sps.Log2DiffMaxMinLumaCodingBlockSize)
			fmt.Printf("     id=%d %dx%d profile=%d level=%d chroma=%d bitdepth=%d CTB=%d maxSubLayersM1=%d\n",
				sps.SpsID, sps.PicWidthInLumaSamples, sps.PicHeightInLumaSamples,
				sps.ProfileTierLevel.GeneralProfileIDC, sps.ProfileTierLevel.GeneralLevelIDC, sps.ChromaFormatIDC,
				sps.BitDepthLumaMinus8+8, ctb, sps.MaxSubLayersMinus1)
			fmt.Printf("     SAO=%v scalingList=%v strongIntraSmooth=%v tmvp=%v pcm=%v confWin=%v\n",
				sps.SampleAdaptiveOffsetEnabledFlag, sps.ScalingListEnabledFlag,
				sps.StrongIntraSmoothingEnabledFlag, sps.SpsTemporalMvpEnabledFlag,
				sps.PCMEnabledFlag, sps.ConformanceWindowFlag)
		case hevc.NALU_PPS:
			pps, err := hevc.ParsePPSNALUnit(n, spsMap)
			if err != nil {
				fmt.Println("     PPS parse err:", err)
				continue
			}
			ppsMap[pps.PicParameterSetID] = pps
			fmt.Printf("     id=%d sps=%d initQpM26=%d tiles=%v entropySync=%v signHide=%v cabacInit=%v\n",
				pps.PicParameterSetID, pps.SeqParameterSetID, pps.InitQpMinus26,
				pps.TilesEnabledFlag, pps.EntropyCodingSyncEnabledFlag,
				pps.SignDataHidingEnabledFlag, pps.CabacInitPresentFlag)
			fmt.Printf("     dependentSlices=%v outputFlagPresent=%v numExtraSliceHdrBits=%d"+
				" cuQpDelta=%v transformSkip=%v\n",
				pps.DependentSliceSegmentsEnabledFlag, pps.OutputFlagPresentFlag,
				pps.NumExtraSliceHeaderBits, pps.CuQpDeltaEnabledFlag, pps.TransformSkipEnabledFlag)
			fmt.Printf("     deblkCtrlPresent=%v deblkOverride=%v deblkDisabled=%v"+
				" loopFilterAcrossSlices=%v sliceChromaQpOff=%v\n",
				pps.DeblockingFilterControlPresentFlag, pps.DeblockingFilterOverrideEnabledFlag,
				pps.DeblockingFilterDisabledFlag, pps.LoopFilterAcrossSlicesEnabledFlag,
				pps.SliceChromaQpOffsetsPresentFlag)
			fmt.Printf("     listsModif=%v sliceHdrExt=%v extPresent=%v scalingListData=%v\n",
				pps.ListsModificationPresentFlag, pps.SliceSegmentHeaderExtensionPresentFlag,
				pps.ExtensionPresentFlag, pps.ScalingListDataPresentFlag)
		default:
			if t >= hevc.NALU_TRAIL_N && t <= hevc.NALU_CRA {
				sh, err := hevc.ParseSliceHeader(n, spsMap, ppsMap)
				if err != nil {
					fmt.Println("     slice parse err:", err)
					continue
				}
				fmt.Printf("     type=%s firstSlice=%v segAddr=%d pps=%d qpDelta=%d"+
					" sao(l=%v,c=%v) hdrSizeBytes=%d numEntryPts=%d\n",
					sh.SliceType, sh.FirstSliceSegmentInPicFlag, sh.SegmentAddress,
					sh.PicParameterSetId, sh.QpDelta, sh.SaoLumaFlag, sh.SaoChromaFlag,
					sh.Size, sh.NumEntryPointOffsets)
			}
		}
	}
}
