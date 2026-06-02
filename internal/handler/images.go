package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/pkg/apierr"
)

type imageResponse struct {
	Created int64       `json:"created"`
	Data    []imageItem `json:"data"`
}

type imageItem struct {
	URL string `json:"url"`
}

// Images returns the synchronous OpenAI /v1/images/generations handler.
func Images(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in registry.ImageInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error")
			return
		}
		if in.Prompt == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "prompt is required", "invalid_request_error")
			return
		}
		if in.Model == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "model is required", "invalid_request_error")
			return
		}
		m, ok := registry.Get(in.Model)
		if !ok || m.Kind != registry.KindImage || m.MapImage == nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusNotFound, "unknown image model: "+in.Model, "model_not_found")
			return
		}
		body, err := m.MapImage(in)
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
		urls := res.Images
		if len(urls) == 0 && len(res.Videos) > 0 {
			// Midjourney's text-to-image returns under "images"; safe-guard.
			urls = res.Videos
		}
		items := make([]imageItem, 0, len(urls))
		for _, u := range urls {
			items = append(items, imageItem{URL: u})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(imageResponse{
			Created: time.Now().Unix(),
			Data:    items,
		})
	}
}
