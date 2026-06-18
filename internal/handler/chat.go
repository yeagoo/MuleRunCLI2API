package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Chat returns the OpenAI-shaped /v1/chat/completions transparent proxy.
//
// Two upstream surfaces are reachable depending on the requested model:
//
//   * Plain model name ("gpt-5", "deepseek-v4-flash", "claude-sonnet-4-6")
//     → /v1/chat/completions — the legacy chat surface most accounts have.
//
//   * Vendor-prefixed model ("openai/gpt-5.5", "openai/gpt-5.3-codex", …)
//     → /vendors/openai/v1/chat/completions — the "code-plane" surface
//     `mulerun code` (opencode) hits for the newer GPT-5.x family. The
//     prefix is STRIPPED from the model field before forwarding because
//     the path already encodes the vendor.
//
// Falling back to the legacy path keeps every existing client working
// unchanged; only requests that explicitly opt in via a vendor prefix
// reach the new surface.
func Chat(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, body := rewriteChatRequest(r)
		if body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		d.Client.Proxy(r.Context(), w, r, upstream, mulerun.AuthBearer)
	})
}

// Messages returns the Anthropic-shaped /v1/messages transparent proxy.
func Messages(d Deps) http.Handler {
	return proxyJSON(d.Client, "/v1/messages", mulerun.AuthAPIKey)
}

// vendorChatPaths maps a model-name prefix (the part before "/") to the
// upstream HTTP path that serves it. Add new vendors here as we
// reverse-engineer them.
var vendorChatPaths = map[string]string{
	"openai": "/vendors/openai/v1/chat/completions",
}

// rewriteChatRequest inspects the request body for a vendor-prefixed model
// and, if found, returns (upstream-path, rewritten-body). When the request
// uses no prefix (or an unknown prefix) the legacy path is returned and
// newBody is nil — the caller forwards the original body without
// re-buffering.
//
// We only re-read the body when the prefix scan succeeds; in the common
// case (no prefix) we don't allocate. The chat handler reads the body
// fully either way because vendor routing requires JSON parsing, so this
// adds no extra round-trip — only a parse and a re-marshal on hits.
func rewriteChatRequest(r *http.Request) (upstream string, newBody []byte) {
	const legacy = "/v1/chat/completions"

	// Cap the read at 32 MB — the chi RequestSize middleware already
	// caps the global per-request body, but this defends the JSON parser
	// against a missing middleware in tests / future refactors.
	const maxChatBody = 32 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBody))
	_ = r.Body.Close()
	if err != nil {
		// We've already consumed the body; hand it back as-is. Worst case
		// the upstream rejects and we surface its error.
		return legacy, body
	}

	// Empty body → no model to inspect; let the upstream return its own
	// validation error.
	if len(body) == 0 {
		return legacy, nil
	}

	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return legacy, body
	}
	model, _ := msg["model"].(string)
	idx := strings.IndexByte(model, '/')
	if idx <= 0 || idx == len(model)-1 {
		return legacy, body
	}
	vendor, suffix := model[:idx], model[idx+1:]
	path, ok := vendorChatPaths[vendor]
	if !ok {
		return legacy, body
	}

	msg["model"] = suffix
	out, err := json.Marshal(msg)
	if err != nil {
		return legacy, body
	}
	return path, out
}
