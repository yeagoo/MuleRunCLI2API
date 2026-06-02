package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/openmule/cli2api/internal/config"
	"github.com/openmule/cli2api/internal/jobstore"
	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Bootstrap logger only — full logger needs cfg.LogLevel.
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	log := newLogger(cfg.LogLevel)

	store, storeDesc, err := openJobStore(cfg.JobStoreDSN)
	if err != nil {
		log.Error("job store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobstore.StartReaper(ctx, store, cfg.ReaperInterval, log)

	log.Info("startup",
		"addr", ":"+cfg.Port,
		"upstream", cfg.MulerunBaseURL,
		"token_source", cfg.TokenSource,
		"image_timeout", cfg.ImageTimeout.String(),
		"poll_interval", cfg.PollInterval.String(),
		"registered_models", len(registry.All()),
		"auth_required", len(cfg.APIKeys) > 0,
		"jobstore", storeDesc,
		"job_retention", cfg.JobRetention.String(),
		"reaper_interval", cfg.ReaperInterval.String(),
	)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.New(cfg, log, store),
		ReadHeaderTimeout: 10 * time.Second,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Info("shutdown initiated")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Error("shutdown", "err", err)
		}
		close(idleConnsClosed)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	<-idleConnsClosed
}

// openJobStore picks the backend based on CLI2API_JOBSTORE_DSN:
//   - empty               → in-memory (default; jobs lost on restart)
//   - file:/abs/jobs.db   → libsql local (libsql-client-go + modernc.org/sqlite)
//   - libsql://host?...   → Turso / sqld remote
//   - bare path           → same as file:bare path
func openJobStore(dsn string) (jobstore.Store, string, error) {
	if dsn == "" {
		return jobstore.NewMemory(), "memory", nil
	}
	s, err := jobstore.OpenLibSQL(dsn)
	if err != nil {
		return nil, "", err
	}
	return s, "libsql:" + dsn, nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
