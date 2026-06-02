package registry

// Subtype: which audio surface a model belongs to. Stored under model.Vendor's
// suffix via the model.ID convention; handlers look up by ID directly.

// IsMusic returns true for music-* model IDs. Used by handlers to route
// audio jobs to the right surface (/v1/audio/speech vs /v1/audio/music).
func IsMusic(modelID string) bool {
	return len(modelID) >= 5 && modelID[:5] == "music"
}

func init() {
	// ---- MiniMax text-to-speech -----------------------------------------
	speechMapper := func(in AudioInput) (map[string]any, error) {
		text := firstNonEmpty(in.Input, in.Prompt)
		voice := firstNonEmpty(in.VoiceID, in.Voice)
		out := map[string]any{
			"prompt":   text,
			"voice_id": voice,
		}
		putNonEmpty(out, "emotion", in.Emotion)
		putNonEmpty(out, "language_boost", in.LanguageBoost)
		putNonEmpty(out, "audio_format", firstNonEmpty(in.AudioFormat, in.ResponseFormat))
		putNonNilFloat(out, "speed", in.Speed)
		putNonNilFloat(out, "vol", in.Vol)
		putNonNilInt(out, "pitch", in.Pitch)
		putNonNilInt(out, "sample_rate", in.SampleRate)
		putNonNilInt(out, "bitrate", in.Bitrate)
		putNonNilBool(out, "english_normalization", in.EnglishNormalization)
		// Always ask upstream for a URL — the handler then either streams the
		// bytes back (OpenAI-shape) or returns the URL JSON.
		out["output_format"] = "url"
		mergeExtra(out, in.Extra)
		return out, nil
	}
	for _, id := range []string{"speech-2.8-hd", "speech-2.8-turbo"} {
		register(Model{
			ID: id, Vendor: "minimax", Kind: KindAudio,
			VendorPath: "/vendors/minimax/v1/" + id + "/text-to-speech/generation",
			MapAudio:   speechMapper,
		})
	}

	// ---- MiniMax text-to-music ------------------------------------------
	musicMapper := func(in AudioInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "prompt", firstNonEmpty(in.Prompt, in.Input))
		putNonEmpty(out, "lyrics_prompt", in.LyricsPrompt)
		putNonNilBool(out, "lyrics_optimizer", in.LyricsOptimizer)
		putNonEmpty(out, "audio_format", firstNonEmpty(in.AudioFormat, in.ResponseFormat))
		putNonNilInt(out, "sample_rate", in.SampleRate)
		putNonNilInt(out, "bitrate", in.Bitrate)
		mergeExtra(out, in.Extra)
		return out, nil
	}
	for _, id := range []string{"music-2.0", "music-2.5"} {
		register(Model{
			ID: id, Vendor: "minimax", Kind: KindAudio,
			VendorPath: "/vendors/minimax/v1/" + id + "/text-to-music/generation",
			MapAudio:   musicMapper,
		})
	}
}
