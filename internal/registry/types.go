package registry

import (
	"fmt"
	"strings"
)

// Kind indicates which OpenAI-style surface a model belongs to.
type Kind string

const (
	KindImage Kind = "image"
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

// ImageInput is the canonical inbound shape we accept on /v1/images/generations.
// Mirrors the OpenAI request body plus a few mulerun-specific extras.
type ImageInput struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	N              int            `json:"n,omitempty"`
	Size           string         `json:"size,omitempty"` // "1024x1024" or "auto"
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Resolution     string         `json:"resolution,omitempty"` // "1K"/"2K"/"4K" for nano-banana
	Quality        string         `json:"quality,omitempty"`    // gpt-image-2 only
	Format         string         `json:"format,omitempty"`     // png|jpeg|webp
	WebSearch      *bool          `json:"web_search,omitempty"` // nano-banana-2 only
	Seed           *int           `json:"seed,omitempty"`
	Image          string         `json:"image,omitempty"` // i2i input
	Extra          map[string]any `json:"extra,omitempty"`
}

// VideoInput is the canonical inbound shape we accept on /v1/videos.
type VideoInput struct {
	Model           string         `json:"model"`
	Prompt          string         `json:"prompt"`
	NegativePrompt  string         `json:"negative_prompt,omitempty"`
	Size            string         `json:"size,omitempty"`
	AspectRatio     string         `json:"aspect_ratio,omitempty"`
	Resolution      string         `json:"resolution,omitempty"`
	Seconds         string         `json:"seconds,omitempty"`
	Duration        *int           `json:"duration,omitempty"`
	Image           string         `json:"image,omitempty"`
	LastFrame       string         `json:"last_frame,omitempty"`
	FirstFrame      string         `json:"first_frame,omitempty"` // kling-v3 family
	Video           string         `json:"video,omitempty"`       // kling-v3-omni v2v
	KeepAudio       *bool          `json:"keep_audio,omitempty"`
	MultiPrompt     []any          `json:"multi_prompt,omitempty"`
	MultiShot       string         `json:"multi_shot,omitempty"`
	ShotType        string         `json:"shot_type,omitempty"`
	Elements        []any          `json:"elements,omitempty"`
	Images          []string       `json:"images,omitempty"`
	InputReference  string         `json:"input_reference,omitempty"`
	ReferenceImages []string       `json:"reference_images,omitempty"`
	Mode            string         `json:"mode,omitempty"`
	Sound           string         `json:"sound,omitempty"`
	GenerateAudio   *bool          `json:"generate_audio,omitempty"`
	Seed            *int           `json:"seed,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

// AudioInput is the canonical inbound shape for /v1/audio/speech and
// /v1/audio/music. Mirrors OpenAI's TTS request plus minimax extras.
type AudioInput struct {
	Model                string         `json:"model"`
	Input                string         `json:"input,omitempty"`         // OpenAI TTS field
	Prompt               string         `json:"prompt,omitempty"`        // mulerun field
	Voice                string         `json:"voice,omitempty"`         // OpenAI TTS field
	VoiceID              string         `json:"voice_id,omitempty"`      // mulerun field
	ResponseFormat       string         `json:"response_format,omitempty"` // OpenAI TTS: mp3 / opus / aac / flac / wav / pcm
	AudioFormat          string         `json:"audio_format,omitempty"`    // mulerun: mp3 / pcm / flac
	Speed                *float64       `json:"speed,omitempty"`
	Vol                  *float64       `json:"vol,omitempty"`
	Pitch                *int           `json:"pitch,omitempty"`
	Emotion              string         `json:"emotion,omitempty"`
	LanguageBoost        string         `json:"language_boost,omitempty"`
	SampleRate           *int           `json:"sample_rate,omitempty"`
	Bitrate              *int           `json:"bitrate,omitempty"`
	EnglishNormalization *bool          `json:"english_normalization,omitempty"`
	LyricsPrompt         string         `json:"lyrics_prompt,omitempty"`
	LyricsOptimizer      *bool          `json:"lyrics_optimizer,omitempty"`
	Extra                map[string]any `json:"extra,omitempty"`
}

// ImageEditInput is the canonical inbound shape for /v1/images/edits. Accepts
// both the OpenAI single-`image` style and the mulerun `images[]` style.
type ImageEditInput struct {
	Model          string         `json:"model"`
	Prompt         string         `json:"prompt"`
	Images         []string       `json:"images,omitempty"` // URL, data: URI, or path
	Image          string         `json:"image,omitempty"`  // OpenAI single-image shorthand
	Mask           string         `json:"mask,omitempty"`
	Size           string         `json:"size,omitempty"`
	AspectRatio    string         `json:"aspect_ratio,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	N              int            `json:"n,omitempty"`
	Format         string         `json:"format,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	Seed           *int           `json:"seed,omitempty"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	Extra          map[string]any `json:"extra,omitempty"`
}

// AllImages returns Images plus the OpenAI single-image shorthand merged in.
func (in ImageEditInput) AllImages() []string {
	out := make([]string, 0, len(in.Images)+1)
	if in.Image != "" {
		out = append(out, in.Image)
	}
	out = append(out, in.Images...)
	return out
}

// Model describes a single supported mulerun model.
type Model struct {
	ID         string
	Vendor     string // human-readable; populates owned_by in /v1/models
	Kind       Kind
	VendorPath string // e.g. /vendors/openai/v1/sora-2/generation
	MapImage   func(in ImageInput) (map[string]any, error)
	MapVideo   func(in VideoInput) (map[string]any, error)
	MapAudio   func(in AudioInput) (map[string]any, error)
	MapEdit    func(in ImageEditInput) (map[string]any, error)
}

var registry = map[string]Model{}

func register(m Model) {
	if _, exists := registry[m.ID]; exists {
		panic(fmt.Sprintf("duplicate model registration: %s", m.ID))
	}
	registry[m.ID] = m
}

func Get(id string) (Model, bool) {
	m, ok := registry[id]
	return m, ok
}

// All returns every registered model, suitable for /v1/models.
func All() []Model {
	out := make([]Model, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	return out
}

// ---- helpers ---------------------------------------------------------------

func sizeToStar(s string) string {
	// OpenAI uses 1024x1024 ; mulerun wan uses 1024*1024
	return strings.ReplaceAll(s, "x", "*")
}

func putNonEmpty(out map[string]any, key string, val string) {
	if val != "" {
		out[key] = val
	}
}

func putNonNilInt(out map[string]any, key string, p *int) {
	if p != nil {
		out[key] = *p
	}
}

func putNonNilBool(out map[string]any, key string, p *bool) {
	if p != nil {
		out[key] = *p
	}
}

func putNonNilFloat(out map[string]any, key string, p *float64) {
	if p != nil {
		out[key] = *p
	}
}

func putNonEmptySlice[T any](out map[string]any, key string, vals []T) {
	if len(vals) > 0 {
		out[key] = vals
	}
}

func mergeExtra(out map[string]any, extra map[string]any) {
	for k, v := range extra {
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
}
