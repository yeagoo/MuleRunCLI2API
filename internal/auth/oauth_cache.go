package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// oauthCache mirrors the JSON shape mulerun-cli writes to
// ~/.config/mulerun/oauth_cache.json. We keep the fields we use as typed
// columns and the rest as Extra so a write-back round-trip preserves any
// future keys we don't model.
//
// MukKey is cli2api's own extension — the muk- API key we exchanged from
// the access_token. Persisting it lets every subsequent startup skip the
// network round-trip to /api/platform/users/cli-token. mulerun-cli
// ignores unknown fields, so a cli2api-written file remains readable by
// the official CLI.
type oauthCache struct {
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	ExpiresAt    int64          `json:"expires_at,omitempty"`
	BaseURL      string         `json:"base_url,omitempty"`
	MukKey       string         `json:"cli2api_muk_key,omitempty"`
	Extra        map[string]any `json:"-"`
}

// readOAuthCacheFile parses the OAuth cache JSON, preserving unknown keys
// in Extra so writeOAuthCacheFile can round-trip them.
func readOAuthCacheFile(path string) (*oauthCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	generic := map[string]any{}
	if err := json.Unmarshal(data, &generic); err != nil {
		return nil, fmt.Errorf("parse oauth_cache: %w", err)
	}
	c := &oauthCache{Extra: map[string]any{}}
	for k, v := range generic {
		switch k {
		case "access_token":
			if s, ok := v.(string); ok {
				c.AccessToken = s
			}
		case "refresh_token":
			if s, ok := v.(string); ok {
				c.RefreshToken = s
			}
		case "expires_at":
			if f, ok := v.(float64); ok {
				c.ExpiresAt = int64(f)
			}
		case "base_url":
			if s, ok := v.(string); ok {
				c.BaseURL = s
			}
		case "cli2api_muk_key":
			if s, ok := v.(string); ok {
				c.MukKey = s
			}
		default:
			c.Extra[k] = v
		}
	}
	return c, nil
}

// writeOAuthCacheFile writes the cache atomically and unconditionally with
// mode 0600.
//
// Atomicity: uses os.CreateTemp(dir, "...") so concurrent writers each get
// a unique temp name — two cli2api processes refreshing in parallel can't
// stomp on the same `.tmp` file and promote a partial write through
// rename.
//
// Mode: always 0o600. The cache holds a refresh_token; group-readable
// modes would let other users on the host steal it, so we intentionally
// tighten rather than preserve.
//
// Round-trip: when ExpiresAt is 0 the field is OMITTED rather than
// written as `"expires_at": 0`. The reader treats `<=0` as "no expiry
// recorded", so writing 0 would forever-trust a cache that has no real
// expiry — defeating the refresh path. Omission keeps the prior value
// out of the file too, which is the conservative read.
func writeOAuthCacheFile(path string, c *oauthCache) error {
	out := map[string]any{}
	for k, v := range c.Extra {
		out[k] = v
	}
	if c.AccessToken != "" {
		out["access_token"] = c.AccessToken
	}
	if c.RefreshToken != "" {
		out["refresh_token"] = c.RefreshToken
	}
	if c.ExpiresAt > 0 {
		out["expires_at"] = c.ExpiresAt
	}
	if c.BaseURL != "" {
		out["base_url"] = c.BaseURL
	}
	if c.MukKey != "" {
		out["cli2api_muk_key"] = c.MukKey
	}

	data, err := json.Marshal(out)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".oauth_cache.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Clean up the temp file if anything goes wrong before rename.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	cleanup = false
	return nil
}
