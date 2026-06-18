package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- #1: writeOAuthCacheFile uses unique temp names ---

func TestWriteOAuthCacheFile_ConcurrentWritesNoCorruption(t *testing.T) {
	// Before fix: both writers used path+".tmp", so one rename could
	// promote a partially-written file. After fix: os.CreateTemp gives
	// each writer a unique temp, and the final rename is atomic per-file.
	dir := t.TempDir()
	path := filepath.Join(dir, "oauth_cache.json")

	const N = 30
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = writeOAuthCacheFile(path, &oauthCache{
				AccessToken: "acc_" + string(rune('A'+i%26)),
				ExpiresAt:   int64(1_000_000_000 + i),
			})
		}(i)
	}
	wg.Wait()

	// Final file must be valid JSON (no winner wrote a truncated file).
	got, err := readOAuthCacheFile(path)
	if err != nil {
		t.Fatalf("post-race read failed: %v", err)
	}
	if !strings.HasPrefix(got.AccessToken, "acc_") {
		t.Fatalf("final access_token corrupted: %q", got.AccessToken)
	}

	// And no `.tmp` files leaked into the cache dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

// --- #4: refreshOAuth surfaces 5xx as transient ---

func TestRefreshOAuth_5xxIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	withBaseURL(t, srv.URL)

	_, err := refreshOAuth(t.Context(), "any")
	if err == nil {
		t.Fatal("expected transient error")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error should mention status: %v", err)
	}
	// Critical: callers use errors.Is to decide whether to preserve the
	// on-disk refresh_token. Mismatch here = silent regression of the
	// "don't burn refresh_token on outages" guarantee.
	if !contains(err.Error(), "transient") {
		t.Fatalf("expected wrapped errOAuthTransient, got %v", err)
	}
}

func TestRefreshOAuth_4xxIsNotTransient(t *testing.T) {
	// 400 invalid_grant must NOT be classified as transient — the
	// refresh_token really is dead, and treating it as transient would
	// loop forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"used"}`))
	}))
	t.Cleanup(srv.Close)
	withBaseURL(t, srv.URL)

	_, err := refreshOAuth(t.Context(), "any")
	if err == nil {
		t.Fatal("expected error")
	}
	if contains(err.Error(), "transient") {
		t.Fatalf("4xx must not be classified transient: %v", err)
	}
}

func TestRefreshOAuth_NetworkErrorIsTransient(t *testing.T) {
	// Closed server → connection refused. Network-level failures during
	// refresh are by definition transient.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	withBaseURL(t, url)

	_, err := refreshOAuth(t.Context(), "any")
	if err == nil {
		t.Fatal("expected error from closed server")
	}
	if !contains(err.Error(), "transient") {
		t.Fatalf("network error should be transient: %v", err)
	}
}

// --- #10: ExpiresAt=0 round-trip ---

func TestWriteOAuthCache_OmitsZeroExpiresAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth_cache.json")
	if err := writeOAuthCacheFile(path, &oauthCache{
		AccessToken: "a",
		ExpiresAt:   0, // unknown; must NOT be written as 0
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"expires_at"`) {
		t.Fatalf("expires_at=0 should be omitted from JSON; got: %s", raw)
	}

	// And on re-read it stays 0 (which tryCacheFile then treats as
	// "no expiry recorded" — the conservative behavior preserved here).
	c, err := readOAuthCacheFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExpiresAt != 0 {
		t.Fatalf("expected ExpiresAt=0, got %d", c.ExpiresAt)
	}
}

// --- #7: CLI2API_OAUTH_TIMEOUT honored ---

func TestOAuthHTTPTimeout_Default(t *testing.T) {
	t.Setenv("CLI2API_OAUTH_TIMEOUT", "")
	if got := oauthHTTPTimeout(); got != defaultOAuthHTTPTimeout {
		t.Fatalf("default mismatch: got %v want %v", got, defaultOAuthHTTPTimeout)
	}
}

func TestOAuthHTTPTimeout_EnvDuration(t *testing.T) {
	t.Setenv("CLI2API_OAUTH_TIMEOUT", "3s")
	if got := oauthHTTPTimeout(); got != 3*time.Second {
		t.Fatalf("got %v want 3s", got)
	}
}

func TestOAuthHTTPTimeout_EnvBareSeconds(t *testing.T) {
	t.Setenv("CLI2API_OAUTH_TIMEOUT", "20")
	if got := oauthHTTPTimeout(); got != 20*time.Second {
		t.Fatalf("got %v want 20s", got)
	}
}

func TestOAuthHTTPTimeout_InvalidFallsBack(t *testing.T) {
	t.Setenv("CLI2API_OAUTH_TIMEOUT", "not-a-duration")
	if got := oauthHTTPTimeout(); got != defaultOAuthHTTPTimeout {
		t.Fatalf("invalid input should fall back; got %v", got)
	}
}

// --- #8: muk- disk cache fast-path ---

func TestDiscoverToken_CachedMukSkipsExchange(t *testing.T) {
	// Verify that a cache with cli2api_muk_key set returns it directly
	// without any HTTP call. Server should record zero hits.
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)
	t.Setenv("CLI2API_TOKEN_CACHE", "")

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token":    "eyJsomestale",
		"refresh_token":   "ory_rt_unused",
		"cli2api_muk_key": "muk-cached0000000000000000000000000000000000000000000000000000000000",
		// expires_at deliberately omitted to exercise the "no expiry" path
	})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "muk-cached") {
		t.Fatalf("expected cached muk-, got %q", tok)
	}
	if !strings.Contains(src, "cached muk-") {
		t.Fatalf("source should mark cached path, got %q", src)
	}
	if srv.cliTokenCalls != 0 || srv.refreshCalls != 0 {
		t.Fatalf("expected zero HTTP work; got exchange=%d refresh=%d",
			srv.cliTokenCalls, srv.refreshCalls)
	}
}

func TestDiscoverToken_ExchangeWritesMukBackToCache(t *testing.T) {
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)
	t.Setenv("CLI2API_TOKEN_CACHE", "")

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token": srv.expectJWT, // matches the fake server's allowlist
	})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != srv.muk {
		t.Fatalf("expected exchanged muk-, got %q", tok)
	}

	// Cache must now have cli2api_muk_key persisted.
	round, _ := os.ReadFile(cachePath)
	var got map[string]any
	_ = json.Unmarshal(round, &got)
	if got["cli2api_muk_key"] != srv.muk {
		t.Fatalf("muk- not persisted to cache: %v", got)
	}

	// And second call short-circuits.
	srv.cliTokenCalls = 0
	if _, _, err := DiscoverToken(); err != nil {
		t.Fatal(err)
	}
	if srv.cliTokenCalls != 0 {
		t.Fatalf("expected fast-path on second call, got %d exchanges", srv.cliTokenCalls)
	}
}

func TestDiscoverToken_RefreshClearsCachedMuk(t *testing.T) {
	// A refresh implies the JWT changed (could be a different account
	// after a re-login). The cached muk- might be wrong, so clear it
	// and re-exchange.
	srv := newFakeOAuthServer(t)
	srv.newAccess = srv.expectJWT // refresh hands back a JWT the exchange accepts
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)
	t.Setenv("CLI2API_TOKEN_CACHE", "")

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token":    "eyJoldexpired",
		"refresh_token":   srv.expectRefreshToken,
		"expires_at":      1,
		"cli2api_muk_key": "muk-staleeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok == "muk-staleeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" {
		t.Fatal("must not serve stale muk- after refresh")
	}
	if tok != srv.muk {
		t.Fatalf("expected re-exchanged muk-, got %q", tok)
	}
}

// --- #2: flock contention ---

func TestTryCacheFile_FlockSerializesConcurrentReaders(t *testing.T) {
	// With flock, only one process at a time can be inside the
	// read-refresh-write window. We assert this by counting how many
	// refresh calls hit the server when N workers race: with flock,
	// only ONE refresh fires (the first acquires the lock, the rest
	// see the now-rotated tokens and skip refresh).
	var refreshes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			refreshes.Add(1)
			// Slow down so contention is real.
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "eyJfresh",
				"refresh_token": "ory_rt_new",
				"expires_in":    3600,
			})
		case "/api/platform/users/cli-token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "SUCCESS",
				"data": map[string]any{"key": "muk-flock0000000000000000000000000000000000000000000000000000000000"},
			})
		}
	}))
	t.Cleanup(srv.Close)
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)
	t.Setenv("CLI2API_TOKEN_CACHE", "")

	dir := filepath.Join(home, ".config", "mulerun")
	_ = os.MkdirAll(dir, 0o700)
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token":  "eyJold",
		"refresh_token": "ory_rt_initial",
		"expires_at":    1,
	})
	_ = os.WriteFile(cachePath, payload, 0o600)

	const N = 4
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _, _ = DiscoverToken()
		}()
	}
	wg.Wait()

	// Without flock, all 4 goroutines would race the refresh and 1-4
	// would succeed (the rest get invalid_grant). With flock, exactly
	// one refresh runs and the rest see the cached muk- written by it.
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("expected 1 refresh under flock, got %d (race not prevented)", got)
	}
}

// --- helpers ---

func contains(s, substr string) bool { return strings.Contains(s, substr) }
