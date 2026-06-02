package registry

func init() {
	// ---- Midjourney ------------------------------------------------------
	register(Model{
		ID: "midjourney", Vendor: "midjourney", Kind: KindImage,
		VendorPath: "/vendors/midjourney/v1/tob/diffusion",
		MapImage: func(in ImageInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})

	// ---- Wan image models (Alibaba) --------------------------------------
	wanImage := func(model string) func(ImageInput) (map[string]any, error) {
		_ = model
		return func(in ImageInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			putNonEmpty(out, "negative_prompt", in.NegativePrompt)
			if in.Size != "" {
				out["size"] = sizeToStar(in.Size)
			}
			if in.N > 0 {
				out["n"] = in.N
			}
			putNonNilInt(out, "seed", in.Seed)
			if in.Image != "" {
				out["image"] = in.Image
			}
			mergeExtra(out, in.Extra)
			return out, nil
		}
	}

	register(Model{ID: "wan2.6-t2i", Vendor: "alibaba", Kind: KindImage,
		VendorPath: "/vendors/alibaba/v1/wan2.6-t2i/generation", MapImage: wanImage("wan2.6-t2i")})
	register(Model{ID: "wan2.6-image", Vendor: "alibaba", Kind: KindImage,
		VendorPath: "/vendors/alibaba/v1/wan2.6-image/generation", MapImage: wanImage("wan2.6-image")})
	register(Model{ID: "wan2.5-t2i-preview", Vendor: "alibaba", Kind: KindImage,
		VendorPath: "/vendors/alibaba/v1/wan2.5-t2i-preview/generation", MapImage: wanImage("wan2.5-t2i-preview")})
	register(Model{ID: "wan2.5-i2i-preview", Vendor: "alibaba", Kind: KindImage,
		VendorPath: "/vendors/alibaba/v1/wan2.5-i2i-preview/generation", MapImage: wanImage("wan2.5-i2i-preview")})

	// ---- OpenAI gpt-image-2 ---------------------------------------------
	register(Model{
		ID: "gpt-image-2", Vendor: "openai", Kind: KindImage,
		VendorPath: "/vendors/openai/v1/gpt-image-2/generation",
		MapImage: func(in ImageInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			putNonEmpty(out, "quality", in.Quality)
			putNonEmpty(out, "size", in.Size)
			putNonEmpty(out, "format", in.Format)
			if in.N > 0 {
				out["n"] = in.N
			}
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})

	// ---- Google nano-banana family --------------------------------------
	nanoBanana := func(supportResolution, supportWebSearch bool) func(ImageInput) (map[string]any, error) {
		return func(in ImageInput) (map[string]any, error) {
			out := map[string]any{"prompt": in.Prompt}
			putNonEmpty(out, "aspect_ratio", in.AspectRatio)
			if supportResolution {
				putNonEmpty(out, "resolution", in.Resolution)
			}
			if supportWebSearch {
				putNonNilBool(out, "web_search", in.WebSearch)
			}
			mergeExtra(out, in.Extra)
			return out, nil
		}
	}
	register(Model{ID: "nano-banana", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana/generation", MapImage: nanoBanana(false, false)})
	register(Model{ID: "nano-banana-pro", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana-pro/generation", MapImage: nanoBanana(true, false)})
	register(Model{ID: "nano-banana-2", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana-2/generation", MapImage: nanoBanana(true, true)})
}
