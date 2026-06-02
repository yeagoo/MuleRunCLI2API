package handler

import (
	"net/http"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Responses returns the OpenAI-shaped /v1/responses transparent proxy.
// Note the upstream is /vendors/openai/v1/responses — NOT /v1/responses.
func Responses(d Deps) http.Handler {
	return proxyJSON(d.Client, "/vendors/openai/v1/responses", mulerun.AuthBearer)
}
