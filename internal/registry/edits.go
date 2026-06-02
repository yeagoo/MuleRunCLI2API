package registry

// Edit-action image models. ID is the generation model's ID with "-edit"
// suffix. The /v1/images/edits handler routes here based on the same
// MapEdit field shape so that we keep generation and edit cleanly separated
// in the routing layer (no implicit "if Image then edit" logic).

func init() {
	// ---- OpenAI gpt-image-2/edit ----------------------------------------
	register(Model{
		ID: "gpt-image-2-edit", Vendor: "openai", Kind: KindImage,
		VendorPath: "/vendors/openai/v1/gpt-image-2/edit",
		MapEdit: func(in ImageEditInput) (map[string]any, error) {
			out := map[string]any{
				"prompt": in.Prompt,
				"images": in.AllImages(),
			}
			putNonEmpty(out, "mask", in.Mask)
			putNonEmpty(out, "size", in.Size)
			putNonEmpty(out, "format", in.Format)
			if in.N > 0 {
				out["n"] = in.N
			}
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})

	// ---- Google nano-banana family /edit --------------------------------
	nanoBananaEdit := func(supportResolution bool) func(ImageEditInput) (map[string]any, error) {
		return func(in ImageEditInput) (map[string]any, error) {
			out := map[string]any{
				"prompt": in.Prompt,
				"images": in.AllImages(),
			}
			putNonEmpty(out, "aspect_ratio", in.AspectRatio)
			if supportResolution {
				putNonEmpty(out, "resolution", in.Resolution)
			}
			mergeExtra(out, in.Extra)
			return out, nil
		}
	}
	register(Model{ID: "nano-banana-edit", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana/edit", MapEdit: nanoBananaEdit(false)})
	register(Model{ID: "nano-banana-pro-edit", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana-pro/edit", MapEdit: nanoBananaEdit(true)})
	register(Model{ID: "nano-banana-2-edit", Vendor: "google", Kind: KindImage,
		VendorPath: "/vendors/google/v1/nano-banana-2/edit", MapEdit: nanoBananaEdit(true)})

	// ---- Alibaba wan2.5-i2i exposed as both /v1/images/generations
	// and /v1/images/edits surfaces. We re-register here for the edit
	// surface — same upstream path, different inbound shape.
	register(Model{
		ID: "wan2.5-i2i-preview-edit", Vendor: "alibaba", Kind: KindImage,
		VendorPath: "/vendors/alibaba/v1/wan2.5-i2i-preview/generation",
		MapEdit: func(in ImageEditInput) (map[string]any, error) {
			out := map[string]any{
				"prompt": in.Prompt,
				"images": in.AllImages(),
			}
			putNonEmpty(out, "negative_prompt", in.NegativePrompt)
			if in.Size != "" {
				out["size"] = sizeToStar(in.Size)
			}
			if in.N > 0 {
				out["n"] = in.N
			}
			putNonNilInt(out, "seed", in.Seed)
			mergeExtra(out, in.Extra)
			return out, nil
		},
	})
}
