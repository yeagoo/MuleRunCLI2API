package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

// version is the build version, injected at link time via
//   -ldflags "-X main.version=v1.2.3"
// Defaults to "dev" for local `go build` / `make build`.
var version = "dev"

func main() {
	// --version / -v: print and exit before any config/credential work.
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-v" || a == "version" {
			println("cli2api " + version)
			return
		}
	}

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
	jobstore.StartReaper(ctx, store, cfg.ReaperInterval, cfg.JobRetention, cfg.JobHardCapMult, log)

	log.Info("startup",
		"version", version,
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
		"job_hard_cap_mult", cfg.JobHardCapMult,
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
	return s, "libsql:" + redactDSN(dsn), nil
}

// redactDSN strips secrets from a libsql/HTTP DSN so it's safe to log.
// Drops userinfo entirely (the username field is often used to carry bearer
// tokens, e.g. `libsql://token@host`) and replaces sensitive query params
// (authToken, auth_token, jwt, password) with "***". File paths and bare
// paths pass through unchanged.
func redactDSN(dsn string) string {
	if strings.HasPrefix(dsn, "file:") || !strings.Contains(dsn, "://") {
		return dsn
	}
	u, err := url.Parse(dsn)
	if err != nil {
		// Unparseable — refuse to guess; show only the scheme.
		if i := strings.Index(dsn, "://"); i > 0 {
			return dsn[:i+3] + "***"
		}
		return "***"
	}
	if u.User != nil {
		// Both username and password can carry secrets; drop the whole
		// userinfo and replace with a marker so the existence is visible.
		u.User = url.User("***")
	}
	q := u.Query()
	// Match secret-bearing query keys case-insensitively. Turso/libsql
	// dashboards commonly emit ?AuthToken=... (PascalCase); a strict
	// lowercase match would leak the token to logs.
	secrets := map[string]struct{}{
		"authtoken":  {},
		"auth_token": {},
		"jwt":        {},
		"password":   {},
	}
	for k := range q {
		if _, hit := secrets[strings.ToLower(k)]; hit {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
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
