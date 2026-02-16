package encode

// writeProfileTierLevel writes the profile_tier_level() syntax to w.
// Main profile, Level 3.0, maxSubLayers=1.
func writeProfileTierLevel(w *BitWriter) {
	w.WriteBits(0, 2)  // general_profile_space = 0
	w.WriteBit(0)      // general_tier_flag = 0
	w.WriteBits(1, 5)  // general_profile_idc = 1 (Main)

	// general_profile_compatibility_flag[0..31]: bit 1 set (Main profile)
	w.WriteBits(0x40000000, 32)

	// general_progressive_source_flag=1, interlaced=0, non_packed=1, frame_only=1
	w.WriteBit(1) // progressive_source_flag
	w.WriteBit(0) // interlaced_source_flag
	w.WriteBit(1) // non_packed_constraint_flag
	w.WriteBit(1) // frame_only_constraint_flag

	// 44 reserved zero bits (constraints)
	w.WriteBits(0, 32)
	w.WriteBits(0, 12)

	w.WriteBits(90, 8) // general_level_idc = 90 (Level 3.0)
}

// generateVPS returns the VPS RBSP bytes.
func generateVPS() []byte {
	w := NewBitWriter()

	w.WriteBits(0, 4)  // vps_video_parameter_set_id = 0
	w.WriteBits(3, 2)  // vps_base_layer_internal_flag=1, vps_base_layer_available_flag=1
	w.WriteBits(0, 6)  // vps_max_layers_minus1 = 0
	w.WriteBits(0, 3)  // vps_max_sub_layers_minus1 = 0
	w.WriteBit(1)      // vps_temporal_id_nesting_flag = 1
	w.WriteBits(0xFFFF, 16) // vps_reserved_0xffff_16bits

	writeProfileTierLevel(w)

	w.WriteBit(0) // vps_sub_layer_ordering_info_present_flag = 0
	// For i = vps_max_sub_layers_minus1 .. vps_max_sub_layers_minus1:
	w.WriteUE(1) // vps_max_dec_pic_buffering_minus1 = 1 (DPB size 2)
	w.WriteUE(0) // vps_max_num_reorder_pics = 0
	w.WriteUE(0) // vps_max_latency_increase_plus1 = 0

	w.WriteBits(0, 6) // vps_max_layer_id = 0
	w.WriteUE(0)      // vps_num_layer_sets_minus1 = 0
	w.WriteBit(0)     // vps_timing_info_present_flag = 0
	w.WriteBit(0)     // vps_extension_flag = 0

	// RBSP trailing bits
	w.WriteBit(1)
	w.AlignToByte()

	return w.Bytes()
}

// generateSPS returns the SPS RBSP bytes for the given dimensions and QP.
func generateSPS(width, height int) []byte {
	w := NewBitWriter()

	w.WriteBits(0, 4)  // sps_video_parameter_set_id = 0
	w.WriteBits(0, 3)  // sps_max_sub_layers_minus1 = 0
	w.WriteBit(1)      // sps_temporal_id_nesting_flag = 1

	writeProfileTierLevel(w)

	w.WriteUE(0)       // sps_seq_parameter_set_id = 0
	w.WriteUE(1)       // chroma_format_idc = 1 (4:2:0)
	// separate_colour_plane_flag: not present when chroma_format_idc != 3
	w.WriteUE(uint32(width))  // pic_width_in_luma_samples
	w.WriteUE(uint32(height)) // pic_height_in_luma_samples
	w.WriteBit(0)      // conformance_window_flag = 0
	w.WriteUE(0)       // bit_depth_luma_minus8 = 0
	w.WriteUE(0)       // bit_depth_chroma_minus8 = 0
	w.WriteUE(0)       // log2_max_pic_order_cnt_lsb_minus4 = 0 (max_poc_lsb = 16)

	w.WriteBit(0)      // sps_sub_layer_ordering_info_present_flag = 0
	// For i = sps_max_sub_layers_minus1:
	w.WriteUE(1)       // sps_max_dec_pic_buffering_minus1 = 1
	w.WriteUE(0)       // sps_max_num_reorder_pics = 0
	w.WriteUE(0)       // sps_max_latency_increase_plus1 = 0

	w.WriteUE(1)       // log2_min_luma_coding_block_size_minus3 = 1 (minCbSize=16)
	w.WriteUE(0)       // log2_diff_max_min_luma_coding_block_size = 0 (CTU=16)
	w.WriteUE(0)       // log2_min_luma_transform_block_size_minus2 = 0 (minTbSize=4)
	w.WriteUE(2)       // log2_diff_max_min_luma_transform_block_size = 2 (maxTbSize=16)
	w.WriteUE(0)       // max_transform_hierarchy_depth_inter = 0
	w.WriteUE(0)       // max_transform_hierarchy_depth_intra = 0

	w.WriteBit(0)      // scaling_list_enabled_flag = 0
	w.WriteBit(0)      // amp_enabled_flag = 0
	w.WriteBit(0)      // sample_adaptive_offset_enabled_flag = 0
	w.WriteBit(0)      // pcm_enabled_flag = 0
	w.WriteUE(0)       // num_short_term_ref_pic_sets = 0
	w.WriteBit(0)      // long_term_ref_pics_present_flag = 0
	w.WriteBit(0)      // sps_temporal_mvp_enabled_flag = 0
	w.WriteBit(0)      // strong_intra_smoothing_enabled_flag = 0
	w.WriteBit(0)      // vui_parameters_present_flag = 0
	w.WriteBit(0)      // sps_extension_present_flag = 0

	// RBSP trailing bits
	w.WriteBit(1)
	w.AlignToByte()

	return w.Bytes()
}

// generatePPS returns the PPS RBSP bytes for the given QP.
func generatePPS(qp int) []byte {
	w := NewBitWriter()

	w.WriteUE(0)           // pps_pic_parameter_set_id = 0
	w.WriteUE(0)           // pps_seq_parameter_set_id = 0
	w.WriteBit(0)          // dependent_slice_segments_enabled_flag = 0
	w.WriteBit(0)          // output_flag_present_flag = 0
	w.WriteBits(0, 3)     // num_extra_slice_header_bits = 0
	w.WriteBit(0)          // sign_data_hiding_enabled_flag = 0
	w.WriteBit(0)          // cabac_init_present_flag = 0
	w.WriteUE(0)           // num_ref_idx_l0_default_active_minus1 = 0
	w.WriteUE(0)           // num_ref_idx_l1_default_active_minus1 = 0
	w.WriteSE(int32(qp) - 26) // init_qp_minus26
	w.WriteBit(0)          // constrained_intra_pred_flag = 0
	w.WriteBit(0)          // transform_skip_enabled_flag = 0
	w.WriteBit(0)          // cu_qp_delta_enabled_flag = 0
	// no cb_qp_offset or cr_qp_offset when cu_qp_delta_enabled_flag=0
	w.WriteSE(0)           // pps_cb_qp_offset = 0
	w.WriteSE(0)           // pps_cr_qp_offset = 0
	w.WriteBit(0)          // pps_slice_chroma_qp_offsets_present_flag = 0
	w.WriteBit(0)          // weighted_pred_flag = 0
	w.WriteBit(0)          // weighted_bipred_flag = 0
	w.WriteBit(0)          // transquant_bypass_enabled_flag = 0
	w.WriteBit(0)          // tiles_enabled_flag = 0
	w.WriteBit(0)          // entropy_coding_sync_enabled_flag = 0
	// no tile/WPP info
	w.WriteBit(0)          // loop_filter_across_slices_enabled_flag = 0
	w.WriteBit(0)          // deblocking_filter_control_present_flag = 0
	// no deblocking syntax
	w.WriteBit(0)          // pps_scaling_list_data_present_flag = 0
	w.WriteBit(0)          // lists_modification_present_flag = 0
	w.WriteUE(0)           // log2_parallel_merge_level_minus2 = 0
	w.WriteBit(0)          // slice_segment_header_extension_present_flag = 0
	w.WriteBit(0)          // pps_extension_present_flag = 0

	// RBSP trailing bits
	w.WriteBit(1)
	w.AlignToByte()

	return w.Bytes()
}
