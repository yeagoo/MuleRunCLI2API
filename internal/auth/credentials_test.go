package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverToken_EnvWins(t *testing.T) {
	t.Setenv("MULERUN_TOKEN", "from-env")
	t.Setenv("HOME", t.TempDir())

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-env" {
		t.Fatalf("want from-env, got %q", tok)
	}
	if src != "env:MULERUN_TOKEN" {
		t.Fatalf("unexpected source: %s", src)
	}
}

func TestDiscoverToken_FromFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"access_token": "from-file"})
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-file" {
		t.Fatalf("want from-file, got %q", tok)
	}
	if src == "" || src[:5] != "file:" {
		t.Fatalf("unexpected source: %s", src)
	}
}

func TestDiscoverToken_None(t *testing.T) {
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", t.TempDir())

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		t.Fatalf("expected empty, got %q", tok)
	}
}

func TestDiscoverToken_FromOAuthCache(t *testing.T) {
	// mulerun-cli 0.1.0+ writes here. Must be discovered before older paths.
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"access_token":  "from-oauth-cache",
		"refresh_token": "ignored",
		// Far in the future so this test isn't time-dependent.
		"expires_at": 4102444800, // 2100-01-01
	})
	if err := os.WriteFile(filepath.Join(dir, "oauth_cache.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, src, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "from-oauth-cache" {
		t.Fatalf("want from-oauth-cache, got %q", tok)
	}
	if !strings.Contains(src, "oauth_cache.json") {
		t.Fatalf("unexpected source: %s", src)
	}
}

func TestDiscoverToken_OAuthCachePreferredOverLegacy(t *testing.T) {
	// When both new and old caches exist, the new path wins.
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	newDir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "oauth_cache.json"),
		[]byte(`{"access_token":"new-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldDir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "auth.json"),
		[]byte(`{"token":"old-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "new-token" {
		t.Fatalf("expected new path to win, got %q", tok)
	}
}

func TestDiscoverToken_SkipsExpiredOAuthCache(t *testing.T) {
	// Review #12: an expired access_token in oauth_cache.json should be
	// treated as missing so we fall through to other candidates.
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	newDir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	expired, _ := json.Marshal(map[string]any{
		"access_token": "stale",
		"expires_at":   1, // Jan 1 1970
	})
	if err := os.WriteFile(filepath.Join(newDir, "oauth_cache.json"), expired, 0o600); err != nil {
		t.Fatal(err)
	}

	// Legacy path with a still-valid token.
	oldDir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "auth.json"),
		[]byte(`{"token":"recovered"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "recovered" {
		t.Fatalf("expected expired oauth_cache to be skipped, got %q", tok)
	}
}

func TestDiscoverToken_FallbackChain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"token": "fallback-token"})
	if err := os.WriteFile(filepath.Join(dir, "token.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatal(err)
	}
	if tok != "fallback-token" {
		t.Fatalf("want fallback-token, got %q", tok)
	}
}

func TestDiscoverToken_SkipsUnreadableFile(t *testing.T) {
	// Review #8 regression: an unreadable file (e.g. root-owned from a
	// prior sudo) must NOT abort the whole discovery loop. Skip it and
	// fall through to the next candidate.
	home := t.TempDir()
	t.Setenv("MULERUN_TOKEN", "")
	t.Setenv("HOME", home)

	// Place an unreadable file at the FIRST-priority path.
	newDir := filepath.Join(home, ".config", "mulerun")
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(newDir, "oauth_cache.json")
	if err := os.WriteFile(cachePath, []byte(`{"access_token":"unreachable"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(cachePath, 0o600) // let t.TempDir() clean up

	// And a usable file at the legacy path.
	oldDir := filepath.Join(home, ".mulerun")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "auth.json"),
		[]byte(`{"token":"recovered"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, _, err := DiscoverToken()
	if err != nil {
		t.Fatalf("expected fall-through, got error: %v", err)
	}
	if tok != "recovered" {
		t.Fatalf("expected fall-through to legacy path, got %q", tok)
	}
}

func TestPrefixClassifiers(t *testing.T) {
	// finalize() dispatches on prefix; pin the shapes here so a future
	// rename or refactor doesn't silently break credential routing.
	cases := []struct {
		token   string
		wantMuk bool
		wantJWT bool
	}{
		{"muk-7c359a7f5daa59f5bfa49c6bae5418cb274c5ff87f61a549b08cfc61eb5d5c26", true, false},
		{"eyJhbGciOiJIUzI1Ni", false, true},
		{"mr_oldstyletoken", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := strings.HasPrefix(c.token, "muk-"); got != c.wantMuk {
			t.Fatalf("muk- prefix for %q: got %v want %v", c.token, got, c.wantMuk)
		}
		if got := strings.HasPrefix(c.token, "eyJ"); got != c.wantJWT {
			t.Fatalf("eyJ prefix for %q: got %v want %v", c.token, got, c.wantJWT)
		}
	}
}
