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
