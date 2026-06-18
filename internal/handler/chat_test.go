package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// rewriteVendorModel — pure function tests

func TestRewriteVendorModel_OpenAIRouted(t *testing.T) {
	in := `{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	path, out, ok := rewriteVendorModel([]byte(in))
	if !ok {
		t.Fatal("expected rewrite hit")
	}
	if path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("wrong path: %q", path)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["model"] != "gpt-5.5" {
		t.Fatalf("model not stripped: %v", got["model"])
	}
}

func TestRewriteVendorModel_NoPrefix(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"gpt-5","messages":[]}`))
	if ok {
		t.Fatal("plain model should not rewrite")
	}
}

func TestRewriteVendorModel_UnknownVendor(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"google/gemini-3"}`))
	if ok {
		t.Fatal("unknown vendor should not rewrite")
	}
}

func TestRewriteVendorModel_TrailingSlash(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"openai/"}`))
	if ok {
		t.Fatal("trailing-slash model should not rewrite")
	}
}

func TestRewriteVendorModel_MalformedJSON(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte("not json"))
	if ok {
		t.Fatal("malformed JSON should not pretend to rewrite")
	}
}

// #2 — Numeric precision preserved via json.RawMessage

func TestRewriteVendorModel_PreservesLargeInt(t *testing.T) {
	// seed > 2^53 would lose precision through float64 round-trip. The
	// new RawMessage path leaves non-model fields byte-exact.
	const bigSeed = 9007199254740993
	in := `{"model":"openai/gpt-5.5","seed":9007199254740993,"messages":[]}`
	_, out, ok := rewriteVendorModel([]byte(in))
	if !ok {
		t.Fatal("expected rewrite")
	}
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("large int corrupted; output: %s", out)
	}
	// And the value survives a decoder set to UseNumber.
	d := json.NewDecoder(bytes.NewReader(out))
	d.UseNumber()
	var got map[string]any
	if err := d.Decode(&got); err != nil {
		t.Fatal(err)
	}
	n, _ := got["seed"].(json.Number).Int64()
	if n != bigSeed {
		t.Fatalf("seed corrupted: want %d got %d", bigSeed, n)
	}
}

func TestRewriteVendorModel_PreservesNestedStructure(t *testing.T) {
	in := `{"model":"openai/gpt-5.5","temperature":0.7,"stream":true,"tools":[{"type":"function","function":{"name":"fn","parameters":{"id":12345678901234567}}}]}`
	_, out, ok := rewriteVendorModel([]byte(in))
	if !ok {
		t.Fatal("expected rewrite")
	}
	// Big nested int still byte-exact.
	if !bytes.Contains(out, []byte("12345678901234567")) {
		t.Fatalf("nested int corrupted: %s", out)
	}
}

// planChatForward — handler integration tests

func newPlanReq(body string, headers map[string]string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions",
		io.NopCloser(strings.NewReader(body)))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestPlanChatForward_LegacyStreams(t *testing.T) {
	// Plain model with no '/' anywhere in the head → fast path returns
	// body=nil signaling "stream original r.Body verbatim".
	r := newPlanReq(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`, nil)
	path, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("wrong path: %q", path)
	}
	if body != nil {
		t.Fatal("legacy fast path should signal streaming via body=nil")
	}
	// And r.Body must contain the full original payload (peek prepended).
	out, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(out), "gpt-5") {
		t.Fatalf("peeked head not re-prepended: %s", out)
	}
}

func TestPlanChatForward_VendorRoutes(t *testing.T) {
	r := newPlanReq(`{"model":"openai/gpt-5.5","messages":[]}`, nil)
	path, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("wrong path: %q", path)
	}
	if body == nil {
		t.Fatal("vendor path must return rewritten body bytes")
	}
	if !bytes.Contains(body, []byte(`"model":"gpt-5.5"`)) {
		t.Fatalf("prefix not stripped: %s", body)
	}
}

func TestPlanChatForward_ContentEncodingSkipsRewrite(t *testing.T) {
	// #4 fix: gzipped vendor-prefix requests previously silently
	// mis-routed to legacy (failed JSON parse). Now we explicitly bail
	// on any Content-Encoding so the legacy stream path runs.
	r := newPlanReq(`{"model":"openai/gpt-5.5"}`, map[string]string{"Content-Encoding": "gzip"})
	path, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("compressed body must skip vendor rewrite, got %q", path)
	}
	if body != nil {
		t.Fatal("compressed body should stream, not buffer")
	}
}

func TestPlanChatForward_EmptyBodyDoesNotPanic(t *testing.T) {
	// #9: empty body used to leave r.Body closed-but-not-reassigned.
	// Now we re-prepend the peek (which is also empty) and stream.
	r := newPlanReq("", nil)
	path, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("empty body should default to legacy, got %q", path)
	}
	if body != nil {
		t.Fatal("empty body should stream")
	}
	// r.Body must still be readable.
	if _, err := io.ReadAll(r.Body); err != nil {
		t.Fatalf("r.Body unreadable after planChatForward: %v", err)
	}
}

func TestPlanChatForward_BigBodyBufferedForCorrectness(t *testing.T) {
	// Bodies > peek must be fully buffered — the model field could sit
	// anywhere in the body, and gating routing on a `/` in the head was
	// the codex-review bug (model after messages → misroute to legacy).
	// Trade-off: legacy chat loses streaming for >4KB bodies, vendor
	// routing gains correctness regardless of field order.
	big := strings.Repeat("x", 10_000)
	in := `{"model":"gpt-5","messages":[{"role":"user","content":"` + big + `"}]}`
	r := newPlanReq(in, nil)
	_, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		t.Fatal("oversize legacy body should now be buffered, not streamed")
	}
	if len(body) != len(in) {
		t.Fatalf("buffered body length mismatch: got %d want %d", len(body), len(in))
	}
}

func TestPlanChatForward_VendorPrefixAfterLargeMessages(t *testing.T) {
	// Codex review #1: a body where messages serialize BEFORE model and
	// the head (4KB) contains no '/' would previously fall to legacy,
	// silently misrouting a valid vendor-prefixed model. Verify the
	// vendor path is picked correctly regardless of field order.
	big := strings.Repeat("x", 8000) // > chatPeekHead
	in := `{"messages":[{"role":"user","content":"` + big + `"}],"model":"openai/gpt-5.5"}`
	r := newPlanReq(in, nil)
	path, body, err := planChatForward(r)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("vendor model deep in body should still route to code-plane, got %q", path)
	}
	if body == nil || !bytes.Contains(body, []byte(`"model":"gpt-5.5"`)) {
		t.Fatal("vendor prefix not stripped from deep-body request")
	}
}

// #1 — MaxBytesReader integration test through the real handler

func TestChat_413OnOversizedBody(t *testing.T) {
	// The handler wraps r.Body in http.MaxBytesReader(w, _, maxChatBody)
	// before calling planChatForward. We construct a body just over the
	// limit and assert 413 — previously this was silently truncated.

	// Use a tiny limit for the test (smaller than maxChatBody) by
	// stub-wrapping via a synthetic handler:
	const limit = 1024
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		_, _, err := planChatForward(r)
		if err == nil {
			t.Error("expected MaxBytesError")
			return
		}
		writeChatError(w, err)
	})

	body := strings.Repeat("a", limit+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openai/gpt-5.5","junk":"`+body+`"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "request_too_large") {
		t.Fatalf("expected request_too_large error type, got %s", w.Body.String())
	}
}

// #3 — Body read error surfaces as 400, not silent partial forward

type erroringReader struct{ reads atomic.Int32 }

func (e *erroringReader) Read(p []byte) (int, error) {
	e.reads.Add(1)
	// Use io.ErrClosedPipe — a real connection-error class that's
	// distinct from io.ErrUnexpectedEOF (which is the legitimate "body
	// smaller than buf" signal).
	return 0, io.ErrClosedPipe
}

func TestPlanChatForward_ReadErrorSurfaces(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		io.NopCloser(&erroringReader{}))
	_, _, err := planChatForward(r)
	if err == nil {
		t.Fatal("expected error from failing Read")
	}
	if err != io.ErrClosedPipe {
		t.Fatalf("want ErrClosedPipe, got %v", err)
	}
}
