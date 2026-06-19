package handler

import (
	"net/http"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Responses returns the OpenAI-shaped /v1/responses transparent proxy.
// Upstream is /vendors/openai/v1/responses (NOT /v1/responses).
//
// Vendor-prefix handling: /v1/models advertises "openai/gpt-5.5" etc.,
// and some clients (codex, the OpenAI Python SDK) faithfully send those
// IDs to the Responses surface too. The upstream path already encodes
// the vendor, so we strip the "openai/" prefix before forwarding —
// otherwise the upstream sees "openai/gpt-5.5" and rejects with
// "unknown model". The strip respects registry.ChatVendorPaths so chat
// and responses surfaces agree on which vendors are valid.
//
// Body handling (MaxBytesReader cap, gzip decode, peek+ReadAll, error
// envelope, TransferEncoding fixup) is shared with Chat via
// bufferForRewrite + setForwardBody.
func Responses(d Deps) http.Handler {
	const upstream = "/vendors/openai/v1/responses"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := bufferForRewrite(w, r)
		if err != nil {
			writeChatError(w, err)
			return
		}
		if body != nil {
			if stripped, ok := stripVendorPrefixForVendor(body, "openai"); ok {
				setForwardBody(r, stripped)
			}
		}
		d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
	})
}
