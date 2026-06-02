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
// Caveat: the access_token from `mulerun login` is an OAuth/JWT token
// scoped to the studio plane only. It will NOT authenticate against the
// chat completions / messages endpoints, which expect a separate
// "muk-" exchange-key issued by mulerun's API gateway. Set MULERUN_TOKEN
// to that key for chat surfaces.
func DiscoverToken() (token string, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MULERUN_TOKEN")); v != "" {
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
			return t, "file:" + path, nil
		}
	}

	return "", "", nil
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
