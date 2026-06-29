package usage

import (
	"bytes"
	"net/http"
	"strings"
)

// capturingWriter wraps http.ResponseWriter to count bytes and capture
// the response status. When the response Content-Type is
// application/json (and only then), it ALSO buffers up to captureMaxLen
// bytes so the recorder can parse model + token usage out of the body.
//
// Streaming responses (text/event-stream) skip the buffer entirely —
// the captureJSON flag is set in WriteHeader based on the
// Content-Type the handler already set, so SSE handlers that flush
// per-chunk are unaffected.
type capturingWriter struct {
	http.ResponseWriter
	status        int
	bytesWritten  int64
	captureJSON   bool
	captureMaxLen int64
	buf           bytes.Buffer
	headerWritten bool
}

func newCapturingWriter(w http.ResponseWriter, max int64) *capturingWriter {
	return &capturingWriter{
		ResponseWriter: w,
		captureMaxLen:  max,
	}
}

func (c *capturingWriter) WriteHeader(status int) {
	if c.headerWritten {
		// Defensive: net/http already logs a "superfluous WriteHeader"
		// in that case; don't compound it.
		return
	}
	c.headerWritten = true
	c.status = status
	if ct := c.ResponseWriter.Header().Get("Content-Type"); strings.HasPrefix(strings.ToLower(ct), "application/json") {
		c.captureJSON = true
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	if !c.headerWritten {
		c.WriteHeader(http.StatusOK)
	}
	if c.captureJSON {
		avail := c.captureMaxLen - int64(c.buf.Len())
		if avail > 0 {
			take := int64(len(b))
			if take > avail {
				take = avail
			}
			c.buf.Write(b[:take])
		}
	}
	n, err := c.ResponseWriter.Write(b)
	c.bytesWritten += int64(n)
	return n, err
}

// Flush forwards to the underlying writer if it supports it (SSE).
func (c *capturingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Status reports the captured status, defaulting to 200 if the handler
// never called WriteHeader.
func (c *capturingWriter) Status() int {
	if c.status == 0 {
		return http.StatusOK
	}
	return c.status
}
