package registry

// Subtype: which audio surface a model belongs to. Stored under model.Vendor's
// suffix via the model.ID convention; handlers look up by ID directly.

// IsMusic returns true for music-* model IDs. Used by handlers to route
// audio jobs to the right surface (/v1/audio/speech vs /v1/audio/music).
func IsMusic(modelID string) bool {
	return len(modelID) >= 5 && modelID[:5] == "music"
}

// buildAudioSetting builds the upstream `audio_setting` sub-object shared by
// speech and music. Returns nil when no fields are set so callers can omit
// the key entirely.
func buildAudioSetting(in AudioInput) map[string]any {
	out := map[string]any{}
	if f := firstNonEmpty(in.AudioFormat, in.ResponseFormat); f != "" {
		out["format"] = f
	}
	putNonNilInt(out, "sample_rate", in.SampleRate)
	putNonNilInt(out, "bitrate", in.Bitrate)
	if len(out) == 0 {
		return nil
	}
	return out
}

func init() {
	// ---- MiniMax text-to-speech -----------------------------------------
	//
	// Mulerun's upstream API expects nested objects:
	//   voice_setting: { voice_id, speed, vol, pitch, emotion, language_boost }
	//   audio_setting: { format, sample_rate, bitrate }
	//   english_normalization, output_format: top-level
	//
	// Verified against @mulerouter/core 0.5.0 buildSpeechRequestBody().
	// Sending these flat → upstream returns 400
	// "voice_setting expected to be provided".
	speechMapper := func(in AudioInput) (map[string]any, error) {
		text := firstNonEmpty(in.Input, in.Prompt)
		out := map[string]any{
			"prompt": text,
		}

		voiceSetting := map[string]any{}
		if v := firstNonEmpty(in.VoiceID, in.Voice); v != "" {
			voiceSetting["voice_id"] = v
		}
		putNonNilFloat(voiceSetting, "speed", in.Speed)
		putNonNilFloat(voiceSetting, "vol", in.Vol)
		putNonNilInt(voiceSetting, "pitch", in.Pitch)
		putNonEmpty(voiceSetting, "emotion", in.Emotion)
		putNonEmpty(voiceSetting, "language_boost", in.LanguageBoost)
		if len(voiceSetting) > 0 {
			out["voice_setting"] = voiceSetting
		}

		audioSetting := buildAudioSetting(in)
		if audioSetting != nil {
			out["audio_setting"] = audioSetting
		}

		putNonNilBool(out, "english_normalization", in.EnglishNormalization)
		// Always ask upstream for a URL — the handler then streams bytes back.
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
	//
	// Same nesting pattern: audio_format / sample_rate / bitrate go inside
	// audio_setting (not flat). Verified against
	// @mulerouter/core buildMusicRequestBody().
	musicMapper := func(in AudioInput) (map[string]any, error) {
		out := map[string]any{}
		putNonEmpty(out, "prompt", firstNonEmpty(in.Prompt, in.Input))
		putNonEmpty(out, "lyrics_prompt", in.LyricsPrompt)
		putNonNilBool(out, "lyrics_optimizer", in.LyricsOptimizer)

		audioSetting := buildAudioSetting(in)
		if audioSetting != nil {
			out["audio_setting"] = audioSetting
		}

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
