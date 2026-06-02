package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
			if errors.Is(perr, os.ErrNotExist) {
				continue
			}
			return "", "", fmt.Errorf("read %s: %w", path, perr)
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
	for _, key := range []string{"token", "access_token", "accessToken"} {
		if v, ok := generic[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s), nil
			}
		}
	}
	return "", nil
}
