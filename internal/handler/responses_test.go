package handler

import (
	"bytes"
	"encoding/json"
	"testing"
)

// #2 — Responses handler strips vendor prefix before forwarding

func TestStripVendorPrefix_OpenAI(t *testing.T) {
	// codex review found that /v1/responses was a raw proxy. A client
	// sending "openai/gpt-5.3-codex" (the model we advertise in
	// /v1/models for the Responses surface) would hit
	// /vendors/openai/v1/responses with the prefix intact, and the
	// upstream would reject it as an unknown model since its own
	// registry uses bare names.
	in := `{"model":"openai/gpt-5.3-codex","input":"hi"}`
	out, ok := stripVendorPrefixForVendor([]byte(in), "openai")
	if !ok {
		t.Fatal("expected strip to fire on openai/-prefixed Responses model")
	}
	if !bytes.Contains(out, []byte(`"model":"gpt-5.3-codex"`)) {
		t.Fatalf("prefix not stripped: %s", out)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["input"] != "hi" {
		t.Fatalf("input field dropped: %v", got["input"])
	}
}

func TestStripVendorPrefix_NoPrefixPassesThrough(t *testing.T) {
	// Non-prefixed models on Responses (pre-codex-review default path)
	// must continue to forward unchanged.
	_, ok := stripVendorPrefixForVendor([]byte(`{"model":"gpt-5","input":"hi"}`), "openai")
	if ok {
		t.Fatal("non-prefixed model should not strip")
	}
}

func TestStripVendorPrefix_WrongVendorRejected(t *testing.T) {
	// If someone sends `google/gemini-3` to the Responses surface
	// (openai-only upstream), we must NOT silently strip the google/
	// prefix and forward "gemini-3" as if it were an openai model. The
	// upstream's "unknown model" error is the right signal.
	_, ok := stripVendorPrefixForVendor([]byte(`{"model":"google/gemini-3","input":"hi"}`), "openai")
	if ok {
		t.Fatal("foreign vendor on openai surface must not be stripped")
	}
}

func TestStripVendorPrefix_PreservesNumericPrecision(t *testing.T) {
	// Same json.RawMessage roundtrip as chat — int64 > 2^53 must survive.
	in := `{"model":"openai/gpt-5.5","input":"hi","seed":9007199254740993}`
	out, ok := stripVendorPrefixForVendor([]byte(in), "openai")
	if !ok {
		t.Fatal("expected strip")
	}
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("seed precision corrupted: %s", out)
	}
}

func TestStripVendorPrefix_EmptyAndMalformed(t *testing.T) {
	if _, ok := stripVendorPrefixForVendor(nil, "openai"); ok {
		t.Fatal("nil body should not strip")
	}
	if _, ok := stripVendorPrefixForVendor([]byte("not json"), "openai"); ok {
		t.Fatal("malformed JSON should not strip")
	}
	if _, ok := stripVendorPrefixForVendor([]byte(`{"input":"hi"}`), "openai"); ok {
		t.Fatal("missing model field should not strip")
	}
	if _, ok := stripVendorPrefixForVendor([]byte(`{"model":"openai/"}`), "openai"); ok {
		t.Fatal("trailing-slash model should not strip")
	}
}
