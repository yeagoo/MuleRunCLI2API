package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DiscoverToken returns a mulerun token plus a human-readable source string.
//
// Resolution order:
//  1. MULERUN_TOKEN env var
//  2. ~/.config/mulerun/oauth_cache.json (mulerun-cli >= 0.1.0)
//  3. ~/.mulerun/auth.json | credentials.json | token.json (older)
//
// In every file we accept any of `token`, `access_token`, `accessToken`.
//
// IMPORTANT: mulerun's API gateway (api.mulerun.com) authenticates with a
// "muk-" API key, NOT the OAuth/JWT access_token that `mulerun login` caches.
// The JWT is rejected with HTTP 401 "Invalid API Key format" on every
// upstream endpoint (image/video/audio AND chat). The muk- key is a stable,
// long-lived per-account key. Set MULERUN_TOKEN to it. We still read the
// OAuth cache as a fallback so the server can *start*, but warnIfJWT() flags
// that upstream calls will 401 until a muk- key is provided.
func DiscoverToken() (token string, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MULERUN_TOKEN")); v != "" {
		warnIfJWT(v, "env:MULERUN_TOKEN")
		return v, "env:MULERUN_TOKEN", nil
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("locate home dir: %w", herr)
	}

	candidates := []string{
		filepath.Join(home, ".config", "mulerun", "oauth_cache.json"),
		filepath.Join(home, ".mulerun", "auth.json"),
		filepath.Join(home, ".mulerun", "credentials.json"),
		filepath.Join(home, ".mulerun", "token.json"),
	}
	for _, path := range candidates {
		t, perr := readTokenFile(path)
		if perr != nil {
			// ENOENT is normal — that path just isn't populated.
			// Other errors (permission denied, malformed JSON, truncated
			// write from a crashed `mulerun login`) are noteworthy: log
			// them at warn so a corrupt cache doesn't silently fall
			// through to "no creds found", but don't abort the loop.
			if !errors.Is(perr, os.ErrNotExist) {
				slog.Warn("credential file unreadable, skipping",
					"path", path, "err", perr)
			}
			continue
		}
		if t != "" {
			warnIfJWT(t, "file:"+path)
			return t, "file:" + path, nil
		}
	}

	return "", "", nil
}

// warnIfJWT logs a loud warning when the resolved token is a JWT (OAuth
// session token) rather than a "muk-" API key. The mulerun gateway rejects
// JWTs, so every upstream call would 401 — surfacing this at startup turns a
// cryptic "502 upstream HTTP 401" into an actionable message.
func warnIfJWT(tok, source string) {
	if strings.HasPrefix(tok, "muk-") {
		return // proper API key
	}
	if strings.HasPrefix(tok, "eyJ") { // base64url of `{"` — a JWT header
		slog.Warn("credential looks like an OAuth JWT, not a muk- API key; "+
			"upstream calls will fail with 401 'Invalid API Key format'. "+
			"Set MULERUN_TOKEN to your muk- key (see docs: Troubleshooting).",
			"source", source)
	}
}

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	// If the file carries an `expires_at` (unix seconds, mulerun-cli format)
	// and it's already past, treat the token as missing — using it just
	// produces 401s that look like cli2api bugs.
	if exp, ok := generic["expires_at"]; ok {
		if expSec, ok := exp.(float64); ok && expSec > 0 && time.Now().Unix() >= int64(expSec) {
			return "", nil
		}
	}
	for _, key := range []string{"token", "access_token", "accessToken"} {
		if v, ok := generic[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s), nil
			}
		}
	}
	return "", nil
}
