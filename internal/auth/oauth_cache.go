package auth

import (
	"encoding/json"
	"fmt"
	"os"
)

// oauthCache mirrors the JSON shape mulerun-cli writes to
// ~/.config/mulerun/oauth_cache.json. We keep the fields we use as typed
// columns and the rest as `extra` so a write-back round-trip preserves any
// future keys we don't know about.
type oauthCache struct {
	AccessToken  string         `json:"access_token,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	ExpiresAt    int64          `json:"expires_at,omitempty"`
	BaseURL      string         `json:"base_url,omitempty"`
	Extra        map[string]any `json:"-"` // everything we don't model
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
		default:
			c.Extra[k] = v
		}
	}
	return c, nil
}

// writeOAuthCacheFile writes the cache atomically (temp file + rename) and
// preserves mode 0600 to avoid leaking refresh_token to other users.
func writeOAuthCacheFile(path string, c *oauthCache) error {
	out := map[string]any{}
	for k, v := range c.Extra {
		out[k] = v
	}
	out["access_token"] = c.AccessToken
	out["refresh_token"] = c.RefreshToken
	out["expires_at"] = c.ExpiresAt
	if c.BaseURL != "" {
		out["base_url"] = c.BaseURL
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
