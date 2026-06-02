package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openmule/cli2api/internal/mulerun"
)

// mockUpstream returns a server that emulates mulerun's submit→poll flow:
// POST → 202 with task_info.id, GET → 200 with status:completed + images[].
// It also captures the last request body for assertion.
func mockUpstream(t *testing.T, capturedBody *[]byte) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if capturedBody != nil {
				*capturedBody = body
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task_info": map[string]any{"id": "task-x", "status": "pending", "created_at": "x", "updated_at": "x"},
			})
			return
		}
		// GET — poll
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": "task-x", "status": "completed", "created_at": "x", "updated_at": "x"},
			"images":    []string{"https://example/test.png"},
		})
	})
	return httptest.NewServer(mux)
}

func TestEdits_JSONPath(t *testing.T) {
	var body []byte
	srv := mockUpstream(t, &body)
	defer srv.Close()

	d := Deps{
		Client:          mulerun.New(srv.URL, "test-token"),
		ImageTimeout:    2 * time.Second,
		PollInterval:    5 * time.Millisecond,
		PollMaxInterval: 50 * time.Millisecond,
	}

	reqBody := `{"model":"nano-banana-edit","prompt":"add sunglasses","images":["https://input/a.png"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.Background())
	rr := httptest.NewRecorder()

	Edits(d)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp imageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://example/test.png" {
		t.Fatalf("response: %+v", resp)
	}

	// Body forwarded upstream should contain images[] and prompt.
	var forwarded map[string]any
	if err := json.Unmarshal(body, &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if forwarded["prompt"] != "add sunglasses" {
		t.Fatalf("prompt missing: %+v", forwarded)
	}
}

func TestEdits_MultipartPath(t *testing.T) {
	var body []byte
	srv := mockUpstream(t, &body)
	defer srv.Close()

	d := Deps{
		Client:          mulerun.New(srv.URL, "test-token"),
		ImageTimeout:    2 * time.Second,
		PollInterval:    5 * time.Millisecond,
		PollMaxInterval: 50 * time.Millisecond,
	}

	// Build a multipart body with model, prompt, and an image file.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "gpt-image-2-edit")
	_ = mw.WriteField("prompt", "oil painting")
	_ = mw.WriteField("size", "1024x1024")
	fw, _ := mw.CreateFormFile("image", "photo.png")
	// PNG signature; http.DetectContentType returns image/png for this prefix.
	_, _ = fw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(context.Background())
	rr := httptest.NewRecorder()

	Edits(d)(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rr.Code, rr.Body.String())
	}

	var forwarded map[string]any
	if err := json.Unmarshal(body, &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	imgs, ok := forwarded["images"].([]any)
	if !ok || len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %+v", forwarded["images"])
	}
	uri, _ := imgs[0].(string)
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("expected data: URI, got %q", uri)
	}
	if forwarded["prompt"] != "oil painting" {
		t.Fatalf("prompt missing: %+v", forwarded)
	}
}

func TestEdits_MissingPrompt(t *testing.T) {
	d := Deps{
		Client:          mulerun.New("http://unused", "tok"),
		ImageTimeout:    1 * time.Second,
		PollInterval:    10 * time.Millisecond,
		PollMaxInterval: 50 * time.Millisecond,
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"gpt-image-2-edit","images":["https://x"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	Edits(d)(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: %d", rr.Code)
	}
}

func TestEdits_MultipartWithMaskFile(t *testing.T) {
	var body []byte
	srv := mockUpstream(t, &body)
	defer srv.Close()

	d := Deps{
		Client:          mulerun.New(srv.URL, "test-token"),
		ImageTimeout:    2 * time.Second,
		PollInterval:    5 * time.Millisecond,
		PollMaxInterval: 50 * time.Millisecond,
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "gpt-image-2-edit")
	_ = mw.WriteField("prompt", "remove background")

	// image file
	fw, _ := mw.CreateFormFile("image", "photo.png")
	_, _ = fw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})

	// mask file (also PNG header so DetectContentType picks image/png)
	mfw, _ := mw.CreateFormFile("mask", "mask.png")
	_, _ = mfw.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0xff, 0xff})
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(context.Background())
	rr := httptest.NewRecorder()

	Edits(d)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rr.Code, rr.Body.String())
	}

	var forwarded map[string]any
	if err := json.Unmarshal(body, &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	mask, _ := forwarded["mask"].(string)
	if !strings.HasPrefix(mask, "data:image/png;base64,") {
		t.Fatalf("mask not encoded as data: URI, got %q", mask)
	}
}

func TestEdits_UnknownModel(t *testing.T) {
	d := Deps{Client: mulerun.New("http://unused", "tok")}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"nope","prompt":"x","images":["https://x"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	Edits(d)(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status: %d", rr.Code)
	}
}

// TestImages_RejectsEditOnlyModel guards Codex finding P2: calling
// /v1/images/generations with an edit-only model used to panic because
// `gpt-image-2-edit` has Kind=Image but no MapImage.
func TestImages_RejectsEditOnlyModel(t *testing.T) {
	d := Deps{Client: mulerun.New("http://unused", "tok")}
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations",
		strings.NewReader(`{"model":"gpt-image-2-edit","prompt":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	Images(d)(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rr.Code, rr.Body.String())
	}
}
