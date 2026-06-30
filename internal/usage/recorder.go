package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// requestPeekMax caps how many head bytes we read off req.Body to extract
// the model field. 64 KB is enough for any reasonable JSON head — the
// `model` field sits near the top of OpenAI-shaped bodies. Larger bodies
// (e.g. multimodal chat with embedded base64 images) get the remaining
// tail re-prepended via io.MultiReader so downstream handlers see the
// full stream untouched.
const requestPeekMax = 64 << 10

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
//
// Model detection runs in two stages, in priority order:
//
//  1. Request-side peek: for JSON requests, we read up to requestPeekMax
//     bytes off req.Body, parse the `model` field, then restore the body
//     so downstream handlers see the same stream. This is the only way
//     to know the model for image/video/audio endpoints — their MuleRun
//     responses don't echo `model`. Without this, /v1/usage's per-model
//     breakdown was useless for non-chat surfaces (claude review of v0.3.0
//     surfaced this).
//
//  2. Response-side parse: for JSON responses (chat / responses surfaces),
//     parse the `model` and `usage.*_tokens` from the body. The model
//     value here OVERRIDES the request-side value — upstream's echo is
//     the canonical name (and for vendor-routed chat we strip the prefix
//     before forwarding, so the request-side value would be the alias,
//     not the upstream-recognised name).
//
// Streaming responses (Content-Type text/event-stream) bypass response-
// body capture entirely; the request-side model still works.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()

		reqModel := ""
		if isJSONContentType(req.Header.Get("Content-Type")) {
			reqModel = peekRequestModel(req)
		}

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
			Model:      reqModel,
		}
		if cw.captureJSON && cw.buf.Len() > 0 {
			// May override Model with the canonical upstream name.
			parseOpenAIJSON(cw.buf.Bytes(), rec)
		}
		r.Send(rec)
	})
}

// isJSONContentType returns true for application/json (with or without
// charset suffix). Used to gate the request-body peek so we don't waste
// I/O on multipart uploads (image edits) or other non-JSON shapes.
func isJSONContentType(ct string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json")
}

// peekRequestModel reads up to requestPeekMax bytes off req.Body, parses
// the `model` field, and restores req.Body so downstream handlers see
// the full stream. Returns "" on any failure — this is best-effort
// observability, never blocking.
//
// Body restoration strategy:
//   - If the body fits in the peek → re-wrap as a bytes.Reader of the
//     captured bytes (the original ReadCloser's tail is empty).
//   - If the body extends past the peek → chain via io.MultiReader so
//     the tail flows through untouched.
func peekRequestModel(req *http.Request) string {
	if req.Body == nil {
		return ""
	}
	head, err := io.ReadAll(io.LimitReader(req.Body, int64(requestPeekMax)+1))
	if err != nil {
		// Restore whatever we got so the handler can still surface its
		// own error (instead of seeing a half-drained body that confuses
		// downstream JSON parsers).
		req.Body = io.NopCloser(bytes.NewReader(head))
		return ""
	}
	if int64(len(head)) > int64(requestPeekMax) {
		// Body exceeds peek — preserve the tail.
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), req.Body))
	} else {
		req.Body = io.NopCloser(bytes.NewReader(head))
	}

	return extractModelField(head)
}

// extractModelField walks the head incrementally with json.Decoder.Token()
// so we can find `"model": "..."` even when the head is truncated
// mid-body (a 60 KB embedded base64 prompt after the model field would
// otherwise make a strict Unmarshal fail). Stops as soon as model is
// found OR exhausts the head.
//
// Limitation: when the model field sits AFTER a field whose value
// extends past the peek (large messages array deserialized first), the
// walker can't reach it. That's an explicit trade-off — alternatives
// would require either a tolerant tokenizer that resyncs after errors
// (complex) or buffering the entire request body in middleware
// (regression on memory). Most clients place `model` first.
func extractModelField(head []byte) string {
	dec := json.NewDecoder(bytes.NewReader(head))

	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return ""
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ""
		}
		key, ok := keyTok.(string)
		if !ok {
			return ""
		}
		if key == "model" {
			valTok, err := dec.Token()
			if err != nil {
				return ""
			}
			s, _ := valTok.(string)
			return strings.TrimSpace(s)
		}
		// Skip the value (object/array/primitive). If it extends past
		// the head we'll error here — fine, we just couldn't find model
		// in this request and return "" cleanly.
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return ""
		}
	}
	return ""
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
