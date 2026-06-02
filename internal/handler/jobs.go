package handler

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openmule/cli2api/internal/jobstore"
	"github.com/openmule/cli2api/internal/mulerun"
	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/pkg/apierr"
)

// asyncJobAPI captures the small variations between the video and music
// surfaces. The submit-and-poll machinery is otherwise identical.
type asyncJobAPI struct {
	kind        jobstore.Kind
	prefix      string                  // "video_" / "audio_"
	objectName  string                  // "video.job" / "audio.job"
	resultField string                  // "videos" / "audios"
	expectKind  registry.Kind           // registry.KindVideo / registry.KindAudio
	extra       func(registry.Model) bool
}

func newLocalID(prefix string) string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return prefix + strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b[:]), "="))
}

// SubmitJob handles the POST half of an async job endpoint.
// payload is the typed inbound shape (VideoInput or AudioInput).
func (h asyncJobAPI) submit(
	d Deps, store jobstore.Store,
	parse func(r *http.Request) (modelID, prompt string, body any, mapper func(registry.Model) (map[string]any, error), err *apiError),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modelID, prompt, _, mapper, perr := parse(r)
		if perr != nil {
			apierr.Write(w, apierr.StyleOpenAI, perr.status, perr.msg, perr.typ)
			return
		}
		if prompt == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "prompt/input is required", "invalid_request_error")
			return
		}
		if modelID == "" {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, "model is required", "invalid_request_error")
			return
		}
		m, ok := registry.Get(modelID)
		if !ok || m.Kind != h.expectKind || (h.extra != nil && !h.extra(m)) {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusNotFound, "unknown "+string(h.kind)+" model: "+modelID, "model_not_found")
			return
		}
		body, err := mapper(m)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		vendorTaskID, err := d.Client.Submit(r.Context(), m.VendorPath, body)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "upstream submit failed: "+err.Error(), "upstream_error")
			return
		}

		job := &jobstore.Job{
			LocalID:      newLocalID(h.prefix),
			Kind:         h.kind,
			Model:        modelID,
			VendorPath:   m.VendorPath,
			VendorTaskID: vendorTaskID,
			CreatedAt:    time.Now().Unix(),
			Status:       "queued",
		}
		if d.JobRetention > 0 {
			job.ExpiresAt = job.CreatedAt + int64(d.JobRetention.Seconds())
		}
		if err := store.Put(r.Context(), job); err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusInternalServerError, "store: "+err.Error(), "internal_error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         job.LocalID,
			"object":     h.objectName,
			"status":     job.Status,
			"model":      job.Model,
			"created_at": job.CreatedAt,
		})
	}
}

func (h asyncJobAPI) get(d Deps, store jobstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		job, err := store.Get(r.Context(), id)
		if err != nil {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusInternalServerError, "store: "+err.Error(), "internal_error")
			return
		}
		if job == nil || job.Kind != h.kind {
			apierr.Write(w, apierr.StyleOpenAI, http.StatusNotFound, string(h.kind)+" job not found: "+id, "not_found")
			return
		}

		if job.Status != "completed" && job.Status != "failed" {
			res, done, perr := d.Client.Poll(r.Context(), job.VendorPath, job.VendorTaskID)
			if perr != nil {
				apierr.Write(w, apierr.StyleOpenAI, http.StatusBadGateway, "upstream poll failed: "+perr.Error(), "upstream_error")
				return
			}
			if done {
				if res.Err != nil {
					job.Status = "failed"
					job.ErrCode = res.Err.Code
					job.ErrMessage = res.Err.Error()
				} else {
					job.Status = "completed"
					if h.kind == jobstore.KindVideo {
						job.ResultURLs = res.Videos
					} else {
						job.ResultURLs = res.Audios
					}
				}
				job.CompletedAt = time.Now().Unix()
			} else {
				job.Status = liveStatus(res.Status)
			}
			if err := store.Put(r.Context(), job); err != nil {
				// log-but-continue: serving the response is still useful
			}
		}

		out := map[string]any{
			"id":         job.LocalID,
			"object":     h.objectName,
			"status":     job.Status,
			"model":      job.Model,
			"created_at": job.CreatedAt,
		}
		switch job.Status {
		case "completed":
			out[h.resultField] = job.ResultURLs
			out["completed_at"] = job.CompletedAt
		case "failed":
			out["error"] = map[string]any{"code": job.ErrCode, "message": job.ErrMessage}
			out["completed_at"] = job.CompletedAt
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func liveStatus(upstream string) string {
	switch upstream {
	case "processing", "running":
		return "in_progress"
	default:
		return "queued"
	}
}

// SubmitVideo / GetVideo are the POST and GET endpoints for /v1/videos.
func SubmitVideo(d Deps, store jobstore.Store) http.HandlerFunc {
	api := asyncJobAPI{
		kind: jobstore.KindVideo, prefix: "video_",
		objectName: "video.job", resultField: "videos",
		expectKind: registry.KindVideo,
	}
	return api.submit(d, store, parseVideoInput)
}

func GetVideo(d Deps, store jobstore.Store) http.HandlerFunc {
	api := asyncJobAPI{
		kind: jobstore.KindVideo, prefix: "video_",
		objectName: "video.job", resultField: "videos",
		expectKind: registry.KindVideo,
	}
	return api.get(d, store)
}

// SubmitMusic / GetMusic are the POST and GET endpoints for /v1/audio/music.
func SubmitMusic(d Deps, store jobstore.Store) http.HandlerFunc {
	api := asyncJobAPI{
		kind: jobstore.KindMusic, prefix: "audio_",
		objectName: "audio.job", resultField: "audios",
		expectKind: registry.KindAudio,
		extra: func(m registry.Model) bool {
			return registry.IsMusic(m.ID)
		},
	}
	return api.submit(d, store, parseAudioInput)
}

func GetMusic(d Deps, store jobstore.Store) http.HandlerFunc {
	api := asyncJobAPI{
		kind: jobstore.KindMusic, prefix: "audio_",
		objectName: "audio.job", resultField: "audios",
		expectKind: registry.KindAudio,
	}
	return api.get(d, store)
}

type apiError struct {
	status int
	msg    string
	typ    string
}

func parseVideoInput(r *http.Request) (modelID, prompt string, body any, mapper func(registry.Model) (map[string]any, error), err *apiError) {
	var in registry.VideoInput
	if derr := json.NewDecoder(r.Body).Decode(&in); derr != nil {
		return "", "", nil, nil, &apiError{http.StatusBadRequest, "invalid JSON body: " + derr.Error(), "invalid_request_error"}
	}
	return in.Model, in.Prompt, in, func(m registry.Model) (map[string]any, error) { return m.MapVideo(in) }, nil
}

func parseAudioInput(r *http.Request) (modelID, prompt string, body any, mapper func(registry.Model) (map[string]any, error), err *apiError) {
	var in registry.AudioInput
	if derr := json.NewDecoder(r.Body).Decode(&in); derr != nil {
		return "", "", nil, nil, &apiError{http.StatusBadRequest, "invalid JSON body: " + derr.Error(), "invalid_request_error"}
	}
	p := in.Prompt
	if p == "" {
		p = in.Input
	}
	return in.Model, p, in, func(m registry.Model) (map[string]any, error) { return m.MapAudio(in) }, nil
}

// keep imports tidy
var _ = mulerun.AuthBearer
var _ = context.Background
