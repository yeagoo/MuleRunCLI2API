package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newReq(body string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions",
		io.NopCloser(strings.NewReader(body)))
	return r
}

func TestRewriteChatRequest_VendorPrefixRouted(t *testing.T) {
	r := newReq(`{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`)
	path, body := rewriteChatRequest(r)

	if path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("expected vendor path, got %q", path)
	}
	if body == nil {
		t.Fatal("expected rewritten body")
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.5" {
		t.Fatalf("model prefix not stripped: %v", got["model"])
	}
	// messages must round-trip unchanged
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages corrupted: %v", got["messages"])
	}
}

func TestRewriteChatRequest_NoPrefixUsesLegacy(t *testing.T) {
	r := newReq(`{"model":"deepseek-v4-flash","messages":[]}`)
	path, body := rewriteChatRequest(r)

	if path != "/v1/chat/completions" {
		t.Fatalf("expected legacy path, got %q", path)
	}
	if body == nil {
		t.Fatal("body should be re-buffered for forwarding (we consumed it)")
	}
	// And the body must not have been rewritten.
	if !bytes.Contains(body, []byte(`"model":"deepseek-v4-flash"`)) {
		t.Fatalf("body unexpectedly rewritten: %s", body)
	}
}

func TestRewriteChatRequest_UnknownPrefixPassesThrough(t *testing.T) {
	// "google/" is a known opencode prefix but cli2api hasn't mapped it
	// (Google API is non-OpenAI shape). Treat unknown prefixes as legacy
	// so the upstream returns its own clear "not supported" rather than
	// us guessing wrong paths.
	r := newReq(`{"model":"google/gemini-3-pro","messages":[]}`)
	path, _ := rewriteChatRequest(r)
	if path != "/v1/chat/completions" {
		t.Fatalf("unknown prefix should fall back to legacy, got %q", path)
	}
}

func TestRewriteChatRequest_TrailingSlashIsNotAPrefix(t *testing.T) {
	// "openai/" with nothing after isn't a real model name — falling back
	// to legacy lets the upstream return its own validation error
	// instead of us forwarding an empty model field.
	r := newReq(`{"model":"openai/","messages":[]}`)
	path, _ := rewriteChatRequest(r)
	if path != "/v1/chat/completions" {
		t.Fatalf("trailing-slash model should not match vendor route, got %q", path)
	}
}

func TestRewriteChatRequest_InvalidJSONFallsThrough(t *testing.T) {
	// We must not crash on bad JSON; upstream will reject and surface the
	// error to the client.
	r := newReq(`not json at all`)
	path, body := rewriteChatRequest(r)
	if path != "/v1/chat/completions" {
		t.Fatalf("malformed JSON should pass through, got %q", path)
	}
	if string(body) != "not json at all" {
		t.Fatalf("body should be preserved verbatim, got %q", body)
	}
}

func TestRewriteChatRequest_EmptyBody(t *testing.T) {
	r := newReq("")
	path, body := rewriteChatRequest(r)
	if path != "/v1/chat/completions" {
		t.Fatalf("empty body should default to legacy, got %q", path)
	}
	if body != nil {
		t.Fatalf("empty body shouldn't allocate, got %d bytes", len(body))
	}
}

func TestRewriteChatRequest_PreservesNonModelFields(t *testing.T) {
	// Rewriting must not drop other fields (temperature, stream, tools…).
	in := `{"model":"openai/gpt-5.5","temperature":0.7,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	r := newReq(in)
	_, body := rewriteChatRequest(r)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["temperature"] != 0.7 {
		t.Fatalf("temperature dropped: %v", got["temperature"])
	}
	if got["stream"] != true {
		t.Fatalf("stream flag dropped: %v", got["stream"])
	}
}
