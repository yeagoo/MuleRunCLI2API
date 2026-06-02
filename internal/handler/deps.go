package handler

import (
	"net/http"
	"time"

	"github.com/openmule/cli2api/internal/mulerun"
)

// Deps carries everything handlers need: an upstream client plus polling tunings.
type Deps struct {
	Client          *mulerun.Client
	ImageTimeout    time.Duration
	PollInterval    time.Duration
	PollMaxInterval time.Duration
	JobRetention    time.Duration // 0 = never expire
}

func proxyJSON(c *mulerun.Client, upstream string, scheme mulerun.AuthScheme) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.Proxy(r.Context(), w, r, upstream, scheme)
	}
}
