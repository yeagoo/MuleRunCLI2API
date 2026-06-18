package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOAuthServer wires up the two endpoints we reverse-engineered:
//
//	POST /api/platform/users/cli-token   (JWT bearer → muk-)
//	POST /oauth2/token                   (refresh_token → new triple)
//
// The handlers are intentionally strict — they assert request shape so a
// future bug that drops a header or switches form encoding fails loudly.
type fakeOAuthServer struct {
	*httptest.Server
	expectJWT          string
	muk                string
	expectRefreshToken string
	newAccess          string
	newRefresh         string
	expiresIn          int

	cliTokenCalls int
	refreshCalls  int

	failExchange bool
	failRefresh  bool
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	f := &fakeOAuthServer{
		expectJWT:          "eyJtest.jwt.value",
		muk:                "muk-fake0000000000000000000000000000000000000000000000000000000000",
		expectRefreshToken: "ory_rt_fake_refresh",
		newAccess:          "eyJnewaccess",
		newRefresh:         "ory_rt_rotated",
		expiresIn:          3600,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/platform/users/cli-token", func(w http.ResponseWriter, r *http.Request) {
		f.cliTokenCalls++
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+f.expectJWT {
			http.Error(w, "bad auth: "+auth, http.StatusUnauthorized)
			return
		}
		if f.failExchange {
			http.Error(w, `{"code":"BAD","message":"forced"}`, http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "SUCCESS",
			"data": map[string]any{"key": f.muk},
		})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		f.refreshCalls++
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			http.Error(w, "bad content-type: "+ct, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "grant_type=refresh_token") {
			http.Error(w, "missing grant_type", http.StatusBadRequest)
			return
		}
		if !strings.Contains(string(body), "refresh_token="+f.expectRefreshToken) {
			http.Error(w, "wrong refresh_token", http.StatusBadRequest)
			return
		}
		if f.failRefresh {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token expired"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  f.newAccess,
			"refresh_token": f.newRefresh,
			"expires_in":    f.expiresIn,
		})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// withBaseURL points the package-level testBaseURL at the fake server for
// the duration of the test, then restores it.
func withBaseURL(t *testing.T, url string) {
	t.Helper()
	old := testBaseURL
	testBaseURL = url
	t.Cleanup(func() { testBaseURL = old })
}

func TestExchangeJWTForMUK_Success(t *testing.T) {
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)

	muk, err := exchangeJWTForMUK(t.Context(), srv.expectJWT)
	if err != nil {
		t.Fatal(err)
	}
	if muk != srv.muk {
		t.Fatalf("got muk %q, want %q", muk, srv.muk)
	}
	if srv.cliTokenCalls != 1 {
		t.Fatalf("expected 1 call, got %d", srv.cliTokenCalls)
	}
}

func TestExchangeJWTForMUK_HTTPError(t *testing.T) {
	srv := newFakeOAuthServer(t)
	srv.failExchange = true
	withBaseURL(t, srv.URL)

	_, err := exchangeJWTForMUK(t.Context(), srv.expectJWT)
	if err == nil {
		t.Fatal("expected error from 403")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("error should mention HTTP 403: %v", err)
	}
}

func TestExchangeJWTForMUK_EmptyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SUCCESS code but no key → reject (defends against silent breakage).
		_, _ = w.Write([]byte(`{"code":"SUCCESS","data":{}}`))
	}))
	t.Cleanup(srv.Close)
	withBaseURL(t, srv.URL)

	_, err := exchangeJWTForMUK(t.Context(), "anything")
	if err == nil {
		t.Fatal("expected error on empty key")
	}
}

func TestExchangeJWTForMUK_WrongPrefix(t *testing.T) {
	// Defense-in-depth: if the API ever returns a non-muk- key, we don't
	// want to silently hand a useless string to the gateway.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"SUCCESS","data":{"key":"sk-leaked"}}`))
	}))
	t.Cleanup(srv.Close)
	withBaseURL(t, srv.URL)

	_, err := exchangeJWTForMUK(t.Context(), "anything")
	if err == nil || !strings.Contains(err.Error(), "unexpected prefix") {
		t.Fatalf("expected prefix-rejection error, got %v", err)
	}
}

func TestRefreshOAuth_Success(t *testing.T) {
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)

	tok, err := refreshOAuth(t.Context(), srv.expectRefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != srv.newAccess || tok.RefreshToken != srv.newRefresh || tok.ExpiresIn != srv.expiresIn {
		t.Fatalf("unexpected token triple: %+v", tok)
	}
}

func TestRefreshOAuth_ServerError(t *testing.T) {
	srv := newFakeOAuthServer(t)
	srv.failRefresh = true
	withBaseURL(t, srv.URL)

	_, err := refreshOAuth(t.Context(), srv.expectRefreshToken)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected invalid_grant error, got %v", err)
	}
}

func TestDiscoverToken_JWTEnvAutoExchanges(t *testing.T) {
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)
	t.Setenv("MULERUN_TOKEN", srv.expectJWT)
	t.Setenv("HOME", t.TempDir())

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != srv.muk {
		t.Fatalf("expected exchange to return muk-, got %q", tok)
	}
	if src != "env:MULERUN_TOKEN" {
		t.Fatalf("source should still point at env var, got %q", src)
	}
}

func TestDiscoverToken_MukEnvNoExchange(t *testing.T) {
	// muk- env should be returned verbatim — NO HTTP call to the exchange
	// endpoint. Verifies that cold-starts with a baked-in muk- key don't
	// hit the network.
	srv := newFakeOAuthServer(t)
	withBaseURL(t, srv.URL)
	t.Setenv("MULERUN_TOKEN", "muk-cached_no_exchange_needed")
	t.Setenv("HOME", t.TempDir())

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "muk-cached_no_exchange_needed" {
		t.Fatalf("muk- token shouldn't be exchanged, got %q", tok)
	}
	if srv.cliTokenCalls != 0 {
		t.Fatalf("expected zero exchange calls, got %d", srv.cliTokenCalls)
	}
}

func TestDiscoverToken_ExchangeFailureFallsBackToRaw(t *testing.T) {
	// Graceful degradation: a broken exchange MUST not crash startup. We
	// return the raw JWT (the upstream will then 401 with a clear message).
	srv := newFakeOAuthServer(t)
	srv.failExchange = true
	withBaseURL(t, srv.URL)
	t.Setenv("MULERUN_TOKEN", srv.expectJWT)
	t.Setenv("HOME", t.TempDir())

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != srv.expectJWT {
		t.Fatalf("expected raw JWT fallback, got %q", tok)
	}
}

func TestDiscoverToken_RefreshesExpiredCacheAndExchanges(t *testing.T) {
	srv := newFakeOAuthServer(t)
	// Make the JWT the refresh endpoint hands back match the one the
	// exchange endpoint expects (end-to-end pipeline).
	srv.newAccess = srv.expectJWT
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token":  "eyJoldexpired",
		"refresh_token": srv.expectRefreshToken,
		"expires_at":    1, // long since expired
		"base_url":      "https://mulerun.com",
	})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != srv.muk {
		t.Fatalf("expected exchanged muk-, got %q", tok)
	}
	if !strings.Contains(src, "oauth_cache.json") {
		t.Fatalf("source should reference oauth_cache.json, got %q", src)
	}
	if srv.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh call, got %d", srv.refreshCalls)
	}
	if srv.cliTokenCalls != 1 {
		t.Fatalf("expected 1 exchange call, got %d", srv.cliTokenCalls)
	}

	// Cache must have been written back with the rotated triple AND
	// preserve the unknown base_url field.
	rewritten, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got["access_token"] != srv.newAccess {
		t.Fatalf("access_token not rotated: %v", got["access_token"])
	}
	if got["refresh_token"] != srv.newRefresh {
		t.Fatalf("refresh_token not rotated: %v", got["refresh_token"])
	}
	if got["base_url"] != "https://mulerun.com" {
		t.Fatalf("base_url was dropped during write-back: %v", got["base_url"])
	}
	// expires_at should be ~now + 3600
	expAt, _ := got["expires_at"].(float64)
	if expAt < float64(time.Now().Unix()+3000) {
		t.Fatalf("expires_at not bumped forward: %v", expAt)
	}
	// Mode preserved (0600)
	stat, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := stat.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected mode 0600 after writeback, got %o", mode)
	}
}

func TestDiscoverToken_RefreshFailureSkipsCache(t *testing.T) {
	// When refresh fails (e.g. refresh_token was already used), the cache
	// should be treated as missing and we fall through to other paths.
	srv := newFakeOAuthServer(t)
	srv.failRefresh = true
	withBaseURL(t, srv.URL)

	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(dir, "oauth_cache.json")
	payload, _ := json.Marshal(map[string]any{
		"access_token":  "eyJoldexpired",
		"refresh_token": srv.expectRefreshToken,
		"expires_at":    1,
	})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	// Place a fallback legacy file so we can confirm fall-through.
	legacyDir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "auth.json"),
		[]byte(`{"token":"muk-legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "muk-legacy" {
		t.Fatalf("expected fall-through to legacy auth.json, got %q from %s", tok, src)
	}
}

func TestOAuthCacheRoundtripPreservesExtraFields(t *testing.T) {
	// Unit-level coverage for write-back: any keys we don't model must
	// survive a read → write cycle. Without this, future versions of
	// mulerun-cli that add fields would lose them on first refresh.
	path := filepath.Join(t.TempDir(), "oauth_cache.json")
	in, _ := json.Marshal(map[string]any{
		"access_token":     "a",
		"refresh_token":    "r",
		"expires_at":       12345.0,
		"base_url":         "https://mulerun.com",
		"future_field_one": "preserve me",
		"future_field_two": []any{1.0, 2.0, 3.0},
	})
	if err := os.WriteFile(path, in, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := readOAuthCacheFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "a" || c.RefreshToken != "r" || c.ExpiresAt != 12345 || c.BaseURL != "https://mulerun.com" {
		t.Fatalf("typed fields wrong: %+v", c)
	}
	if c.Extra["future_field_one"] != "preserve me" {
		t.Fatalf("future_field_one not captured: %v", c.Extra)
	}

	if err := writeOAuthCacheFile(path, c); err != nil {
		t.Fatal(err)
	}
	round, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(round, &got); err != nil {
		t.Fatal(err)
	}
	if got["future_field_one"] != "preserve me" {
		t.Fatalf("future_field_one lost on write: %v", got)
	}
	arr, _ := got["future_field_two"].([]any)
	if len(arr) != 3 {
		t.Fatalf("future_field_two corrupted: %v", got["future_field_two"])
	}
}
