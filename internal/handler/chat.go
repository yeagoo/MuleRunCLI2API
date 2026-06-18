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
// Return contract:
//   - upstream: the path mulerun.Client.Proxy will POST to.
//   - body:     bytes to forward. nil = "stream r.Body unchanged" (we may
//               have replaced r.Body internally so the caller still gets a
//               coherent reader). non-nil = "use this exact slice".
//   - err:      caller-visible 413/400-grade error.
//
// Correctness vs streaming trade-off: a body that exceeds the peek window
// (chatPeekHead) is ALWAYS fully buffered. We can't safely stream the tail
// without inspecting the whole body — the model field could sit deeper than
// the peek, and a vendor-prefixed model misrouted to legacy is a real bug
// (codex review caught this). Bodies that fit in the peek still stream
// (we hand r.Body back wrapped around the peek bytes).
func planChatForward(r *http.Request) (upstream string, body []byte, err error) {
	const legacy = "/v1/chat/completions"

	// Compressed bodies can't be parsed without decompression. Skip
	// rewriting; legacy proxy forwards as-is. Streams.
	if r.Header.Get("Content-Encoding") != "" {
		return legacy, nil, nil
	}

	head, peekErr := readPeek(r.Body, chatPeekHead)
	if peekErr != nil {
		return "", nil, peekErr
	}

	// Body fully captured by the peek. Try to rewrite; if no vendor
	// prefix, restore r.Body so Proxy() reads the same bytes it would
	// have seen pre-peek.
	if len(head) < chatPeekHead {
		if path, rewritten, ok := rewriteVendorModel(head); ok {
			return path, rewritten, nil
		}
		r.Body = io.NopCloser(bytes.NewReader(head))
		return legacy, nil, nil
	}

	// Body extends past the peek. We have to read it all to know whether
	// the model field carries a vendor prefix (it could sit anywhere in
	// the body, regardless of head content). Streaming the tail would
	// risk misrouting; buffer and parse.
	rest, restErr := io.ReadAll(r.Body)
	if restErr != nil {
		return "", nil, restErr
	}
	full := append(head, rest...)

	if path, rewritten, ok := rewriteVendorModel(full); ok {
		return path, rewritten, nil
	}
	return legacy, full, nil
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
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", nil, false
	}
	vendor, suffix, ok := extractVendorSuffix(raw)
	if !ok {
		return "", nil, false
	}
	path, ok := registry.ChatVendorPaths[vendor]
	if !ok {
		return "", nil, false
	}
	out, ok := writeBackModel(raw, suffix)
	if !ok {
		return "", nil, false
	}
	return path, out, true
}

// stripVendorPrefixForVendor rewrites the body's `model` field by stripping
// a leading `<vendor>/` if (and only if) the prefix matches the given
// vendor. Used by the Responses handler where the upstream path already
// encodes a single vendor — we only want to strip its OWN prefix, not
// re-route a foreign one.
func stripVendorPrefixForVendor(body []byte, vendor string) (newBody []byte, ok bool) {
	if len(body) == 0 || vendor == "" {
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

// extractVendorSuffix pulls the model field out of a parsed JSON map and
// splits it on the first '/'. Returns false unless the model is a
// well-formed "vendor/suffix" pair (both halves non-empty).
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

// writeBackModel replaces the model field in `raw` with `suffix` (encoded
// as JSON for safe escaping) and re-marshals.
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
