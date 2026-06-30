package usage

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRecorder_CapturesJSONResponseTokensAndModel(t *testing.T) {
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	// Handler emits an OpenAI-shaped chat response.
	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x","model":"openai/gpt-5.5","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":17,"total_tokens":59}}`))
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openai/gpt-5.5","messages":[]}`))
	r.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	// Drain async writer.
	waitFor(t, store, 1)

	rows, totals, _ := store.Aggregate(context.Background(), AggregateRequest{
		From:    time.Now().Add(-time.Minute),
		To:      time.Now().Add(time.Minute),
		GroupBy: "model",
	})
	if totals.Calls != 1 {
		t.Fatalf("want 1 call, got %d", totals.Calls)
	}
	if len(rows) != 1 || rows[0].Bucket != "openai/gpt-5.5" {
		t.Fatalf("model not captured from JSON body: %+v", rows)
	}
	if rows[0].PromptTokens != 42 || rows[0].CompletionTokens != 17 {
		t.Fatalf("tokens not captured: %+v", rows[0])
	}
}

func TestRecorder_CapturesResponsesAPIInputOutputTokens(t *testing.T) {
	// The /v1/responses surface uses input_tokens/output_tokens instead
	// of prompt_tokens/completion_tokens; both shapes must be supported.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.5","status":"completed","usage":{"input_tokens":33,"output_tokens":7}}`))
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	waitFor(t, store, 1)

	rows, _, _ := store.Aggregate(context.Background(), AggregateRequest{
		From:    time.Now().Add(-time.Minute),
		To:      time.Now().Add(time.Minute),
		GroupBy: "model",
	})
	if len(rows) != 1 || rows[0].PromptTokens != 33 || rows[0].CompletionTokens != 7 {
		t.Fatalf("Responses-API tokens not mapped: %+v", rows)
	}
}

func TestRecorder_SSEResponseSkipsBodyCapture(t *testing.T) {
	// Streaming responses must NOT be buffered — the middleware should
	// pass writes straight through and record the request with empty
	// model/tokens (we can't see them without parsing SSE chunks).
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"x\":1}\n\n"))
		_, _ = w.Write([]byte("data: {\"x\":2}\n\n"))
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	waitFor(t, store, 1)

	rows, _, _ := store.Aggregate(context.Background(), AggregateRequest{
		From:    time.Now().Add(-time.Minute),
		To:      time.Now().Add(time.Minute),
		GroupBy: "endpoint",
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d: %+v", len(rows), rows)
	}
	// SSE → no model/tokens but bytes counted
	if rows[0].PromptTokens != 0 || rows[0].CompletionTokens != 0 {
		t.Fatalf("SSE should not yield tokens: %+v", rows[0])
	}
	if rows[0].BytesOut == 0 {
		t.Fatalf("bytes_out should still be counted on SSE: %+v", rows[0])
	}
}

func TestRecorder_HashesAPIKeyNotPlaintext(t *testing.T) {
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	r := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	r.Header.Set("Authorization", "Bearer super-secret-muk-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	waitFor(t, store, 1)

	// Read the raw record back.
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.records) != 1 {
		t.Fatalf("want 1 record")
	}
	hash := store.records[0].APIKeyHash
	if hash == "" {
		t.Fatal("api_key_hash should be set when Bearer header present")
	}
	if strings.Contains(hash, "super-secret") {
		t.Fatalf("api_key_hash MUST NOT contain plaintext: %s", hash)
	}
	if len(hash) != 12 {
		t.Fatalf("hash must be 12 hex chars, got %d", len(hash))
	}
}

func TestRecorder_DroppedOnFullBufferDoesNotBlockHandler(t *testing.T) {
	// Send 100 records with a 1-slot buffer and a SLOW store. The
	// handler must return immediately for each request even though
	// inserts pile up; the recorder drops the excess.
	slowStore := &blockingStore{ch: make(chan struct{})}
	rec := NewRecorder(slowStore, slog.New(slog.DiscardHandler), 1)
	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))

	start := time.Now()
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
	}
	dur := time.Since(start)
	if dur > 500*time.Millisecond {
		t.Fatalf("recorder back-pressure leaked into handler latency: %v", dur)
	}
	close(slowStore.ch)
	_ = rec.Close()
	if rec.dropped < 90 {
		t.Fatalf("expected most records to drop, got dropped=%d", rec.dropped)
	}
}

// Round-4 of the usage feature: capture model from REQUEST body so
// image/video/audio (responses without a model field) get attributed.

func TestRecorder_CapturesImageModelFromRequestBody(t *testing.T) {
	// gpt-image-2's response has no `model` field — just
	// {"created":..., "data":[{"url":"..."}]}. Pre-fix the recorder
	// stored model="" and /v1/usage?model=gpt-image-2 returned nothing.
	// Post-fix: model is read from the request body.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	// Handler simulates MuleRun's image response shape (no model echo).
	var seenBody []byte
	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenBody, _ = io.ReadAll(req.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"created":1234,"data":[{"url":"https://x/y.png"}]}`))
	}))

	in := `{"model":"gpt-image-2","prompt":"hi","size":"1024x1024"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(in))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	waitFor(t, store, 1)

	store.mu.RLock()
	got := store.records[0].Model
	store.mu.RUnlock()
	if got != "gpt-image-2" {
		t.Fatalf("model should be captured from request body, got %q", got)
	}
	// Critical: handler must still see the FULL request body.
	if string(seenBody) != in {
		t.Fatalf("body restoration broken; handler saw %q want %q", seenBody, in)
	}
}

func TestRecorder_ResponseModelOverridesRequestModel(t *testing.T) {
	// For chat, request says "openai/gpt-5.5" but cli2api strips the
	// prefix before forwarding so upstream echoes "gpt-5.5". Stored
	// model should be the upstream-canonical name.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.5","usage":{"prompt_tokens":5}}`))
	}))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openai/gpt-5.5","messages":[]}`))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	waitFor(t, store, 1)

	store.mu.RLock()
	got := store.records[0].Model
	store.mu.RUnlock()
	if got != "gpt-5.5" {
		t.Fatalf("response-side model should override request-side; got %q want gpt-5.5", got)
	}
}

func TestRecorder_RequestModelStandsWhenResponseHasNone(t *testing.T) {
	// SSE chat responses don't get parsed for tokens (captureJSON==false
	// for text/event-stream), so the request-side model is the only
	// source. Must persist.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"x\":1}\n\n"))
	}))
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek-v4-flash","stream":true,"messages":[]}`))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	waitFor(t, store, 1)

	store.mu.RLock()
	got := store.records[0].Model
	store.mu.RUnlock()
	if got != "deepseek-v4-flash" {
		t.Fatalf("SSE response should keep request-side model, got %q", got)
	}
}

func TestRecorder_LargeRequestBodyRestoredViaMultiReader(t *testing.T) {
	// Body > requestPeekMax must still be fully readable downstream.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	bigField := strings.Repeat("x", requestPeekMax+5000) // ~69 KB > peek
	in := `{"model":"gpt-image-2","prompt":"` + bigField + `"}`

	var seenLen int
	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		seenLen = len(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(in))
	r.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	waitFor(t, store, 1)

	if seenLen != len(in) {
		t.Fatalf("handler saw %d bytes of body, want %d (MultiReader restore broken)", seenLen, len(in))
	}
	store.mu.RLock()
	got := store.records[0].Model
	store.mu.RUnlock()
	if got != "gpt-image-2" {
		t.Fatalf("model should still parse from head of >peek body, got %q", got)
	}
}

func TestRecorder_NonJSONContentTypeSkipsPeek(t *testing.T) {
	// multipart/form-data (image edits) and other non-JSON requests
	// shouldn't trigger the peek — both for perf (form bodies can be
	// large) and correctness (parsing a multipart as JSON would never
	// work). Handler must see body untouched.
	store := NewMemory()
	rec := NewRecorder(store, slog.New(slog.DiscardHandler), 8)
	defer rec.Close()

	var seenBody []byte
	handler := rec.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seenBody, _ = io.ReadAll(req.Body)
		w.WriteHeader(200)
	}))
	body := "--boundary\r\nContent-Disposition: form-data\r\n\r\nblah\r\n--boundary--"
	r := httptest.NewRequest(http.MethodPost, "/v1/images/edits",
		strings.NewReader(body))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	waitFor(t, store, 1)

	if string(seenBody) != body {
		t.Fatalf("multipart body altered: %q", seenBody)
	}
	store.mu.RLock()
	got := store.records[0].Model
	store.mu.RUnlock()
	if got != "" {
		t.Fatalf("non-JSON should not yield a model; got %q", got)
	}
}

// helpers

func waitFor(t *testing.T, s *Memory, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		got := len(s.records)
		s.mu.RUnlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("async insert never reached %d (memory remained empty)", n)
}

type blockingStore struct {
	ch chan struct{}
}

func (b *blockingStore) Insert(_ context.Context, _ *Record) error {
	<-b.ch
	return nil
}
func (b *blockingStore) Aggregate(_ context.Context, _ AggregateRequest) ([]AggregateRow, Totals, error) {
	return nil, Totals{}, nil
}
func (b *blockingStore) DeleteOlder(_ context.Context, _ time.Time) (int64, error) { return 0, nil }
func (b *blockingStore) Close() error                                              { return nil }

// keep imports stable
var _ = io.Discard
