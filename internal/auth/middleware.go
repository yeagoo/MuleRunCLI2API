package auth

import (
	"net/http"
	"strings"

	"github.com/openmule/cli2api/pkg/apierr"
)

type Style int

const (
	StyleOpenAI Style = iota
	StyleAnthropic
)

func (s Style) toAPIErr() apierr.Style {
	if s == StyleAnthropic {
		return apierr.StyleAnthropic
	}
	return apierr.StyleOpenAI
}

// Middleware enforces a static API-key allow-list against inbound requests.
// allowed=nil disables auth (local-only deployments).
func Middleware(allowed []string, style Style) func(http.Handler) http.Handler {
	set := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		set[k] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(set) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			key := extract(r)
			if key == "" {
				apierr.Write(w, style.toAPIErr(), http.StatusUnauthorized, "missing API key", "unauthorized")
				return
			}
			if _, ok := set[key]; !ok {
				apierr.Write(w, style.toAPIErr(), http.StatusUnauthorized, "invalid API key", "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extract(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-API-Key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("Authorization")); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
		return v
	}
	return ""
}
