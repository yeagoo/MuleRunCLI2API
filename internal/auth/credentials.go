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

// oauthCacheFilename is the basename mulerun-cli writes under
// ~/.config/mulerun/. Used as the default cache path when neither
// CLI2API_TOKEN_CACHE nor an existing legacy file is set.
const oauthCacheFilename = "oauth_cache.json"

// DiscoverToken returns a mulerun-gateway-ready muk- API key plus a
// human-readable source string.
//
// Resolution order:
//  1. MULERUN_TOKEN env var (muk- used as-is; JWT exchanged at startup,
//     not cached to disk — see env-JWT caveat below)
//  2. CLI2API_TOKEN_CACHE if set, else ~/.config/mulerun/oauth_cache.json
//     — cached muk- preferred over (re-)exchange; access_token refreshed
//     when expired; new tokens + exchanged muk- written back under a
//     file lock so concurrent cli2api/mulerun-cli processes don't race.
//  3. ~/.mulerun/{auth,credentials,token}.json (older mulerun-cli formats)
//
// Env-JWT caveat: a JWT passed via MULERUN_TOKEN is exchanged for a muk-
// at startup but NOT persisted (we don't write to env). Restarts re-pay
// the round-trip. To benefit from disk caching, point CLI2API_TOKEN_CACHE
// at a writable JSON file containing your JWT instead.
//
// Graceful degradation: HTTP exchange/refresh failures are logged at WARN
// and the raw token is returned so startup proceeds — better one cryptic
// 401 from the gateway than a refusal to start. The transient-vs-permanent
// distinction (errOAuthTransient) is honored: a 5xx during refresh leaves
// the cached refresh_token intact.
func DiscoverToken() (token string, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("MULERUN_TOKEN")); v != "" {
		return finalize(v, "env:MULERUN_TOKEN", "", nil), "env:MULERUN_TOKEN", nil
	}

	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", "", fmt.Errorf("locate home dir: %w", herr)
	}

	cachePath := tokenCachePath(home)
	if t, src := tryCacheFile(cachePath); t != "" {
		return t, src, nil
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
			return finalize(t, "file:"+path, "", nil), "file:" + path, nil
		}
	}
	return "", "", nil
}

// tokenCachePath returns the path cli2api uses for the OAuth cache.
// CLI2API_TOKEN_CACHE wins; otherwise default to the mulerun-cli location.
func tokenCachePath(home string) string {
	if v := strings.TrimSpace(os.Getenv("CLI2API_TOKEN_CACHE")); v != "" {
		return v
	}
	return filepath.Join(home, ".config", "mulerun", oauthCacheFilename)
}

// tryCacheFile handles the cache-file path: serves cached muk- when valid,
// refreshes expired access_tokens (writing rotated triples back atomically),
// and exchanges access_token for muk- when no cached muk- is present.
// Returns ("", "") when no usable credential is on disk.
//
// All disk I/O is wrapped in a per-cache flock so concurrent cli2api
// processes (and concurrent `mulerun login`) serialize through the
// rotating refresh_token instead of racing it.
func tryCacheFile(path string) (token, source string) {
	var (
		out    string
		outSrc string
	)
	_ = withCacheLock(path, func() error {
		cache, err := readOAuthCacheFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				slog.Warn("oauth cache unreadable, skipping", "path", path, "err", err)
			}
			return nil
		}

		if cache.AccessToken == "" && cache.MukKey == "" {
			return nil
		}

		// Explicitly-expired access_token with no refresh path: treat as
		// missing rather than handing the gateway a known-stale token
		// (matches the pre-Tier-3 behavior). The cached muk- still has
		// value though — muk- doesn't ride on access_token expiry — so
		// serve it if present.
		now := time.Now().Unix()
		expired := cache.ExpiresAt > 0 && now >= cache.ExpiresAt
		if expired && cache.RefreshToken == "" {
			if cache.MukKey != "" {
				out = cache.MukKey
				outSrc = "file:" + path + " (cached muk-)"
			}
			return nil
		}

		// Refresh-on-read when the access_token has an explicit, past
		// expiry AND we have a refresh_token to use. A successful
		// refresh clears MukKey: the rotated tokens may belong to a
		// different account (mulerun logout + login between writes),
		// so re-exchange below rather than serving a possibly-stale
		// muk-.
		if expired {
			if !refreshIntoCache(cache, path) {
				return nil
			}
		}

		// Fast path: cached muk- still valid (no refresh just happened,
		// or refresh didn't clear it). The muk- key is stable per
		// account so this is safe until the gateway rejects it.
		if cache.MukKey != "" {
			out = cache.MukKey
			outSrc = "file:" + path + " (cached muk-)"
			return nil
		}

		// Exchange the (possibly-refreshed) access_token for a muk-,
		// then persist the muk- so future startups can fast-path.
		muk := finalize(cache.AccessToken, "file:"+path, path, cache)
		out = muk
		outSrc = "file:" + path
		return nil
	})
	return out, outSrc
}

// refreshIntoCache POSTs the cache's refresh_token to /oauth2/token,
// updates the cache in-memory with the rotated triple, and writes the
// cache back to disk. Returns true when the caller should proceed with
// cache.AccessToken (refresh succeeded or was transient), false when the
// caller should treat the cache as unusable (e.g. refresh_token rejected
// as invalid_grant).
func refreshIntoCache(cache *oauthCache, path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout())
	defer cancel()

	tok, rerr := refreshOAuth(ctx, cache.RefreshToken)
	if rerr != nil {
		if errors.Is(rerr, errOAuthTransient) {
			// Server-side rotation almost certainly didn't happen. Use
			// the (likely still valid) cached access_token for THIS
			// startup; don't touch the on-disk refresh_token. Next
			// startup will retry.
			slog.Warn("oauth refresh transient failure; using cached access_token unchanged",
				"path", path, "err", rerr)
			return true
		}
		slog.Warn("oauth refresh failed; re-run `mulerun login`",
			"path", path, "err", rerr)
		return false
	}

	cache.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cache.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresIn > 0 {
		cache.ExpiresAt = time.Now().Unix() + int64(tok.ExpiresIn)
	}
	// MukKey is invalidated by a refresh — different access_token, so the
	// previously-exchanged muk- might be for a different account if the
	// user re-logged-in between writes. Clear and re-exchange below.
	cache.MukKey = ""

	if werr := writeOAuthCacheFile(path, cache); werr != nil {
		slog.Warn("refreshed JWT but failed to persist back to cache; "+
			"next startup will need `mulerun login` again",
			"path", path, "err", werr)
	} else {
		slog.Info("refreshed expired OAuth tokens", "path", path)
	}
	return true
}

// finalize turns the resolved raw credential into a muk- key (exchanging
// if needed) and, when a cache path was the source, writes the muk- back
// so future startups skip the exchange.
//
// raw: the resolved credential (muk-, JWT, or legacy string).
// source: the source label for log context.
// cachePath: empty when there's nowhere to persist muk-; non-empty when
// reading from a cache file (we'll write muk- back into it).
// cache: the parsed cache struct to mutate before write; nil when caller
// has no cache state.
//
// On exchange failure the raw token is returned with a WARN — the
// gateway will then 401 with a clear error, but the server still starts.
func finalize(raw, source, cachePath string, cache *oauthCache) string {
	if strings.HasPrefix(raw, "muk-") {
		return raw
	}
	if !strings.HasPrefix(raw, "eyJ") {
		// Unknown shape — could be a stale legacy token. Pass through;
		// upstream will tell us if it's invalid.
		return raw
	}

	ctx, cancel := context.WithTimeout(context.Background(), oauthHTTPTimeout())
	defer cancel()

	muk, err := exchangeJWTForMUK(ctx, raw)
	if err != nil {
		slog.Warn("JWT → muk- exchange failed; upstream calls will likely 401. "+
			"Set MULERUN_TOKEN to a muk- key, or re-run `mulerun login` and retry.",
			"source", source, "err", err)
		return raw
	}
	slog.Info("exchanged OAuth JWT for muk- API key", "source", source)

	if cachePath != "" && cache != nil {
		cache.MukKey = muk
		if werr := writeOAuthCacheFile(cachePath, cache); werr != nil {
			slog.Warn("failed to cache muk- back to file; next startup will re-exchange",
				"path", cachePath, "err", werr)
		}
	}
	return muk
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
