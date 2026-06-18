package handler

import (
	"bytes"
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

// maxChatBody caps inbound chat request bodies. Aligned with the global
// chi RequestSize middleware (64 MB) so a request rejected here is
// rejected for the same reason elsewhere, not silently truncated.
const maxChatBody = 64 << 20

// chatPeekHead is how many head bytes we sniff before deciding whether to
// fully buffer. Vendor-prefixed model IDs always contain '/'; if we don't
// see one in the head, the request is almost certainly a legacy chat call
// and we can keep streaming it through without ever buffering the full
// body. The cost paid by vendor-prefixed requests is a single contiguous
// read of the rest of the body, which we'd have to do anyway.
const chatPeekHead = 4096

// Chat returns the OpenAI-shaped /v1/chat/completions proxy.
//
// Routing surfaces:
//   - Plain model name → /v1/chat/completions (legacy chat surface).
//   - "vendor/foo" model where vendor ∈ registry.ChatVendorPaths →
//     /vendors/{vendor}/v1/chat/completions; the prefix is stripped from
//     the model field before forwarding (the upstream registry on the
//     vendor surface uses bare names).
//
// Streaming preserved on the legacy path: we peek at most chatPeekHead
// bytes to detect a vendor prefix; if absent, the unread tail flows
// through as a stream — pre-diff behavior. Only vendor-prefixed
// requests pay the full-buffer + parse + re-marshal cost.
func Chat(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bound the inbound body. MaxBytesReader fails the next Read with
		// *http.MaxBytesError once the cap is crossed — unlike io.LimitReader
		// which silently truncates (the original review finding).
		r.Body = http.MaxBytesReader(w, r.Body, maxChatBody)

		upstream, body, err := planChatForward(r)
		if err != nil {
			writeChatError(w, err)
			return
		}
		// `body == nil` means "stream the original body through" — see the
		// peek-then-stream branch in planChatForward.
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
	})
}

// Messages returns the Anthropic-shaped /v1/messages transparent proxy.
//
// Anthropic vendor routing (e.g. "anthropic/claude-opus-4-7" hitting a
// /vendors/anthropic/v1/messages surface) is NOT implemented — that
// upstream URL hasn't been verified, and routing speculatively would
// cause silent 404s. Document it as a known gap; revisit if mulerun
// surfaces a confirmed Messages code-plane.
func Messages(d Deps) http.Handler {
	return proxyJSON(d.Client, "/v1/messages", mulerun.AuthAPIKey)
}

// planChatForward decides where to send the request and what body to send.
//
// Return contract (single shape, no more tri-state):
//   - upstream: the path mulerun.Client.Proxy will POST to.
//   - body:     bytes to forward. nil = "stream the original r.Body
//               unchanged" (legacy fast path). non-nil = "use this exact
//               slice" (vendor rewrite, or body was fully buffered for
//               other reasons).
//   - err:      403/413/400-grade caller-visible error.
//
// Errors that aren't the caller's fault (transient network read errors
// mid-upload) fall through to the upstream so it returns its own
// transport-level signal — we don't want to fail open by forwarding a
// truncated body.
func planChatForward(r *http.Request) (upstream string, body []byte, err error) {
	const legacy = "/v1/chat/completions"

	// Compressed bodies can't be parsed without decompression. Skip
	// rewriting and let the legacy proxy forward as-is — vendor routing
	// requires uncompressed JSON. The body still goes through as a
	// stream.
	if r.Header.Get("Content-Encoding") != "" {
		return legacy, nil, nil
	}

	// Peek the head. If the head contains no '/' character, the request
	// can't possibly carry a vendor-prefixed model, so we stream the
	// whole body through the legacy path without buffering.
	head, peekErr := readPeek(r.Body, chatPeekHead)
	if peekErr != nil {
		return "", nil, peekErr
	}
	if !bytes.ContainsRune(head, '/') {
		// Re-prepend the peeked head before the unread tail so Proxy() sees
		// the full body in order.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), r.Body))
		return legacy, nil, nil
	}

	// '/' could mean a vendor prefix, but it could also be a URL inside
	// the messages array. Read the rest of the body, parse just the model
	// field (preserving other fields byte-exact via json.RawMessage so
	// int64s aren't downgraded to float64).
	rest, restErr := io.ReadAll(r.Body)
	if restErr != nil {
		return "", nil, restErr
	}
	full := append(head, rest...)

	upstream, rewritten, ok := rewriteVendorModel(full)
	if !ok {
		// Couldn't rewrite (no prefix match, malformed JSON, etc.) — forward
		// the original bytes verbatim on the legacy path. We can't stream
		// here because we already consumed the body for the peek probe.
		return legacy, full, nil
	}
	return upstream, rewritten, nil
}

// readPeek reads up to n bytes from r without consuming any more than that.
// io.EOF (clean) and io.ErrUnexpectedEOF (body smaller than n, returned by
// io.ReadFull when the underlying Reader EOFs mid-fill) both fold into a
// successful read of whatever arrived — short bodies are normal.
//
// Distinct connection errors (read tcp..., use of closed network connection,
// etc.) are surfaced so the handler can return 400 instead of silently
// forwarding a partial body.
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

// rewriteVendorModel parses just enough of a JSON chat-completion body to
// inspect the `model` field. If the model has a known vendor prefix the
// prefix is stripped and the new body is returned along with the vendor's
// upstream path. All other fields round-trip byte-exact through
// json.RawMessage — no int64 → float64 precision loss, no canonical
// re-encoding of nested arrays.
func rewriteVendorModel(body []byte) (upstream string, newBody []byte, ok bool) {
	if len(body) == 0 {
		return "", nil, false
	}

	// json.RawMessage preserves the original bytes of every value we don't
	// touch. Only `model` is decoded as a string.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, false
	}
	modelRaw, present := raw["model"]
	if !present {
		return "", nil, false
	}
	var model string
	if err := json.Unmarshal(modelRaw, &model); err != nil {
		return "", nil, false
	}
	idx := strings.IndexByte(model, '/')
	if idx <= 0 || idx == len(model)-1 {
		return "", nil, false
	}
	vendor, suffix := model[:idx], model[idx+1:]
	path, hit := registry.ChatVendorPaths[vendor]
	if !hit {
		return "", nil, false
	}

	// Re-encode the stripped model name as JSON so any odd chars escape.
	encoded, err := json.Marshal(suffix)
	if err != nil {
		return "", nil, false
	}
	raw["model"] = encoded

	out, err := json.Marshal(raw)
	if err != nil {
		return "", nil, false
	}
	return path, out, true
}

// writeChatError maps body-read failures to the right HTTP status. The
// chat surface uses OpenAI's error envelope so SDK clients surface a
// readable message instead of an opaque 500.
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
