package mulerun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitAndWait_PollsToCompletion(t *testing.T) {
	const taskID = "task-123"
	var polls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/vendors/test/v1/foo/generation", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("wrong method on submit: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("missing/invalid auth header: %q", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": taskID, "status": "pending", "created_at": "x", "updated_at": "x"},
		})
	})
	mux.HandleFunc("/vendors/test/v1/foo/generation/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		var status string
		var images []string
		switch n {
		case 1:
			status = "processing"
		default:
			status = "completed"
			images = []string{"https://example.test/a.png"}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": taskID, "status": status, "created_at": "x", "updated_at": "x"},
			"images":    images,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.SubmitAndWait(context.Background(), "/vendors/test/v1/foo/generation", map[string]string{"prompt": "x"}, 5*time.Second, 10*time.Millisecond, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("status: %s", res.Status)
	}
	if len(res.Images) != 1 || res.Images[0] != "https://example.test/a.png" {
		t.Fatalf("images: %+v", res.Images)
	}
	if got := atomic.LoadInt32(&polls); got < 2 {
		t.Fatalf("expected ≥2 polls, got %d", got)
	}
}

func TestSubmitAndWait_FailureSurfaces(t *testing.T) {
	const taskID = "task-fail"
	mux := http.NewServeMux()
	mux.HandleFunc("/vendors/test/v1/foo/generation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": taskID, "status": "pending", "created_at": "x", "updated_at": "x"},
		})
	})
	mux.HandleFunc("/vendors/test/v1/foo/generation/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{
				"id": taskID, "status": "failed", "created_at": "x", "updated_at": "x",
				"error": map[string]any{"code": 3001, "title": "boom", "detail": "kaboom"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.SubmitAndWait(context.Background(), "/vendors/test/v1/foo/generation", map[string]string{"prompt": "x"}, 2*time.Second, 5*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("did not expect error, got %v", err)
	}
	if res.Err == nil || res.Err.Code != 3001 {
		t.Fatalf("expected vendor error 3001, got %+v", res.Err)
	}
}

func TestSubmitAndWait_Timeout(t *testing.T) {
	const taskID = "task-hang"
	mux := http.NewServeMux()
	mux.HandleFunc("/vendors/test/v1/foo/generation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"task_info": map[string]any{"id": taskID, "status": "pending", "created_at": "x", "updated_at": "x"}})
	})
	mux.HandleFunc("/vendors/test/v1/foo/generation/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"task_info": map[string]any{"id": taskID, "status": "processing", "created_at": "x", "updated_at": "x"}})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.SubmitAndWait(context.Background(), "/vendors/test/v1/foo/generation", map[string]string{"prompt": "x"}, 50*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond)
	if err != ErrJobTimeout {
		t.Fatalf("expected ErrJobTimeout, got %v", err)
	}
}
