package mulerun

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// Proxy forwards the inbound request body and selected headers to the given
// upstream path, streaming the response back to the client verbatim. Used for
// chat completions and messages (both supporting SSE).
func (c *Client) Proxy(ctx context.Context, w http.ResponseWriter, r *http.Request, upstreamPath string, scheme AuthScheme) {
	resp, err := c.Do(ctx, r.Method, upstreamPath, r.Body, scheme, sanitizeInbound(r.Header))
	if err != nil {
		http.Error(w, "upstream request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		if shouldDropResponseHeader(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			return
		}
	}
}

// sanitizeInbound drops hop-by-hop and auth headers from the inbound request
// before we forward upstream. We always inject our own auth header.
func sanitizeInbound(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vv := range h {
		if shouldDropRequestHeader(k) {
			continue
		}
		for _, v := range vv {
			out.Add(k, v)
		}
	}
	return out
}

var dropRequestHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"connection":        {},
	"keep-alive":        {},
	"proxy-connection":  {},
	"transfer-encoding": {},
	"upgrade":           {},
	"te":                {},
	"trailer":           {},
	"authorization":     {},
	"x-api-key":         {},
	"api-key":           {},
}

var dropResponseHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"content-length":      {},
}

func shouldDropRequestHeader(name string) bool {
	_, ok := dropRequestHeaders[strings.ToLower(name)]
	return ok
}

func shouldDropResponseHeader(name string) bool {
	_, ok := dropResponseHeaders[strings.ToLower(name)]
	return ok
}

