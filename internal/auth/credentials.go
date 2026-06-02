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
//  2. ~/.mulerun/auth.json | credentials.json | token.json
//     parsed for a string field named `token`, `access_token`, or `accessToken`.
func DiscoverToken() (token string, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MULERUN_TOKEN")); v != "" {
		return v, "env:MULERUN_TOKEN", nil
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("locate home dir: %w", herr)
	}
	dir := filepath.Join(home, ".mulerun")

	candidates := []string{"auth.json", "credentials.json", "token.json"}
	for _, name := range candidates {
		path := filepath.Join(dir, name)
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
