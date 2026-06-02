package registry

func init() {
	// ---- OpenAI Sora -----------------------------------------------------
	soraMapper := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "seconds", in.Seconds)
		putNonEmpty(out, "size", in.Size)
		putNonEmpty(out, "input_reference", firstNonEmpty(in.InputReference, in.Image))
		mergeExtra(out, in.Extra)
		return out, nil
	}
	register(Model{ID: "sora", Vendor: "openai", Kind: KindVideo,
		VendorPath: "/vendors/openai/v1/sora/generation", MapVideo: soraMapper})
	register(Model{ID: "sora-2", Vendor: "openai", Kind: KindVideo,
		VendorPath: "/vendors/openai/v1/sora-2/generation", MapVideo: soraMapper})
	register(Model{ID: "sora-2-pro", Vendor: "openai", Kind: KindVideo,
		VendorPath: "/vendors/openai/v1/sora-2-pro/generation", MapVideo: soraMapper})

	// ---- Google Veo ------------------------------------------------------
	veoMapper := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "image", in.Image)
		putNonEmpty(out, "last_frame", in.LastFrame)
		if len(in.ReferenceImages) > 0 {
			out["reference_images"] = in.ReferenceImages
		}
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonEmpty(out, "resolution", in.Resolution)
		putNonNilInt(out, "duration", in.Duration)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	for _, id := range []string{"veo3", "veo-3.0", "veo-3.1", "veo-3.1-fast"} {
		register(Model{ID: id, Vendor: "google", Kind: KindVideo,
			VendorPath: "/vendors/google/v1/" + id + "/generation", MapVideo: veoMapper})
	}

	// ---- Kling AI --------------------------------------------------------
	klingT2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonEmpty(out, "sound", in.Sound)
		putNonEmpty(out, "mode", in.Mode)
		putNonNilInt(out, "duration", in.Duration)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingI2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "image", in.Image)
		putNonEmpty(out, "prompt", in.Prompt)
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "image_tail", in.LastFrame)
		putNonEmpty(out, "sound", in.Sound)
		putNonEmpty(out, "mode", in.Mode)
		putNonNilInt(out, "duration", in.Duration)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingVersions := []string{"kling-v2.1-master", "kling-v2.5-turbo", "kling-v2.6"}
	for _, v := range klingVersions {
		register(Model{ID: v + "-text-to-video", Vendor: "klingai", Kind: KindVideo,
			VendorPath: "/vendors/klingai/v1/" + v + "/text-to-video/generation", MapVideo: klingT2V})
		register(Model{ID: v + "-image-to-video", Vendor: "klingai", Kind: KindVideo,
			VendorPath: "/vendors/klingai/v1/" + v + "/image-to-video/generation", MapVideo: klingI2V})
	}

	// ---- ByteDance Seedance ---------------------------------------------
	seedanceT2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "resolution", in.Resolution)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilBool(out, "generate_audio", in.GenerateAudio)
		putNonNilInt(out, "seed", in.Seed)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	seedanceI2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "image", in.Image)
		putNonEmpty(out, "last_frame", in.LastFrame)
		putNonEmpty(out, "resolution", in.Resolution)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilBool(out, "generate_audio", in.GenerateAudio)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	seedanceRef2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		if len(in.ReferenceImages) > 0 {
			out["reference_images"] = in.ReferenceImages
		}
		putNonEmpty(out, "resolution", in.Resolution)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilBool(out, "generate_audio", in.GenerateAudio)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	for _, base := range []string{"seedance-2.0", "seedance-2.0-fast"} {
		register(Model{ID: base + "-text-to-video", Vendor: "bytedance", Kind: KindVideo,
			VendorPath: "/vendors/bytedance/v1/" + base + "/text-to-video/generation", MapVideo: seedanceT2V})
		register(Model{ID: base + "-image-to-video", Vendor: "bytedance", Kind: KindVideo,
			VendorPath: "/vendors/bytedance/v1/" + base + "/image-to-video/generation", MapVideo: seedanceI2V})
		register(Model{ID: base + "-reference-to-video", Vendor: "bytedance", Kind: KindVideo,
			VendorPath: "/vendors/bytedance/v1/" + base + "/reference-to-video/generation", MapVideo: seedanceRef2V})
	}

	// ---- Alibaba Wan video models ---------------------------------------
	wanVideo := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		if in.Size != "" {
			out["size"] = sizeToStar(in.Size)
		}
		putNonEmpty(out, "image", in.Image)
		putNonEmpty(out, "last_frame", in.LastFrame)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonEmpty(out, "resolution", in.Resolution)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilInt(out, "seed", in.Seed)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	wanVideoIDs := []string{
		"wan2.6-t2v", "wan2.6-i2v",
		"wan2.5-t2v-preview", "wan2.5-i2v-preview",
		"wan2.2-t2v-plus", "wan2.2-i2v-plus", "wan2.2-i2v-flash",
		"wan2.1-vace-plus", "wan2.1-kf2v-plus",
	}
	for _, id := range wanVideoIDs {
		register(Model{ID: id, Vendor: "alibaba", Kind: KindVideo,
			VendorPath: "/vendors/alibaba/v1/" + id + "/generation", MapVideo: wanVideo})
	}

	// ---- MuleRouter "spark" wan variants --------------------------------
	for _, id := range []string{"wan2.5-t2v-spark", "wan2.5-i2v-spark", "wan2.6-t2v-spark", "wan2.6-i2v-spark"} {
		register(Model{ID: id, Vendor: "mulerouter", Kind: KindVideo,
			VendorPath: "/vendors/mulerouter/v1/" + id + "/generation", MapVideo: wanVideo})
	}

	// ---- Midjourney text-to-video ---------------------------------------
	register(Model{ID: "midjourney-video", Vendor: "midjourney", Kind: KindVideo,
		VendorPath: "/vendors/midjourney/v1/tob/video-diffusion",
		MapVideo: func(in VideoInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})

	// ---- Alibaba happy-horse 1.0 ----------------------------------------
	happyHorseT2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{"prompt": in.Prompt}
		putNonEmpty(out, "resolution", in.Resolution)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilInt(out, "seed", in.Seed)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	happyHorseI2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "image", firstNonEmpty(in.Image, in.FirstFrame))
		putNonEmpty(out, "prompt", in.Prompt)
		putNonEmpty(out, "resolution", in.Resolution)
		putNonNilInt(out, "duration", in.Duration)
		putNonNilInt(out, "seed", in.Seed)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	register(Model{ID: "happy-horse-1-0-t2v", Vendor: "alibaba", Kind: KindVideo,
		VendorPath: "/vendors/alibaba/v1/happy-horse-1-0-t2v/generation", MapVideo: happyHorseT2V})
	register(Model{ID: "happy-horse-1-0-i2v", Vendor: "alibaba", Kind: KindVideo,
		VendorPath: "/vendors/alibaba/v1/happy-horse-1-0-i2v/generation", MapVideo: happyHorseI2V})

	// ---- Google Veo aggregated endpoint ---------------------------------
	register(Model{ID: "veo", Vendor: "google", Kind: KindVideo,
		VendorPath: "/vendors/google/v1/veo/generation",
		MapVideo: func(in VideoInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			putNonEmpty(out, "negative_prompt", in.NegativePrompt)
			putNonEmpty(out, "image", firstNonEmpty(in.Image, in.FirstFrame))
			putNonEmpty(out, "last_frame", in.LastFrame)
			if refs := firstNonEmptySlice(in.ReferenceImages, in.Images); len(refs) > 0 {
				out["reference_images"] = refs
			}
			putNonEmpty(out, "aspect_ratio", in.AspectRatio)
			putNonEmpty(out, "resolution", in.Resolution)
			putNonNilInt(out, "duration", in.Duration)
			// Extra.model wins; default to veo-3.1.
			if _, has := in.Extra["model"]; !has {
				out["model"] = "veo-3.1"
			}
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})

	// ---- Kling v3 (text-to-video / image-to-video) ----------------------
	klingV3T2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "prompt", in.Prompt)
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "mode", in.Mode)
		putNonEmpty(out, "multi_shot", in.MultiShot)
		putNonEmpty(out, "shot_type", in.ShotType)
		putNonEmptySlice(out, "multi_prompt", in.MultiPrompt)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonNilInt(out, "duration", in.Duration)
		putNonEmpty(out, "sound", in.Sound)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingV3I2V := func(in VideoInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "first_frame", firstNonEmpty(in.FirstFrame, in.Image))
		putNonEmpty(out, "last_frame", in.LastFrame)
		putNonEmptySlice(out, "elements", in.Elements)
		putNonEmpty(out, "prompt", in.Prompt)
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "mode", in.Mode)
		putNonEmpty(out, "multi_shot", in.MultiShot)
		putNonEmpty(out, "shot_type", in.ShotType)
		putNonEmptySlice(out, "multi_prompt", in.MultiPrompt)
		putNonNilInt(out, "duration", in.Duration)
		putNonEmpty(out, "sound", in.Sound)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	register(Model{ID: "kling-v3-text-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3/text-to-video/generation", MapVideo: klingV3T2V})
	register(Model{ID: "kling-v3-image-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3/image-to-video/generation", MapVideo: klingV3I2V})

	// ---- Kling v3 Omni (4 sub-actions) ----------------------------------
	klingOmniBase := func(in VideoInput) map[string]any {
		out := map[string]any{}
		putNonEmpty(out, "prompt", in.Prompt)
		putNonEmpty(out, "negative_prompt", in.NegativePrompt)
		putNonEmpty(out, "mode", in.Mode)
		putNonEmpty(out, "multi_shot", in.MultiShot)
		putNonEmpty(out, "shot_type", in.ShotType)
		putNonEmptySlice(out, "multi_prompt", in.MultiPrompt)
		putNonEmpty(out, "sound", in.Sound)
		putNonEmpty(out, "aspect_ratio", in.AspectRatio)
		putNonNilInt(out, "duration", in.Duration)
		return out
	}
	klingOmniT2V := func(in VideoInput) (map[string]any, error) {
		out := klingOmniBase(in)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingOmniI2V := func(in VideoInput) (map[string]any, error) {
		out := klingOmniBase(in)
		putNonEmpty(out, "first_frame", firstNonEmpty(in.FirstFrame, in.Image))
		putNonEmpty(out, "last_frame", in.LastFrame)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingOmniRefImg := func(in VideoInput) (map[string]any, error) {
		out := klingOmniBase(in)
		putNonEmpty(out, "first_frame", firstNonEmpty(in.FirstFrame, in.Image))
		putNonEmpty(out, "last_frame", in.LastFrame)
		putNonEmptySlice(out, "images", in.Images)
		putNonEmptySlice(out, "elements", in.Elements)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingOmniRefVid := func(in VideoInput) (map[string]any, error) {
		out := klingOmniBase(in)
		putNonEmpty(out, "video", in.Video)
		putNonNilBool(out, "keep_audio", in.KeepAudio)
		putNonEmptySlice(out, "images", in.Images)
		putNonEmptySlice(out, "elements", in.Elements)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	klingOmniV2V := func(in VideoInput) (map[string]any, error) {
		out := klingOmniBase(in)
		putNonEmpty(out, "video", in.Video)
		putNonNilBool(out, "keep_audio", in.KeepAudio)
		putNonEmptySlice(out, "images", in.Images)
		putNonEmptySlice(out, "elements", in.Elements)
		mergeExtra(out, in.Extra)
		// Drop v2v-unsupported keys AFTER mergeExtra so a client that
		// sneaks them in via `extra` still doesn't reach the upstream.
		delete(out, "aspect_ratio")
		delete(out, "duration")
		delete(out, "multi_shot")
		delete(out, "shot_type")
		delete(out, "multi_prompt")
		delete(out, "sound")
		return out, nil
	}
	register(Model{ID: "kling-v3-omni-text-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3-omni/text-to-video/generation", MapVideo: klingOmniT2V})
	register(Model{ID: "kling-v3-omni-image-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3-omni/image-to-video/generation", MapVideo: klingOmniI2V})
	register(Model{ID: "kling-v3-omni-reference-image-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3-omni/reference-image-to-video/generation", MapVideo: klingOmniRefImg})
	register(Model{ID: "kling-v3-omni-reference-video-to-video", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3-omni/reference-video-to-video/generation", MapVideo: klingOmniRefVid})
	register(Model{ID: "kling-v3-omni-video-to-video-edit", Vendor: "klingai", Kind: KindVideo,
		VendorPath: "/vendors/klingai/v1/kling-v3-omni/video-to-video/edit", MapVideo: klingOmniV2V})
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func firstNonEmptySlice[T any](xs ...[]T) []T {
	for _, s := range xs {
		if len(s) > 0 {
			return s
		}
	}
	return nil
}
