package jobstore

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestReaper_DeletesExpiredOverTime(t *testing.T) {
	s := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Job that expires at unix=2.
	if err := s.Put(ctx, &Job{LocalID: "expired", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-1", CreatedAt: 1, ExpiresAt: 2, Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	// Job that never expires.
	if err := s.Put(ctx, &Job{LocalID: "forever", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-2", CreatedAt: 1, ExpiresAt: 0, Status: "queued"}); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	StartReaper(ctx, s, 20*time.Millisecond, log)

	// Wait long enough for at least 2 sweeps.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got, _ := s.Get(ctx, "expired"); got == nil {
			// confirmed deleted; also verify forever survived
			if g, _ := s.Get(ctx, "forever"); g == nil {
				t.Fatal("forever was incorrectly reaped")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reaper did not delete expired job within deadline")
}

func TestReaper_DisabledWhenIntervalZero(t *testing.T) {
	s := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Should return immediately without spawning a goroutine.
	StartReaper(ctx, s, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
}
