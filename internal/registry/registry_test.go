package registry

import (
	"testing"
)

func TestImageRegistry_WanSizeConversion(t *testing.T) {
	m, ok := Get("wan2.6-t2i")
	if !ok {
		t.Fatal("wan2.6-t2i not registered")
	}
	got, err := m.MapImage(ImageInput{Prompt: "x", Size: "1024x1024", N: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got["size"] != "1024*1024" {
		t.Fatalf("expected 1024*1024, got %v", got["size"])
	}
	if got["n"] != 2 {
		t.Fatalf("n: %v", got["n"])
	}
}

func TestVideoRegistry_SoraReferenceFallback(t *testing.T) {
	m, ok := Get("sora-2")
	if !ok {
		t.Fatal("sora-2 not registered")
	}
	got, err := m.MapVideo(VideoInput{Prompt: "x", Image: "https://example/i.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got["input_reference"] != "https://example/i.png" {
		t.Fatalf("expected input_reference filled from image, got %v", got["input_reference"])
	}
}

func TestVideoRegistry_KlingI2VShapesImageRequired(t *testing.T) {
	m, ok := Get("kling-v2.6-image-to-video")
	if !ok {
		t.Fatal("kling-v2.6-image-to-video not registered")
	}
	got, err := m.MapVideo(VideoInput{Prompt: "x", Image: "https://example/i.png", Mode: "pro"})
	if err != nil {
		t.Fatal(err)
	}
	if got["image"] != "https://example/i.png" {
		t.Fatalf("missing image")
	}
	if got["mode"] != "pro" {
		t.Fatalf("missing mode")
	}
}

func TestVideoRegistry_ExtraPassThrough(t *testing.T) {
	m, _ := Get("wan2.6-t2v")
	got, err := m.MapVideo(VideoInput{Prompt: "x", Extra: map[string]any{"custom_key": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["custom_key"] != "v" {
		t.Fatalf("extra not propagated: %v", got)
	}
}

func TestAllNonEmpty(t *testing.T) {
	if len(All()) < 30 {
		t.Fatalf("expected ≥30 models, got %d", len(All()))
	}
}

func TestAudio_SpeechMaps(t *testing.T) {
	m, ok := Get("speech-2.8-hd")
	if !ok {
		t.Fatal("speech-2.8-hd not registered")
	}
	if m.Kind != KindAudio {
		t.Fatalf("kind: %s", m.Kind)
	}
	got, err := m.MapAudio(AudioInput{
		Input:   "hello world",
		Voice:   "Charming_Lady",
		Emotion: "happy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["prompt"] != "hello world" || got["voice_id"] != "Charming_Lady" {
		t.Fatalf("required mapping wrong: %+v", got)
	}
	if got["output_format"] != "url" {
		t.Fatalf("output_format should be forced to url, got %v", got["output_format"])
	}
	if got["emotion"] != "happy" {
		t.Fatalf("emotion: %v", got["emotion"])
	}
}

func TestAudio_MusicMaps(t *testing.T) {
	m, ok := Get("music-2.5")
	if !ok {
		t.Fatal("music-2.5 not registered")
	}
	if !IsMusic(m.ID) {
		t.Fatal("IsMusic should be true for music-2.5")
	}
	got, err := m.MapAudio(AudioInput{
		Prompt:       "synthwave, melodic, upbeat",
		LyricsPrompt: "[verse]\nlight the night",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["prompt"] == nil || got["lyrics_prompt"] == nil {
		t.Fatalf("expected prompt + lyrics_prompt, got %+v", got)
	}
	if _, has := got["output_format"]; has {
		t.Fatalf("music should not force output_format, got %v", got["output_format"])
	}
}

func TestIsMusic(t *testing.T) {
	cases := map[string]bool{
		"music-2.5":        true,
		"music-2.0":        true,
		"speech-2.8-hd":    false,
		"sora-2":           false,
		"":                 false,
	}
	for id, want := range cases {
		if got := IsMusic(id); got != want {
			t.Errorf("IsMusic(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestImageRegistry_GptImage2(t *testing.T) {
	m, ok := Get("gpt-image-2")
	if !ok {
		t.Fatal("gpt-image-2 not registered")
	}
	q := "high"
	got, err := m.MapImage(ImageInput{Prompt: "x", Quality: q, Size: "1024x1024", N: 2, Format: "png"})
	if err != nil {
		t.Fatal(err)
	}
	if got["quality"] != "high" || got["size"] != "1024x1024" || got["n"] != 2 || got["format"] != "png" {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestImageRegistry_NanoBananaFamily(t *testing.T) {
	cases := []struct {
		id              string
		wantResolution  bool
		wantWebSearch   bool
	}{
		{"nano-banana", false, false},
		{"nano-banana-pro", true, false},
		{"nano-banana-2", true, true},
	}
	tb := true
	for _, tc := range cases {
		m, ok := Get(tc.id)
		if !ok {
			t.Fatalf("%s not registered", tc.id)
		}
		got, err := m.MapImage(ImageInput{Prompt: "x", AspectRatio: "16:9", Resolution: "2K", WebSearch: &tb})
		if err != nil {
			t.Fatal(err)
		}
		if got["aspect_ratio"] != "16:9" {
			t.Fatalf("%s aspect_ratio: %v", tc.id, got["aspect_ratio"])
		}
		if _, has := got["resolution"]; has != tc.wantResolution {
			t.Fatalf("%s resolution present=%v want=%v", tc.id, has, tc.wantResolution)
		}
		if _, has := got["web_search"]; has != tc.wantWebSearch {
			t.Fatalf("%s web_search present=%v want=%v", tc.id, has, tc.wantWebSearch)
		}
	}
}

func TestVideoRegistry_VeoAggregator(t *testing.T) {
	m, ok := Get("veo")
	if !ok {
		t.Fatal("veo not registered")
	}
	// Default: model defaults to veo-3.1
	got, err := m.MapVideo(VideoInput{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "veo-3.1" {
		t.Fatalf("default model: %v", got["model"])
	}
	// Extra.model wins.
	got, err = m.MapVideo(VideoInput{Prompt: "x", Extra: map[string]any{"model": "veo-3.1-fast"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "veo-3.1-fast" {
		t.Fatalf("extra override: %v", got["model"])
	}
}

func TestVideoRegistry_KlingV3FirstFrame(t *testing.T) {
	m, ok := Get("kling-v3-image-to-video")
	if !ok {
		t.Fatal("not registered")
	}
	// Image (compat) → first_frame
	got, err := m.MapVideo(VideoInput{Image: "https://example/i.png", Prompt: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if got["first_frame"] != "https://example/i.png" {
		t.Fatalf("Image not mapped to first_frame: %+v", got)
	}
	// Explicit first_frame wins over Image
	got, err = m.MapVideo(VideoInput{Image: "fallback", FirstFrame: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if got["first_frame"] != "primary" {
		t.Fatalf("explicit first_frame should win: %+v", got)
	}
}

func TestVideoRegistry_KlingV3OmniV2VDropsAspect(t *testing.T) {
	m, ok := Get("kling-v3-omni-video-to-video-edit")
	if !ok {
		t.Fatal("not registered")
	}
	got, err := m.MapVideo(VideoInput{Prompt: "edit", Video: "https://example/v.mp4", AspectRatio: "16:9", MultiShot: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got["aspect_ratio"]; has {
		t.Fatalf("v2v/edit must not include aspect_ratio, got %+v", got)
	}
	if _, has := got["multi_shot"]; has {
		t.Fatalf("v2v/edit must not include multi_shot, got %+v", got)
	}
}

func TestVideoRegistry_HappyHorseI2V(t *testing.T) {
	m, ok := Get("happy-horse-1-0-i2v")
	if !ok {
		t.Fatal("not registered")
	}
	d := 8
	got, err := m.MapVideo(VideoInput{Image: "https://x", Resolution: "1080P", Duration: &d})
	if err != nil {
		t.Fatal(err)
	}
	if got["image"] != "https://x" || got["resolution"] != "1080P" || got["duration"] != 8 {
		t.Fatalf("mapping wrong: %+v", got)
	}
}

func TestEditRegistry_GptImage2Edit(t *testing.T) {
	m, ok := Get("gpt-image-2-edit")
	if !ok {
		t.Fatal("not registered")
	}
	if m.MapEdit == nil {
		t.Fatal("MapEdit nil")
	}
	got, err := m.MapEdit(ImageEditInput{
		Prompt: "make it red",
		Image:  "https://a/single.png",
		Images: []string{"https://b/extra.png"},
		Mask:   "https://m/mask.png",
		Size:   "1024x1024",
		N:      2,
		Format: "png",
	})
	if err != nil {
		t.Fatal(err)
	}
	imgs, ok := got["images"].([]string)
	if !ok || len(imgs) != 2 || imgs[0] != "https://a/single.png" {
		t.Fatalf("images merge wrong: %+v", got["images"])
	}
	if got["mask"] != "https://m/mask.png" || got["size"] != "1024x1024" || got["n"] != 2 {
		t.Fatalf("fields wrong: %+v", got)
	}
}

func TestEditRegistry_Wan25I2IEdit(t *testing.T) {
	m, ok := Get("wan2.5-i2i-preview-edit")
	if !ok {
		t.Fatal("not registered")
	}
	got, err := m.MapEdit(ImageEditInput{
		Prompt: "transform",
		Images: []string{"https://a/x.png"},
		Size:   "1024x1024",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["size"] != "1024*1024" {
		t.Fatalf("size star conversion missing: %+v", got)
	}
}

func TestImageEditInput_AllImages(t *testing.T) {
	in := ImageEditInput{Image: "single", Images: []string{"a", "b"}}
	got := in.AllImages()
	if len(got) != 3 || got[0] != "single" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("AllImages: %+v", got)
	}
}
