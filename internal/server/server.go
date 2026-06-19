package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/openmule/cli2api/internal/auth"
	"github.com/openmule/cli2api/internal/config"
	"github.com/openmule/cli2api/internal/handler"
	"github.com/openmule/cli2api/internal/jobstore"
	"github.com/openmule/cli2api/internal/mulerun"
)

func New(cfg *config.Config, log *slog.Logger, store jobstore.Store) http.Handler {
	client := mulerun.New(cfg.MulerunBaseURL, cfg.MulerunToken)
	deps := handler.Deps{
		Client:          client,
		ImageTimeout:    cfg.ImageTimeout,
		PollInterval:    cfg.PollInterval,
		PollMaxInterval: cfg.PollMaxInterval,
		JobRetention:    cfg.JobRetention,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(log))
	r.Use(corsMiddleware())
	// Single source of truth for the inbound body cap. The chat/responses
	// handlers also wrap with http.MaxBytesReader(handler.MaxRequestBody)
	// — same constant, so the two never drift.
	r.Use(middleware.RequestSize(handler.MaxRequestBody))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// OpenAI-style surface
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(cfg.APIKeys, auth.StyleOpenAI))
		r.Method(http.MethodPost, "/v1/chat/completions", handler.Chat(deps))
		r.Method(http.MethodPost, "/v1/responses", handler.Responses(deps))
		r.Method(http.MethodPost, "/v1/images/generations", handler.Images(deps))
		r.Method(http.MethodPost, "/v1/images/edits", handler.Edits(deps))
		r.Method(http.MethodPost, "/v1/videos", handler.SubmitVideo(deps, store))
		r.Method(http.MethodGet, "/v1/videos/{id}", handler.GetVideo(deps, store))
		r.Method(http.MethodPost, "/v1/audio/speech", handler.Speech(deps))
		r.Method(http.MethodPost, "/v1/audio/music", handler.SubmitMusic(deps, store))
		r.Method(http.MethodGet, "/v1/audio/music/{id}", handler.GetMusic(deps, store))
		r.Method(http.MethodGet, "/v1/models", handler.Models(deps))
	})

	// Anthropic-style surface
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(cfg.APIKeys, auth.StyleAnthropic))
		r.Method(http.MethodPost, "/v1/messages", handler.Messages(deps))
	})

	return r
}

// requestLogger emits a structured access log per request.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Type, anthropic-version")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
