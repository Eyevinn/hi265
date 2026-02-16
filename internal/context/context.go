// Package context implements HEVC CABAC context model initialization.
// Per HEVC spec section 9.3.2.2, each context model is initialized from
// an initValue via: slopeIdx = initValue >> 4, offsetIdx = initValue & 15,
// m = slopeIdx*5 - 45, n = offsetIdx*8 - 16,
// preCtxState = Clip3(1, 126, ((m * Clip3(0,51,SliceQpY)) >> 4) + n)
package context

import (
	"github.com/Eyevinn/hi265/internal/cabac"
)

// HEVC context model indices for syntax elements.
// Aligned with HM reference decoder ContextTables.h layout.
const (
	// split_cu_flag: 3 contexts
	CtxSplitCuFlag = 0
	// cu_skip_flag: 3 contexts (unused in I-slice)
	CtxCuSkipFlag = 3
	// pred_mode_flag: 1 context (unused in I-slice)
	CtxPredModeFlag = 6
	// part_mode: 4 contexts (only first used for I-slice)
	CtxPartMode = 7
	// prev_intra_luma_pred_flag: 1 context
	CtxPrevIntraLumaPredFlag = 11
	// intra_chroma_pred_mode: 1 context
	CtxIntraChromaPredMode = 12
	// merge_flag: 1 context (unused in I-slice)
	CtxMergeFlag = 13
	// cbf_luma: 2 contexts
	CtxCbfLuma = 14
	// cbf_chroma (shared by cbf_cb and cbf_cr): 5 contexts
	CtxCbfCb = 16
	CtxCbfCr = 16 // same contexts as cbf_cb per HEVC spec
	// split_transform_flag: 3 contexts
	CtxSplitTransformFlag = 21
	// last_sig_coeff_x_prefix: 30 contexts (15 luma + 15 chroma)
	CtxLastSigCoeffXPrefix = 24
	// last_sig_coeff_y_prefix: 30 contexts (15 luma + 15 chroma)
	CtxLastSigCoeffYPrefix = 54
	// coded_sub_block_flag: 4 contexts
	CtxCodedSubBlockFlag = 84
	// sig_coeff_flag: 44 contexts (28 luma + 16 chroma)
	CtxSigCoeffFlag = 88
	// coeff_abs_level_greater1: 24 contexts
	CtxCoeffAbsLevelGreater1 = 132
	// coeff_abs_level_greater2: 6 contexts
	CtxCoeffAbsLevelGreater2 = 156
	// cu_transquant_bypass_flag: 1 context
	CtxCuTransquantBypassFlag = 162
	// transform_skip_flag: 2 contexts
	CtxTransformSkipFlag = 163
	// sao_merge_flag: 1 context
	CtxSaoMergeFlag = 165
	// sao_type_idx: 1 context
	CtxSaoTypeIdx = 166
	// merge_idx: 1 context (unused in I-slice)
	CtxMergeIdx = 167
	// cu_qp_delta_abs: 2 contexts
	CtxCuQpDeltaAbs = 168

	// Total context count
	NumContextModels = 170
)

// CNU is "Context Not Used" — default initValue for unused contexts.
const cnu = 154

// lastSigCoeffInitValuesI has 30 values: 15 luma + 15 chroma (I-slice).
var lastSigCoeffInitValuesI = [30]uint8{
	// luma (15)
	110, 110, 124, 125, 140, 153, 125, 127, 140, 109, 111, 143, 127, 111, 79,
	// chroma (15)
	108, 123, 63, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu,
}

// lastSigCoeffInitValuesP has 30 values: 15 luma + 15 chroma (P-slice).
var lastSigCoeffInitValuesP = [30]uint8{
	// luma (15)
	125, 110, 94, 110, 95, 79, 125, 111, 110, 78, 110, 111, 111, 95, 94,
	// chroma (15)
	108, 123, 108, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu, cnu,
}

// Slice type constants per HEVC spec.
const (
	SliceTypeB = 0
	SliceTypeP = 1
	SliceTypeI = 2
)

// initValuesISlice contains the initValue (0-255) for each context model
// for I-slice. From HM reference decoder ContextTables.h, I_SLICE index.
var initValuesISlice [NumContextModels]uint8

// initValuesPSlice contains the initValue (0-255) for each context model
// for P-slice. From FFmpeg hevc_cabac.c, init_type=1.
var initValuesPSlice [NumContextModels]uint8

func init() {
	// === I-slice init values ===
	idx := 0
	// split_cu_flag (3): INIT_SPLIT_FLAG
	copy(initValuesISlice[idx:], []uint8{139, 141, 157})
	idx = CtxCuSkipFlag
	// cu_skip_flag (3): unused in I-slice
	copy(initValuesISlice[idx:], []uint8{cnu, cnu, cnu})
	idx = CtxPredModeFlag
	// pred_mode_flag (1): unused in I-slice
	initValuesISlice[idx] = cnu
	idx = CtxPartMode
	// part_mode (4): INIT_PART_SIZE
	copy(initValuesISlice[idx:], []uint8{184, cnu, cnu, cnu})
	idx = CtxPrevIntraLumaPredFlag
	// prev_intra_luma_pred_flag (1): INIT_INTRA_PRED_MODE
	initValuesISlice[idx] = 184
	idx = CtxIntraChromaPredMode
	// intra_chroma_pred_mode (1): INIT_CHROMA_PRED_MODE
	initValuesISlice[idx] = 63
	idx = CtxMergeFlag
	// merge_flag (1): unused in I-slice
	initValuesISlice[idx] = cnu
	idx = CtxCbfLuma
	// cbf_luma (2): INIT_QT_CBF[0:2]
	copy(initValuesISlice[idx:], []uint8{111, 141})
	idx = CtxCbfCb
	// cbf_chroma (5): INIT_QT_CBF[5:10]
	copy(initValuesISlice[idx:], []uint8{94, 138, 182, 154, 154})
	idx = CtxSplitTransformFlag
	// split_transform_flag (3): INIT_TRANS_SUBDIV_FLAG
	copy(initValuesISlice[idx:], []uint8{153, 138, 138})
	idx = CtxLastSigCoeffXPrefix
	// last_sig_coeff_x_prefix (30): same init as Y
	copy(initValuesISlice[idx:], lastSigCoeffInitValuesI[:])
	idx = CtxLastSigCoeffYPrefix
	// last_sig_coeff_y_prefix (30): same init values, separate state
	copy(initValuesISlice[idx:], lastSigCoeffInitValuesI[:])
	idx = CtxCodedSubBlockFlag
	// coded_sub_block_flag (4): INIT_SIG_CG_FLAG
	copy(initValuesISlice[idx:], []uint8{91, 171, 134, 141})
	idx = CtxSigCoeffFlag
	// sig_coeff_flag (44): INIT_SIG_FLAG
	// Luma: ctxIdx 0-26 (27 values), Chroma: ctxIdx 27-41 (15 values), Skip: ctxIdx 42-43 (2 values)
	copy(initValuesISlice[idx:], []uint8{
		111, 111, 125, 110, 110, 94, 124, 108, 124, 107, 125, 141,
		179, 153, 125, 107, 125, 141, 179, 153, 125, 107, 125, 141,
		179, 153, 125,
		140, 139, 182, 182, 152, 136, 152, 136, 153, 136, 139, 111,
		136, 139, 111, 141, 111,
	})
	idx = CtxCoeffAbsLevelGreater1
	// coeff_abs_level_greater1 (24): INIT_ONE_FLAG
	copy(initValuesISlice[idx:], []uint8{
		140, 92, 137, 138, 140, 152, 138, 139,
		153, 74, 149, 92, 139, 107, 122, 152,
		140, 179, 166, 182, 140, 227, 122, 197,
	})
	idx = CtxCoeffAbsLevelGreater2
	// coeff_abs_level_greater2 (6): INIT_ABS_FLAG
	copy(initValuesISlice[idx:], []uint8{138, 153, 136, 167, 152, 152})
	idx = CtxCuTransquantBypassFlag
	// cu_transquant_bypass_flag (1)
	initValuesISlice[idx] = cnu
	idx = CtxTransformSkipFlag
	// transform_skip_flag (2): INIT_TRANSFORMSKIP_FLAG
	copy(initValuesISlice[idx:], []uint8{139, 139})
	idx = CtxSaoMergeFlag
	// sao_merge_flag (1)
	initValuesISlice[idx] = 153
	idx = CtxSaoTypeIdx
	// sao_type_idx (1)
	initValuesISlice[idx] = 200
	idx = CtxMergeIdx
	// merge_idx (1): unused in I-slice
	initValuesISlice[idx] = cnu
	idx = CtxCuQpDeltaAbs
	// cu_qp_delta_abs (2)
	copy(initValuesISlice[idx:], []uint8{154, 154})

	// === P-slice init values (from FFmpeg hevc_cabac.c, init_type=1) ===
	idx = CtxSplitCuFlag
	copy(initValuesPSlice[idx:], []uint8{107, 139, 126})
	idx = CtxCuSkipFlag
	copy(initValuesPSlice[idx:], []uint8{197, 185, 201})
	idx = CtxPredModeFlag
	initValuesPSlice[idx] = 149
	idx = CtxPartMode
	copy(initValuesPSlice[idx:], []uint8{154, 139, 154, 154})
	idx = CtxPrevIntraLumaPredFlag
	initValuesPSlice[idx] = 154
	idx = CtxIntraChromaPredMode
	initValuesPSlice[idx] = 152
	idx = CtxMergeFlag
	initValuesPSlice[idx] = 110
	idx = CtxCbfLuma
	copy(initValuesPSlice[idx:], []uint8{153, 111})
	idx = CtxCbfCb
	// cbf_chroma (5): 4 from FFmpeg + CNU for depth 4
	copy(initValuesPSlice[idx:], []uint8{149, 107, 167, 154, cnu})
	idx = CtxSplitTransformFlag
	copy(initValuesPSlice[idx:], []uint8{124, 138, 94})
	idx = CtxLastSigCoeffXPrefix
	copy(initValuesPSlice[idx:], lastSigCoeffInitValuesP[:])
	idx = CtxLastSigCoeffYPrefix
	copy(initValuesPSlice[idx:], lastSigCoeffInitValuesP[:])
	idx = CtxCodedSubBlockFlag
	copy(initValuesPSlice[idx:], []uint8{121, 140, 61, 154})
	idx = CtxSigCoeffFlag
	copy(initValuesPSlice[idx:], []uint8{
		155, 154, 139, 153, 139, 123, 123, 63, 153, 166, 183, 140,
		136, 153, 154, 166, 183, 140, 136, 153, 154, 166, 183, 140,
		136, 153, 154,
		170, 153, 123, 123, 107, 121, 107, 121, 167, 151, 183, 140,
		151, 183, 140, 140, 140,
	})
	idx = CtxCoeffAbsLevelGreater1
	copy(initValuesPSlice[idx:], []uint8{
		154, 196, 196, 167, 154, 152, 167, 182,
		182, 134, 149, 136, 153, 121, 136, 137,
		169, 194, 166, 167, 154, 167, 137, 182,
	})
	idx = CtxCoeffAbsLevelGreater2
	copy(initValuesPSlice[idx:], []uint8{107, 167, 91, 122, 107, 167})
	idx = CtxCuTransquantBypassFlag
	initValuesPSlice[idx] = 154
	idx = CtxTransformSkipFlag
	copy(initValuesPSlice[idx:], []uint8{139, 139})
	idx = CtxSaoMergeFlag
	initValuesPSlice[idx] = 153
	idx = CtxSaoTypeIdx
	initValuesPSlice[idx] = 185
	idx = CtxMergeIdx
	initValuesPSlice[idx] = 122
	idx = CtxCuQpDeltaAbs
	copy(initValuesPSlice[idx:], []uint8{154, 154})
}

// InitModels initializes CABAC context models for the given slice type and QP.
// sliceType: 0=B, 1=P, 2=I.
func InitModels(sliceType, sliceQPY int) []cabac.CtxState {
	models := make([]cabac.CtxState, NumContextModels)
	qp := clip3(0, 51, sliceQPY)

	var initValues *[NumContextModels]uint8
	switch sliceType {
	case SliceTypeP:
		initValues = &initValuesPSlice
	default:
		initValues = &initValuesISlice
	}

	for i := range NumContextModels {
		initValue := int(initValues[i])
		slopeIdx := initValue >> 4
		offsetIdx := initValue & 15
		m := slopeIdx*5 - 45
		n := (offsetIdx << 3) - 16
		preCtxState := clip3(1, 126, ((m*qp)>>4)+n)
		if preCtxState <= 63 {
			models[i].PStateIdx = uint8(63 - preCtxState)
			models[i].ValMPS = 0
		} else {
			models[i].PStateIdx = uint8(preCtxState - 64)
			models[i].ValMPS = 1
		}
	}
	return models
}

func clip3(low, high, val int) int {
	if val < low {
		return low
	}
	if val > high {
		return high
	}
	return val
}
