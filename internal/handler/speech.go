package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/pkg/apierr"
)

// Speech handles POST /v1/audio/speech — synchronous, OpenAI-compatible.
// Streams the generated audio bytes back to the client.
func Speech(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in registry.AudioInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error")
			return
		}
		if in.Model == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "model is required", "invalid_request_error")
			return
		}
		if in.Input == "" && in.Prompt == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "input is required", "invalid_request_error")
			return
		}
		m, ok := registry.Get(in.Model)
		if !ok || m.Kind != registry.KindAudio || registry.IsMusic(in.Model) || m.MapAudio == nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusNotFound, "unknown speech model: "+in.Model, "model_not_found")
			return
		}
		body, err := m.MapAudio(in)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}

		res, err := d.Client.SubmitAndWait(r.Context(), m.VendorPath, body, d.ImageTimeout, d.PollInterval, d.PollMaxInterval)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "upstream: "+err.Error(), "upstream_error")
			return
		}
		if res.Err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, res.Err.Error(), "vendor_error")
			return
		}
		if len(res.Audios) == 0 {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "upstream returned no audio", "upstream_error")
			return
		}

		if err := streamAudio(r.Context(), w, res.Audios[0], contentTypeFor(in)); err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "audio download: "+err.Error(), "upstream_error")
		}
	}
}

func contentTypeFor(in registry.AudioInput) string {
	switch firstNonEmpty(in.ResponseFormat, in.AudioFormat) {
	case "opus":
		return "audio/ogg; codecs=opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wav":
		return "audio/wav"
	case "pcm":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// streamAudio fetches the upstream URL and writes its body to w with the
// given content type. Uses chunked transfer + per-chunk Flush so the client
// sees bytes as soon as they arrive from the CDN (no client-side buffering
// on the entire body).
func streamAudio(ctx context.Context, w http.ResponseWriter, url, contentType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("upstream %d", resp.StatusCode)
	}
	if contentType == "" {
		contentType = resp.Header.Get("Content-Type")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	// Intentionally omit Content-Length so Go uses chunked transfer encoding;
	// this enables progressive playback at SDKs that respect Transfer-Encoding.
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
