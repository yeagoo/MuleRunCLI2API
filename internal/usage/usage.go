// Package usage records per-request metadata for every upstream call
// cli2api proxies, so operators can answer "what did I spend my MuleRun
// credits on, when, on which model" without depending on MuleRun's web
// dashboard (which doesn't expose a per-call API).
//
// What's captured:
//   - timestamp, endpoint (e.g. /v1/chat/completions), upstream status
//   - duration (handler wall-clock)
//   - bytes written to the client (response size)
//   - best-effort: model + prompt/completion tokens, parsed from the
//     response body when Content-Type is application/json (i.e.
//     non-streaming chat/responses). Streaming responses (SSE) and
//     non-JSON endpoints (image/video binary URLs are still JSON
//     envelopes, so they work; speech audio bytes are skipped) get a
//     record with zero tokens.
//
// What's NOT captured:
//   - request body content (privacy)
//   - the inbound API key in plaintext (only a hashed prefix, for
//     multi-tenant attribution later)
//   - Anthropic /v1/messages token usage — its envelope shape differs;
//     could be added with one more parser.
package usage

import (
	"context"
	"time"
)

// Record is one upstream call.
type Record struct {
	Timestamp        time.Time
	Endpoint         string // /v1/chat/completions, /v1/responses, /v1/images/generations, …
	Model            string // gpt-5.5, deepseek-v4-flash, gpt-image-2, … ("" when not detected)
	Status           int    // upstream HTTP status code (200, 502, 413…)
	DurationMs       int64
	BytesOut         int64
	PromptTokens     int
	CompletionTokens int
	RequestID        string
	APIKeyHash       string // first 12 hex of SHA-256(inbound key), or "" when no inbound auth
}

// AggregateRequest is a usage query.
type AggregateRequest struct {
	From    time.Time
	To      time.Time
	GroupBy string // "model" | "endpoint" | "status" | "day" | "hour"
	// Optional filters (empty == no filter).
	Model    string
	Endpoint string
}

// AggregateRow is one bucket of aggregated counts.
type AggregateRow struct {
	Bucket           string `json:"bucket"` // model OR endpoint OR "200"/"502" OR "2026-06-29" OR "2026-06-29T15:00"
	Calls            int64  `json:"calls"`
	Errors           int64  `json:"errors"` // status / 100 != 2
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	BytesOut         int64  `json:"bytes_out"`
}

// Totals summarises an entire query result.
type Totals struct {
	Calls            int64 `json:"calls"`
	Errors           int64 `json:"errors"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	BytesOut         int64 `json:"bytes_out"`
}

// Store is the persistence interface. Implementations: Memory (default)
// and LibSQL (CLI2API_USAGE_DSN).
type Store interface {
	// Insert appends a record. Implementations may buffer and batch.
	Insert(ctx context.Context, r *Record) error
	// Aggregate returns rows grouped per req.GroupBy with totals.
	Aggregate(ctx context.Context, req AggregateRequest) ([]AggregateRow, Totals, error)
	// DeleteOlder drops records strictly older than cutoff. Returns
	// rows removed.
	DeleteOlder(ctx context.Context, cutoff time.Time) (int64, error)
	// Close flushes and releases resources.
	Close() error
}

// ValidGroupBy is the closed set of group_by keys the query layer
// accepts. Keeping it separate from raw SQL prevents an attacker from
// injecting `1=1) UNION …` via a query param.
var ValidGroupBy = map[string]struct{}{
	"model":    {},
	"endpoint": {},
	"status":   {},
	"day":      {},
	"hour":     {},
}
