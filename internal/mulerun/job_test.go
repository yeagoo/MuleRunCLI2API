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

func TestPoll_4xxIsTerminal(t *testing.T) {
	// Permanent 4xx (403/404/410) means the task is gone or never existed;
	// stop the loop with a terminal failure.
	for _, status := range []int{403, 404, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
			}))
			defer srv.Close()
			c := New(srv.URL, "test-token")
			res, done, err := c.Poll(context.Background(), "/v1/foo/generation", "task-x")
			if err != nil {
				t.Fatalf("expected no err, got %v", err)
			}
			if !done {
				t.Fatal("expected done=true on permanent 4xx")
			}
			if res.Err == nil || res.Err.Code != status {
				t.Fatalf("expected VendorError with code=%d, got %+v", status, res.Err)
			}
			if res.Status != "failed" {
				t.Fatalf("expected status=failed, got %q", res.Status)
			}
		})
	}
}

func TestPoll_TransientStatusesRetry(t *testing.T) {
	// 401/408/425/429 are transient — ratelimit, request timeout, early
	// hint, token-rotation blip. The loop must NOT mark the job failed.
	for _, status := range []int{401, 408, 425, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()
			c := New(srv.URL, "test-token")
			_, done, err := c.Poll(context.Background(), "/v1/foo/generation", "task-x")
			if err == nil {
				t.Fatal("expected non-nil err so caller retries")
			}
			if done {
				t.Fatal("expected done=false on transient 4xx so caller retries")
			}
		})
	}
}

func TestPoll_EmptyStatusIsTerminal(t *testing.T) {
	// 2xx with no status field is a contract violation — fail instead of
	// polling forever.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"task_info":{"id":"x","created_at":"x","updated_at":"x"}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "test-token")
	res, done, err := c.Poll(context.Background(), "/v1/foo/generation", "task-x")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !done || res.Status != "failed" || res.Err == nil {
		t.Fatalf("expected terminal failure, got done=%v status=%q err=%+v", done, res.Status, res.Err)
	}
}

func TestSubmitAndWait_4xxBreaksLoop(t *testing.T) {
	const taskID = "task-gone"
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/vendors/test/v1/foo/generation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": taskID, "status": "pending", "created_at": "x", "updated_at": "x"},
		})
	})
	mux.HandleFunc("/vendors/test/v1/foo/generation/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&polls, 1)
		// 403 is permanent (vs 401 which is now transient)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"revoked"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.SubmitAndWait(context.Background(), "/vendors/test/v1/foo/generation", map[string]string{"prompt": "x"}, 2*time.Second, 5*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("expected no error (vendor failure), got %v", err)
	}
	if res.Err == nil || res.Err.Code != 403 {
		t.Fatalf("expected vendor error 403, got %+v", res.Err)
	}
	if got := atomic.LoadInt32(&polls); got > 2 {
		t.Fatalf("expected at most 2 polls before giving up, got %d", got)
	}
}

func TestSubmitAndWait_RetriesTransientPollErrors(t *testing.T) {
	// A transient transport error (server closes connection mid-poll) must
	// NOT fail the job — the loop should retry and pick up the completion.
	const taskID = "task-flaky"
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/vendors/test/v1/foo/generation", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"task_info": map[string]any{"id": taskID, "status": "pending", "created_at": "x", "updated_at": "x"},
		})
	})
	mux.HandleFunc("/vendors/test/v1/foo/generation/"+taskID, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		switch n {
		case 1:
			// Simulate a dropped connection: hijack and close without writing.
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			w.WriteHeader(http.StatusBadGateway) // fallback transient
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable) // 503 transient
		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"task_info": map[string]any{"id": taskID, "status": "completed", "created_at": "x", "updated_at": "x"},
				"images":    []string{"https://example.test/ok.png"},
			})
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.SubmitAndWait(context.Background(), "/vendors/test/v1/foo/generation",
		map[string]string{"prompt": "x"}, 5*time.Second, 5*time.Millisecond, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("expected success despite transient poll errors, got %v", err)
	}
	if res.Status != "completed" || len(res.Images) != 1 {
		t.Fatalf("expected completed with 1 image, got %+v", res)
	}
	if got := atomic.LoadInt32(&polls); got < 3 {
		t.Fatalf("expected ≥3 polls (2 transient + success), got %d", got)
	}
}
