package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// oauthCachePath is the canonical mulerun-cli 0.1.0+ cache location.
// Pulled into a const because credentials.go and the refresh writeback path
// both need it and must agree.
const oauthCacheFilename = "oauth_cache.json"

// DiscoverToken returns a mulerun-gateway-ready muk- API key plus a
// human-readable source string.
//
// Resolution order:
//  1. MULERUN_TOKEN env var
//  2. ~/.config/mulerun/oauth_cache.json (mulerun-cli >= 0.1.0)
//  3. ~/.mulerun/auth.json | credentials.json | token.json (older)
//
// Auto-upgrade for OAuth JWTs: when the resolved token is a JWT
// (eyJ-prefixed) we POST it to the mulerun platform's CLI-token endpoint to
// obtain the stable muk- API key the gateway accepts, and return THAT.
// When the source is an oauth_cache.json whose access_token has expired,
// we refresh it via the OAuth2 refresh endpoint and write the new tokens
// back to the same file (atomic, mode-preserving), then exchange.
//
// Graceful degradation: if any of those network steps fail we return the
// raw token with a slog.Warn so the user can still see *which* step broke.
// The old "muk- required" warning still fires for non-JWT non-muk- shapes
// that wouldn't survive the gateway either way.
func DiscoverToken() (token string, source string, err error) {
	raw, src, err := discoverRaw()
	if err != nil {
		return "", "", err
	}
	if raw == "" {
		return "", "", nil
	}
	return maybeExchange(raw, src), src, nil
}

// discoverRaw resolves the raw credential (JWT, muk-, or legacy string)
// without any HTTP work. Refresh-on-read for oauth_cache.json happens here.
func discoverRaw() (token string, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MULERUN_TOKEN")); v != "" {
		return v, "env:MULERUN_TOKEN", nil
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("locate home dir: %w", herr)
	}

	oauthCache := filepath.Join(home, ".config", "mulerun", oauthCacheFilename)
	if t := tryOAuthCache(oauthCache); t != "" {
		return t, "file:" + oauthCache, nil
	}

	for _, path := range []string{
		filepath.Join(home, ".mulerun", "auth.json"),
		filepath.Join(home, ".mulerun", "credentials.json"),
		filepath.Join(home, ".mulerun", "token.json"),
	} {
		t, perr := readSimpleTokenFile(path)
		if perr != nil {
			if !errors.Is(perr, os.ErrNotExist) {
				slog.Warn("credential file unreadable, skipping", "path", path, "err", perr)
			}
			continue
		}
		if t != "" {
			return t, "file:" + path, nil
		}
	}
	return "", "", nil
}

// tryOAuthCache reads ~/.config/mulerun/oauth_cache.json, refreshes the
// access_token if it's expired and a refresh_token is present, writes the
// rotated tokens back to disk, and returns whatever access_token is now
// valid (or "" if no usable credential could be produced).
func tryOAuthCache(path string) string {
	cache, err := readOAuthCacheFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("oauth_cache.json unreadable, skipping", "path", path, "err", err)
		}
		return ""
	}
	if cache.AccessToken == "" {
		return ""
	}

	now := time.Now().Unix()
	// Missing or zero expires_at = "no expiry recorded"; preserve the
	// pre-Tier-3 behavior of trusting it. Only refresh when there's an
	// explicit expiry that has actually passed.
	if cache.ExpiresAt <= 0 || now < cache.ExpiresAt {
		return cache.AccessToken
	}

	if cache.RefreshToken == "" {
		// Expired and no way to refresh. Treat as missing (old behavior).
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	defer cancel()
	tok, rerr := refreshOAuth(ctx, cache.RefreshToken)
	if rerr != nil {
		slog.Warn("oauth refresh failed; re-run `mulerun login`",
			"path", path, "err", rerr)
		return ""
	}

	cache.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cache.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		cache.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn)
	}
	if werr := writeOAuthCacheFile(path, cache); werr != nil {
		// Refresh succeeded server-side; we'll still use the in-memory
		// access_token, but warn loudly — without the writeback the next
		// startup will try the now-invalidated refresh_token.
		slog.Warn("refreshed JWT but failed to persist back to cache; "+
			"the next startup will need `mulerun login` again",
			"path", path, "err", werr)
	} else {
		slog.Info("refreshed expired OAuth tokens", "path", path)
	}
	return cache.AccessToken
}

// maybeExchange turns a JWT into its corresponding muk- key. For anything
// else (muk-..., legacy "mr_*", arbitrary strings) it returns the input
// unchanged so existing user configs keep working. Failures degrade to the
// raw token with a warning rather than crashing startup — that matches the
// pre-Tier-3 behavior, just with better diagnostics.
func maybeExchange(raw, source string) string {
	if strings.HasPrefix(raw, "muk-") {
		return raw
	}
	if !looksLikeJWT(raw) {
		warnIfJWT(raw, source) // legacy classifier; warns on weird shapes
		return raw
	}

	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout)
	defer cancel()
	muk, err := exchangeJWTForMUK(ctx, raw)
	if err != nil {
		slog.Warn("JWT → muk- exchange failed; upstream calls will likely 401. "+
			"Set MULERUN_TOKEN to a muk- key, or re-run `mulerun login` and retry.",
			"source", source, "err", err)
		return raw
	}
	slog.Info("exchanged OAuth JWT for muk- API key", "source", source)
	return muk
}

func looksLikeJWT(s string) bool { return strings.HasPrefix(s, "eyJ") }

// warnIfJWT is the legacy classifier kept for the case where exchange was
// never attempted (e.g. a raw token that doesn't match any known prefix).
// JWTs themselves now route through maybeExchange instead.
func warnIfJWT(tok, source string) {
	if strings.HasPrefix(tok, "muk-") || looksLikeJWT(tok) {
		return
	}
	// Unknown shape — could be a stale token format. Don't classify; let
	// upstream tell us if it's invalid.
	_ = source
}

// readSimpleTokenFile is the legacy path reader used for the older
// ~/.mulerun/*.json caches. Accepts `token`, `access_token`, or
// `accessToken` as the field name.
func readSimpleTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return "", fmt.Errorf("parse json: %w", err)
	}
	// Same expires_at gate the legacy path always had — keep it so a stale
	// cache doesn't return a token the gateway will reject.
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
