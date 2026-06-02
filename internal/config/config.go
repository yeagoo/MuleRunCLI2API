package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openmule/cli2api/internal/auth"
)

type Config struct {
	Port            string
	APIKeys         []string
	MulerunToken    string
	MulerunBaseURL  string
	ImageTimeout    time.Duration
	PollInterval    time.Duration
	PollMaxInterval time.Duration
	LogLevel        string
	JobStoreDSN     string  // empty = in-memory; otherwise a libsql DSN or file path
	JobRetention    time.Duration // 0 = jobs never expire
	ReaperInterval  time.Duration // 0 = reaper disabled
	JobHardCapMult  float64       // hard-cap multiplier on retention for in-flight jobs

	TokenSource string
}

const (
	defaultPort            = "8080"
	defaultBaseURL         = "https://api.mulerun.com"
	defaultImageTimeout    = 5 * time.Minute
	defaultPollInterval    = 2 * time.Second
	defaultPollMaxInterval = 10 * time.Second
	defaultLogLevel        = "info"
	defaultJobRetention    = 7 * 24 * time.Hour
	defaultReaperInterval  = time.Hour
	defaultJobHardCapMult  = 3.0
)

func Load() (*Config, error) {
	c := &Config{
		Port:            envOr("CLI2API_PORT", defaultPort),
		MulerunBaseURL:  strings.TrimRight(envOr("MULERUN_API_BASE_URL", defaultBaseURL), "/"),
		ImageTimeout:    durationEnv("CLI2API_IMAGE_TIMEOUT", defaultImageTimeout),
		PollInterval:    durationEnv("CLI2API_POLL_INTERVAL", defaultPollInterval),
		PollMaxInterval: durationEnv("CLI2API_POLL_MAX_INTERVAL", defaultPollMaxInterval),
		LogLevel:        strings.ToLower(envOr("CLI2API_LOG_LEVEL", defaultLogLevel)),
		JobStoreDSN:     strings.TrimSpace(os.Getenv("CLI2API_JOBSTORE_DSN")),
		JobRetention:    durationEnv("CLI2API_JOB_RETENTION", defaultJobRetention),
		ReaperInterval:  durationEnv("CLI2API_REAPER_INTERVAL", defaultReaperInterval),
		JobHardCapMult:  floatEnv("CLI2API_JOB_HARD_CAP_MULT", defaultJobHardCapMult),
	}

	if raw := strings.TrimSpace(os.Getenv("CLI2API_API_KEYS")); raw != "" {
		for _, k := range strings.Split(raw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				c.APIKeys = append(c.APIKeys, k)
			}
		}
	}

	token, source, err := auth.DiscoverToken()
	if err != nil {
		return nil, fmt.Errorf("discover mulerun token: %w", err)
	}
	if token == "" {
		return nil, errors.New("no mulerun credentials found; run `mulerun login` or set MULERUN_TOKEN")
	}
	c.MulerunToken = token
	c.TokenSource = source

	return c, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		return time.Duration(secs) * time.Second
	}
	return fallback
}

func floatEnv(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	// Reject Inf / NaN / out-of-range values that would overflow
	// time.Duration math downstream. Cap at a sane upper bound (1000×).
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 1 || f > 1000 {
		return fallback
	}
	return f
}
