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
	// retention=1h means hard-cap is far in the future; only terminal+expired
	// gets deleted in this test.
	StartReaper(ctx, s, 20*time.Millisecond, time.Hour, 3.0, log)

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
	StartReaper(ctx, s, 0, time.Hour, 3.0, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestReaper_RetentionZeroSparesInFlightLegacy(t *testing.T) {
	// Codex round-3 P2.1 follow-up: setting retention=0 must NOT wipe
	// pre-existing rows. Earlier a 0 retention computed hardCutoff=now,
	// matching every legacy row and reaping in-flight jobs.
	s := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Two legacy rows from a prior config (retention was non-zero then).
	for _, j := range []*Job{
		{LocalID: "old-completed", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-1", CreatedAt: 1, ExpiresAt: 2, Status: "completed"},
		{LocalID: "old-inflight", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-2", CreatedAt: 1, ExpiresAt: 2, Status: "queued"},
	} {
		if err := s.Put(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// retention=0 ⇒ "never expire". Reaper still ticks, but must NOT delete.
	StartReaper(ctx, s, 20*time.Millisecond, 0, 3.0, log)

	time.Sleep(150 * time.Millisecond) // ~5+ ticks

	if g, _ := s.Get(ctx, "old-completed"); g == nil {
		t.Fatal("retention=0 must NOT reap legacy completed row")
	}
	if g, _ := s.Get(ctx, "old-inflight"); g == nil {
		t.Fatal("retention=0 must NOT reap legacy in-flight row")
	}
}

func TestReaper_HardCapDeletesAbandonedInFlight(t *testing.T) {
	// Simulate the case Codex P2.1 flagged: a client submits a job and
	// then never polls. Local status stays "queued" forever, so the
	// "terminal+expired" predicate alone never fires. The hard-cap
	// predicate must still reap it.
	s := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// expires_at=2: any sweep at unix>=2 with the soft predicate would skip
	// it (status=queued), but a hard cutoff >= 2 should reap.
	if err := s.Put(ctx, &Job{LocalID: "abandoned", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-z", CreatedAt: 1, ExpiresAt: 2, Status: "queued"}); err != nil {
		t.Fatal(err)
	}

	// retention=1ms, multiplier=1 → hard cutoff = now - 1ms; well past 2.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	StartReaper(ctx, s, 20*time.Millisecond, time.Millisecond, 1.0, log)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got, _ := s.Get(ctx, "abandoned"); got == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("hard-cap reaper did not delete abandoned in-flight job")
}
