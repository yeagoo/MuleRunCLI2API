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
		"expires_at":    1780410965,
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
