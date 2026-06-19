package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openmule/cli2api/internal/mulerun"
	"github.com/openmule/cli2api/internal/registry"
	"github.com/openmule/cli2api/pkg/apierr"
)

// MaxRequestBody is the per-request inbound body cap shared by the chat,
// responses, and chi global RequestSize middleware. One literal in one
// place so they can never drift.
const MaxRequestBody = 64 << 20

// chatPeekHead is how many head bytes we sniff before deciding whether to
// fully buffer. Vendor-prefix detection requires parsing the model field,
// which can sit anywhere in the JSON; if the peek captures the whole body
// we parse it directly, otherwise we buffer fully (correctness over
// streaming for >peek bodies — codex round 1 caught the alternative).
const chatPeekHead = 4096

// Chat returns the OpenAI-shaped /v1/chat/completions proxy.
//
// Routing surfaces:
//   - Plain model name → /v1/chat/completions.
//   - "vendor/foo" model where vendor ∈ registry.ChatVendorPaths →
//     /vendors/{vendor}/v1/chat/completions, prefix stripped before
//     forward (the upstream registry on the vendor surface uses bare
//     names).
//
// Streaming preserved on the legacy path when the body fits in the peek
// AND no vendor prefix is detected. Larger bodies are fully buffered —
// the model field could sit anywhere, and silently misrouting a
// vendor-prefixed request is a real bug.
func Chat(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := bufferForRewrite(w, r)
		if err != nil {
			writeChatError(w, err)
			return
		}

		upstream := "/v1/chat/completions"
		if body != nil {
			if path, rewritten, ok := rewriteVendorModel(body); ok {
				upstream = path
				setForwardBody(r, rewritten)
			}
			// body!=nil but no rewrite: r.Body was already restored by
			// bufferForRewrite, fall through.
		}
		// body==nil (unknown encoding bypass): r.Body untouched, forward.
		d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
	})
}

// Messages returns the Anthropic-shaped /v1/messages transparent proxy.
//
// Anthropic vendor routing (e.g. "anthropic/claude-opus-4-7" hitting a
// /vendors/anthropic/v1/messages surface) is NOT implemented — that
// upstream URL hasn't been verified, and routing speculatively would
// cause silent 404s.
func Messages(d Deps) http.Handler {
	return proxyJSON(d.Client, "/v1/messages", mulerun.AuthAPIKey)
}

// bufferForRewrite is the body-handling primitive shared by Chat and
// Responses.
//
// Return contract:
//   - body == nil → caller should NOT inspect or rewrite; forward r.Body
//     as-is (currently only the unknown-Content-Encoding bypass).
//   - body != nil → caller may pass these bytes to a rewriter. r.Body
//     has been restored to a fresh reader over the same bytes, so a
//     caller that decides NOT to rewrite can call Proxy() with r
//     untouched.
//   - err → caller-visible 413/400-grade error.
//
// Compression: Content-Encoding: gzip is transparently decoded so the
// downstream rewrite can inspect the body, and the encoding header is
// stripped before forwarding (upstream sees plain JSON). Unknown
// encodings bypass to streaming with the original (encoded) body —
// vendor routing doesn't apply for those.
func bufferForRewrite(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBody)

	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
		// no-op
	case "gzip":
		// Decompress in-memory; drop the header so the upstream doesn't
		// expect a still-gzipped body. The decompressed body flows
		// through the normal peek+parse path so vendor routing works
		// the same as for uncompressed requests (claude review #1).
		gz, gerr := gzip.NewReader(r.Body)
		if gerr != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", gerr)
		}
		// Cap the decompressed size too — gzip bombs can multiply
		// MaxRequestBody by 1000x.
		r.Body = io.NopCloser(io.LimitReader(gz, MaxRequestBody+1))
		r.Header.Del("Content-Encoding")
		r.ContentLength = -1
	default:
		// Unknown encoding (br, zstd, deflate). Forward as-is — the
		// upstream will return its own error if it can't decode. Vendor
		// routing skipped because we can't see the model field.
		return nil, nil
	}

	head, peekErr := readPeek(r.Body, chatPeekHead)
	if peekErr != nil {
		return nil, peekErr
	}

	if len(head) < chatPeekHead {
		// Body fully captured by the peek. Restore r.Body so a caller
		// that doesn't rewrite can stream the same bytes through.
		r.Body = io.NopCloser(bytes.NewReader(head))
		return head, nil
	}

	// Body extends past the peek. Pre-size to avoid O(log N)
	// reallocations during append (claude review #8).
	hint := int(r.ContentLength)
	if hint <= 0 || hint > MaxRequestBody {
		hint = MaxRequestBody
	}
	full := make([]byte, 0, hint)
	full = append(full, head...)

	rest, restErr := io.ReadAll(r.Body)
	if restErr != nil {
		return nil, restErr
	}
	full = append(full, rest...)
	r.Body = io.NopCloser(bytes.NewReader(full))
	return full, nil
}

// setForwardBody installs a rewritten body on r and aligns the framing
// headers so net/http builds a clean upstream request.
//
// Clears TransferEncoding because Content-Length AND TE:chunked are
// mutually exclusive per RFC 7230 §3.3.3, and we're now sending a known
// fixed length (claude review #3).
func setForwardBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.TransferEncoding = nil
}

// rewriteVendorModel parses just enough of a JSON chat-completion body to
// inspect the `model` field. If the model has a known vendor prefix the
// prefix is stripped and the new body is returned along with the vendor's
// upstream path. All other fields round-trip byte-exact through
// json.RawMessage — no int64 → float64 precision loss.
//
// Performance note: this does a full Unmarshal+Marshal of the body to
// change one field. For typical chat bodies (<100KB) the overhead is
// microseconds; for multi-MB multimodal bodies it's measurable. A byte-
// splice optimization is possible (see codex round 1 / claude round 4
// findings) but adds JSON-tokenizer complexity for a low-frequency path.
func rewriteVendorModel(body []byte) (upstream string, newBody []byte, ok bool) {
	if len(body) == 0 {
		return "", nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, false
	}
	vendor, suffix, ok := extractVendorSuffix(raw)
	if !ok {
		return "", nil, false
	}
	path, hit := registry.ChatVendorPaths[vendor]
	if !hit {
		return "", nil, false
	}
	out, ok := writeBackModel(raw, suffix)
	if !ok {
		return "", nil, false
	}
	return path, out, true
}

// stripVendorPrefixForVendor rewrites the body's `model` field by
// stripping the `<vendor>/` prefix iff it matches the given vendor AND
// the vendor is recognized by the chat registry. Used by the Responses
// handler where the upstream path already encodes a single vendor.
//
// The registry gate is intentional: it keeps the chat and responses
// surfaces from drifting on which vendors are recognized — a vendor
// removed from registry.ChatVendorPaths automatically stops being
// stripped here too.
func stripVendorPrefixForVendor(body []byte, vendor string) (newBody []byte, ok bool) {
	if len(body) == 0 || vendor == "" {
		return nil, false
	}
	if _, registered := registry.ChatVendorPaths[vendor]; !registered {
		return nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false
	}
	v, suffix, ok := extractVendorSuffix(raw)
	if !ok || v != vendor {
		return nil, false
	}
	return writeBackModel(raw, suffix)
}

func extractVendorSuffix(raw map[string]json.RawMessage) (vendor, suffix string, ok bool) {
	modelRaw, present := raw["model"]
	if !present {
		return "", "", false
	}
	var model string
	if err := json.Unmarshal(modelRaw, &model); err != nil {
		return "", "", false
	}
	idx := strings.IndexByte(model, '/')
	if idx <= 0 || idx == len(model)-1 {
		return "", "", false
	}
	return model[:idx], model[idx+1:], true
}

func writeBackModel(raw map[string]json.RawMessage, suffix string) ([]byte, bool) {
	encoded, err := json.Marshal(suffix)
	if err != nil {
		return nil, false
	}
	raw["model"] = encoded
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	return out, true
}

// readPeek reads up to n bytes from r without consuming more than that.
// io.EOF (clean) and io.ErrUnexpectedEOF (body smaller than n) both fold
// into a successful short read.
func readPeek(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	got, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return buf[:got], nil
	}
	if err != nil {
		return nil, err
	}
	return buf[:got], nil
}

// writeChatError maps body-read failures to the right HTTP status. Used
// by every OpenAI-shape proxy surface (chat, responses) — the name is
// historical; treat as writeProxyError.
func writeChatError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		apierr.Write(w, apierr.StyleOpenAI, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body too large (max %d bytes)", maxErr.Limit),
			"request_too_large")
		return
	}
	apierr.Write(w, apierr.StyleOpenAI, http.StatusBadRequest,
		"failed to read request body: "+err.Error(),
		"invalid_request")
}
