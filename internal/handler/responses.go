package handler

import (
	"bytes"
	"io"
	"net/http"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Responses returns the OpenAI-shaped /v1/responses transparent proxy.
// Note the upstream is /vendors/openai/v1/responses — NOT /v1/responses.
//
// Vendor-prefix handling: /v1/models advertises "openai/gpt-5.5" etc.,
// and some clients (codex, the OpenAI Python SDK) faithfully send those
// IDs to the Responses surface too. The upstream path already encodes
// the vendor, so we MUST strip the "openai/" prefix from the model field
// before forwarding — otherwise the upstream sees "openai/gpt-5.5" and
// rejects with "unknown model".
//
// Same body-handling rules as Chat apply: MaxBytesReader → 413; ReadAll
// errors → 400; Content-Encoding requests skip rewriting (compressed
// bodies aren't parsed). The upstream is fixed, so dispatch is simpler
// than planChatForward — only the prefix-strip is needed.
func Responses(d Deps) http.Handler {
	const upstream = "/vendors/openai/v1/responses"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxChatBody)

		if r.Header.Get("Content-Encoding") != "" {
			d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
			return
		}

		head, err := readPeek(r.Body, chatPeekHead)
		if err != nil {
			writeChatError(w, err)
			return
		}
		var full []byte
		if len(head) < chatPeekHead {
			full = head
		} else {
			rest, rerr := io.ReadAll(r.Body)
			if rerr != nil {
				writeChatError(w, rerr)
				return
			}
			full = append(head, rest...)
		}

		// The Responses surface is openai-only. Strip a leading "openai/"
		// from the model field if present; anything else (no prefix,
		// non-openai prefix, malformed body) passes through verbatim and
		// the upstream surfaces its own error.
		body := full
		if stripped, ok := stripVendorPrefixForVendor(full, "openai"); ok {
			body = stripped
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
	})
}
