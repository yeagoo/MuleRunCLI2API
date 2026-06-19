package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// rewriteVendorModel — pure function tests

func TestRewriteVendorModel_OpenAIRouted(t *testing.T) {
	in := `{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	path, out, ok := rewriteVendorModel([]byte(in))
	if !ok {
		t.Fatal("expected rewrite hit")
	}
	if path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("wrong path: %q", path)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["model"] != "gpt-5.5" {
		t.Fatalf("model not stripped: %v", got["model"])
	}
}

func TestRewriteVendorModel_NoPrefix(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"gpt-5","messages":[]}`))
	if ok {
		t.Fatal("plain model should not rewrite")
	}
}

func TestRewriteVendorModel_UnknownVendor(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"google/gemini-3"}`))
	if ok {
		t.Fatal("unknown vendor should not rewrite")
	}
}

func TestRewriteVendorModel_TrailingSlash(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte(`{"model":"openai/"}`))
	if ok {
		t.Fatal("trailing-slash model should not rewrite")
	}
}

func TestRewriteVendorModel_MalformedJSON(t *testing.T) {
	_, _, ok := rewriteVendorModel([]byte("not json"))
	if ok {
		t.Fatal("malformed JSON should not pretend to rewrite")
	}
}

// #2 from prior round — numeric precision via json.RawMessage

func TestRewriteVendorModel_PreservesLargeInt(t *testing.T) {
	const bigSeed = 9007199254740993
	in := `{"model":"openai/gpt-5.5","seed":9007199254740993,"messages":[]}`
	_, out, ok := rewriteVendorModel([]byte(in))
	if !ok {
		t.Fatal("expected rewrite")
	}
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("large int corrupted; output: %s", out)
	}
	d := json.NewDecoder(bytes.NewReader(out))
	d.UseNumber()
	var got map[string]any
	if err := d.Decode(&got); err != nil {
		t.Fatal(err)
	}
	n, _ := got["seed"].(json.Number).Int64()
	if n != bigSeed {
		t.Fatalf("seed corrupted: want %d got %d", bigSeed, n)
	}
}

// bufferForRewrite — handler integration tests

func newBufferReq(body string, headers map[string]string) (*http.Request, *httptest.ResponseRecorder) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r, httptest.NewRecorder()
}

func TestBufferForRewrite_SmallBodyReturnsBytesAndRestoresBody(t *testing.T) {
	// Plain body that fits in the peek → body is returned (non-nil),
	// and r.Body has been restored to a fresh reader over the same bytes
	// so a caller that decides not to rewrite can stream-forward.
	r, w := newBufferReq(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`, nil)
	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		t.Fatal("small body should return non-nil bytes")
	}
	if !bytes.Contains(body, []byte("gpt-5")) {
		t.Fatalf("body contents wrong: %s", body)
	}
	rest, _ := io.ReadAll(r.Body)
	if !bytes.Contains(rest, []byte("gpt-5")) {
		t.Fatal("r.Body not restored after peek")
	}
}

func TestBufferForRewrite_VendorPrefixDeepInBody(t *testing.T) {
	// Codex round 1 — body where messages serialize BEFORE model with
	// the peek-sized prelude containing no '/'. Must buffer fully so the
	// model field is parseable regardless of position.
	big := strings.Repeat("x", 8000)
	in := `{"messages":[{"role":"user","content":"` + big + `"}],"model":"openai/gpt-5.5"}`
	r, w := newBufferReq(in, nil)
	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		t.Fatal("oversize body must return buffered bytes")
	}
	path, rewritten, ok := rewriteVendorModel(body)
	if !ok || path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("vendor model deep in body lost: ok=%v path=%q", ok, path)
	}
	if !bytes.Contains(rewritten, []byte(`"model":"gpt-5.5"`)) {
		t.Fatal("prefix not stripped from deep-body request")
	}
}

func TestBufferForRewrite_BigBodyByteExact(t *testing.T) {
	// Claude round 4 #4 — assert byte equality, not just length.
	big := strings.Repeat("x", 10_000)
	in := `{"model":"gpt-5","messages":[{"role":"user","content":"` + big + `"}]}`
	r, w := newBufferReq(in, nil)
	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte(in)) {
		t.Fatalf("body bytes differ from input")
	}
}

// Claude round 4 #1 — gzip + vendor prefix routes correctly

func TestBufferForRewrite_GzipVendorPrefixDecodedAndRouted(t *testing.T) {
	original := `{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(original))
	_ = gz.Close()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader(buf.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()

	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatalf("unexpected err on gzip body: %v", err)
	}
	if body == nil {
		t.Fatal("gzip body should be decoded and returned")
	}
	if r.Header.Get("Content-Encoding") != "" {
		t.Fatalf("Content-Encoding header should be cleared after decode, got %q", r.Header.Get("Content-Encoding"))
	}
	if !bytes.Equal(body, []byte(original)) {
		t.Fatalf("decompressed body mismatch:\n got: %s\nwant: %s", body, original)
	}
	path, _, ok := rewriteVendorModel(body)
	if !ok || path != "/vendors/openai/v1/chat/completions" {
		t.Fatalf("vendor routing broke after gzip decode: ok=%v path=%q", ok, path)
	}
}

func TestBufferForRewrite_InvalidGzip(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader("not actually gzipped"))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()
	_, err := bufferForRewrite(w, r)
	if err == nil {
		t.Fatal("expected error from invalid gzip")
	}
}

// Codex round 5 #1 — gzip bombs must 413, not silently truncate
func TestBufferForRewrite_GzipBombReturns413(t *testing.T) {
	// Compress a payload that decodes to MaxRequestBody+1MB. The
	// compressed size is tiny (highly compressible repeating bytes);
	// only the DECODED size triggers the cap. The old io.LimitReader
	// truncated silently; the new http.MaxBytesReader returns
	// *MaxBytesError → 413 via writeChatError.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	chunk := bytes.Repeat([]byte("a"), 1<<16) // 64 KB
	// Write a bit more than MaxRequestBody decompressed.
	for written := 0; written <= MaxRequestBody+(1<<20); written += len(chunk) {
		_, _ = gz.Write(chunk)
	}
	_ = gz.Close()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader(buf.Bytes()))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()

	_, err := bufferForRewrite(w, r)
	if err == nil {
		t.Fatal("oversized decoded gzip body must error, not silently truncate")
	}
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		t.Fatalf("expected *http.MaxBytesError for oversized gzip, got %T: %v", err, err)
	}
}

// Codex round 5 #2 — truncated gzip stream must error, not be served as short body
func TestBufferForRewrite_TruncatedGzipReturnsError(t *testing.T) {
	// Encode a valid gzip stream, then chop off the trailing checksum +
	// length footer. gzip.Reader.Read on the truncated input returns
	// io.ErrUnexpectedEOF. The old peek-based path folded that into a
	// clean short read and forwarded a partial decoded body to upstream
	// as if it were complete. The new ReadAll path surfaces the error.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(`{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`))
	_ = gz.Close()
	full := buf.Bytes()
	// gzip footer is 8 bytes (CRC32 + uncompressed size). Chop them
	// off to simulate a network-truncated upload.
	if len(full) < 16 {
		t.Skip("gzip output too small to truncate meaningfully")
	}
	truncated := full[:len(full)-8]

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader(truncated))
	r.Header.Set("Content-Encoding", "gzip")
	w := httptest.NewRecorder()

	_, err := bufferForRewrite(w, r)
	if err == nil {
		t.Fatal("truncated gzip must error, not be served as a clean short body")
	}
}

func TestBufferForRewrite_UnknownEncodingBypass(t *testing.T) {
	// br/zstd/deflate not implemented. Returns body=nil so the caller
	// forwards r.Body untouched on the legacy path.
	r, w := newBufferReq("compressed-data", map[string]string{"Content-Encoding": "br"})
	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		t.Fatalf("unknown encoding should signal bypass via body=nil, got %d bytes", len(body))
	}
}

func TestBufferForRewrite_EmptyBody(t *testing.T) {
	r, w := newBufferReq("", nil)
	body, err := bufferForRewrite(w, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("empty body length nonzero: %d", len(body))
	}
}

// Codex round 1 — read errors surface, partial body not forwarded

type erroringReader struct{ reads atomic.Int32 }

func (e *erroringReader) Read(p []byte) (int, error) {
	e.reads.Add(1)
	return 0, io.ErrClosedPipe
}

func TestBufferForRewrite_ReadErrorSurfaces(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		io.NopCloser(&erroringReader{}))
	w := httptest.NewRecorder()
	_, err := bufferForRewrite(w, r)
	if err == nil {
		t.Fatal("expected error from failing Read")
	}
	if err != io.ErrClosedPipe {
		t.Fatalf("want ErrClosedPipe, got %v", err)
	}
}

// Codex round 1 — 413 on oversized body (MaxBytesReader)

func TestChat_413OnOversizedBody(t *testing.T) {
	const limit = 1024
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		head, err := readPeek(r.Body, chatPeekHead)
		_ = head
		if err == nil {
			t.Error("expected MaxBytesError")
			return
		}
		writeChatError(w, err)
	})
	body := strings.Repeat("a", limit+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"openai/gpt-5.5","junk":"`+body+`"}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d: %s", w.Code, w.Body.String())
	}
}

// Claude round 4 #3 — TransferEncoding stripped on body replacement

func TestSetForwardBody_ClearsTransferEncoding(t *testing.T) {
	// Client sent TE:chunked (no Content-Length). After we replace
	// r.Body with a bytes.Reader, we must clear r.TransferEncoding so
	// net/http doesn't emit both Content-Length AND TE:chunked on the
	// upstream request (RFC 7230 §3.3.3 mutual exclusion).
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader("x"))
	r.TransferEncoding = []string{"chunked"}
	setForwardBody(r, []byte(`{"model":"gpt-5"}`))
	if r.TransferEncoding != nil {
		t.Fatalf("TransferEncoding not cleared: %v", r.TransferEncoding)
	}
	if r.ContentLength != int64(len(`{"model":"gpt-5"}`)) {
		t.Fatalf("ContentLength wrong: %d", r.ContentLength)
	}
}
