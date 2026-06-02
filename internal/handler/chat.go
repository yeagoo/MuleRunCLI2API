package handler

import (
	"net/http"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Chat returns the OpenAI-shaped /v1/chat/completions transparent proxy.
func Chat(d Deps) http.Handler {
	return proxyJSON(d.Client, "/v1/chat/completions", mulerun.AuthBearer)
}

// Messages returns the Anthropic-shaped /v1/messages transparent proxy.
func Messages(d Deps) http.Handler {
	return proxyJSON(d.Client, "/v1/messages", mulerun.AuthAPIKey)
}
