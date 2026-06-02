package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
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
