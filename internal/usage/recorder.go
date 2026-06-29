package usage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Recorder ingests Records on a buffered channel and writes them to a
// Store in the background. Send is non-blocking — when the channel is
// full, the record is dropped and a counter increments. Operators see
// the drop rate in the periodic "usage backpressure" log.
type Recorder struct {
	store Store
	log   *slog.Logger
	in    chan *Record
	done  chan struct{}

	dropped       int64 // updated by Send, read by drainer at shutdown
	captureMaxLen int64 // max bytes of response body to buffer for token parsing
}

// NewRecorder spins up the background writer. bufSize controls how many
// records can pile up before drops start (default 1024 if 0).
func NewRecorder(store Store, log *slog.Logger, bufSize int) *Recorder {
	if bufSize <= 0 {
		bufSize = 1024
	}
	r := &Recorder{
		store:         store,
		log:           log,
		in:            make(chan *Record, bufSize),
		done:          make(chan struct{}),
		captureMaxLen: 256 << 10, // 256 KB — enough for a long chat response
	}
	go r.run()
	return r
}

func (r *Recorder) run() {
	defer close(r.done)
	for rec := range r.in {
		// One Insert per record. libsql is fast enough for typical
		// chat QPS that batching isn't justified yet; revisit if a
		// load test shows it. Use a short ctx to avoid the writer
		// blocking forever on a hung backend.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := r.store.Insert(ctx, rec); err != nil {
			r.log.Warn("usage insert failed", "err", err, "endpoint", rec.Endpoint, "model", rec.Model)
		}
		cancel()
	}
}

// Send enqueues a record. Non-blocking — drops on full buffer.
func (r *Recorder) Send(rec *Record) {
	if r == nil || r.in == nil {
		return
	}
	select {
	case r.in <- rec:
	default:
		r.dropped++
		// Don't log per-drop; the recorder is on the hot path and a
		// burst could flood logs.
	}
}

// Close stops accepting new records, waits for the in-flight queue to
// drain, then closes the store.
func (r *Recorder) Close() error {
	close(r.in)
	<-r.done
	if r.dropped > 0 {
		r.log.Warn("usage recorder dropped records due to backpressure", "dropped", r.dropped)
	}
	return r.store.Close()
}

// Store exposes the underlying store for the query handler.
func (r *Recorder) Store() Store { return r.store }

// Middleware wraps a handler so each completed request emits a Record.
// Captures status + bytes_out via a ResponseWriter wrap. If the
// response Content-Type is application/json, the body is buffered up
// to captureMaxLen bytes and parsed for `model` + `usage.*_tokens`.
//
// Streaming responses (Content-Type text/event-stream) bypass body
// capture entirely — they pass through with zero overhead beyond status
// and byte counting.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		cw := newCapturingWriter(w, r.captureMaxLen)
		next.ServeHTTP(cw, req)

		rec := &Record{
			Timestamp:  start,
			Endpoint:   req.URL.Path,
			Status:     cw.status,
			DurationMs: time.Since(start).Milliseconds(),
			BytesOut:   cw.bytesWritten,
			RequestID:  middleware.GetReqID(req.Context()),
			APIKeyHash: hashAPIKey(req),
		}
		if cw.captureJSON && cw.buf.Len() > 0 {
			parseOpenAIJSON(cw.buf.Bytes(), rec)
		}
		r.Send(rec)
	})
}

// hashAPIKey returns the first 12 hex chars of SHA-256(inbound key).
// Enough for attribution; not enough to recover the key. Empty when no
// inbound auth (cli2api in no-auth mode).
func hashAPIKey(req *http.Request) string {
	var key string
	if h := req.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		key = strings.TrimSpace(h[len("bearer "):])
	} else if h := req.Header.Get("x-api-key"); h != "" {
		key = strings.TrimSpace(h)
	}
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6]) // 12 hex chars
}

// parseOpenAIJSON populates rec.Model and rec.{Prompt,Completion}Tokens
// from an OpenAI-shaped response body if present.
//
// Handles both the Chat Completions shape:
//
//	{ "model": "...", "usage": { "prompt_tokens": N, "completion_tokens": N } }
//
// and the Responses API shape:
//
//	{ "model": "...", "usage": { "input_tokens": N, "output_tokens": N } }
//
// Silently no-ops on malformed JSON / missing fields — that's exactly
// when we don't want to spam logs.
func parseOpenAIJSON(body []byte, rec *Record) {
	var p struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return
	}
	if p.Model != "" {
		rec.Model = p.Model
	}
	if p.Usage.PromptTokens > 0 {
		rec.PromptTokens = p.Usage.PromptTokens
	} else if p.Usage.InputTokens > 0 {
		rec.PromptTokens = p.Usage.InputTokens
	}
	if p.Usage.CompletionTokens > 0 {
		rec.CompletionTokens = p.Usage.CompletionTokens
	} else if p.Usage.OutputTokens > 0 {
		rec.CompletionTokens = p.Usage.OutputTokens
	}
}
