package jobstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func runStoreSuite(t *testing.T, s Store) {
	ctx := context.Background()
	j := &Job{
		LocalID: "abc", Kind: KindVideo, Model: "sora-2",
		VendorPath: "/vendors/openai/v1/sora-2/generation", VendorTaskID: "vt-1",
		CreatedAt: 100, Status: "queued",
	}
	if err := s.Put(ctx, j); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := s.Get(ctx, "abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil for known id")
	}
	if got.Status != "queued" || got.Model != "sora-2" || got.Kind != KindVideo {
		t.Fatalf("mismatch: %+v", got)
	}

	j.Status = "completed"
	j.CompletedAt = 200
	j.ResultURLs = []string{"https://example/v.mp4"}
	if err := s.Put(ctx, j); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := s.Get(ctx, "abc")
	if got2.Status != "completed" || len(got2.ResultURLs) != 1 || got2.ResultURLs[0] != "https://example/v.mp4" {
		t.Fatalf("update mismatch: %+v", got2)
	}

	miss, err := s.Get(ctx, "does-not-exist")
	if err != nil || miss != nil {
		t.Fatalf("expected nil/nil, got %+v / %v", miss, err)
	}
}

func TestMemory(t *testing.T) {
	runStoreSuite(t, NewMemory())
}

func TestLibSQL_FileDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := OpenLibSQL("file:" + path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	runStoreSuite(t, s)
}

func TestLibSQL_PlainPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := OpenLibSQL(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	runStoreSuite(t, s)
}

func TestLibSQL_Persistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := OpenLibSQL("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, &Job{LocalID: "persist-me", Kind: KindMusic, Model: "music-2.5", VendorPath: "x", VendorTaskID: "y", CreatedAt: 1, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := OpenLibSQL("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Get(ctx, "persist-me")
	if err != nil || got == nil {
		t.Fatalf("expected record across restart, got %+v / %v", got, err)
	}
	if got.Kind != KindMusic {
		t.Fatalf("kind: %s", got.Kind)
	}
}

func runExpireSuite(t *testing.T, s Store) {
	ctx := context.Background()
	// Seed:
	//   "expired-completed" — terminal & expired → SHOULD be deleted
	//   "expired-failed"    — terminal & expired → SHOULD be deleted
	//   "expired-inflight"  — expired BUT still queued → MUST NOT be deleted
	//                         (jobs in-flight can run longer than retention)
	//   "fresh"             — not expired → MUST NOT be deleted
	//   "forever"           — ExpiresAt=0   → MUST NOT be deleted
	jobs := []*Job{
		{LocalID: "expired-completed", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-1", CreatedAt: 1, ExpiresAt: 50, Status: "completed"},
		{LocalID: "expired-failed", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-2", CreatedAt: 1, ExpiresAt: 50, Status: "failed"},
		{LocalID: "expired-inflight", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-3", CreatedAt: 1, ExpiresAt: 50, Status: "queued"},
		{LocalID: "fresh", Kind: KindVideo, Model: "x", VendorPath: "p", VendorTaskID: "vt-4", CreatedAt: 1, ExpiresAt: 1000, Status: "queued"},
		{LocalID: "forever", Kind: KindMusic, Model: "x", VendorPath: "p", VendorTaskID: "vt-5", CreatedAt: 1, ExpiresAt: 0, Status: "queued"},
	}
	for _, j := range jobs {
		if err := s.Put(ctx, j); err != nil {
			t.Fatalf("put %s: %v", j.LocalID, err)
		}
	}

	// Pass hardCutoff=0 to test only the soft predicate (terminal expired).
	n, err := s.DeleteExpired(ctx, 100, 0)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 deletions (terminal expired only), got %d", n)
	}

	if g, _ := s.Get(ctx, "expired-completed"); g != nil {
		t.Fatal("expired+completed job should have been deleted")
	}
	if g, _ := s.Get(ctx, "expired-failed"); g != nil {
		t.Fatal("expired+failed job should have been deleted")
	}
	if g, _ := s.Get(ctx, "expired-inflight"); g == nil {
		t.Fatal("expired but in-flight (queued) job MUST NOT be deleted by soft cutoff")
	}
	if g, _ := s.Get(ctx, "fresh"); g == nil {
		t.Fatal("fresh job missing")
	}
	if g, _ := s.Get(ctx, "forever"); g == nil {
		t.Fatal("forever job missing")
	}

	// Now apply a hard cutoff that crosses the in-flight job's expires_at.
	// It MUST be reaped — clients that abandoned polling shouldn't leak forever.
	n, err = s.DeleteExpired(ctx, 100, 200)
	if err != nil {
		t.Fatalf("delete with hard cutoff: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deletion (in-flight via hard cutoff), got %d", n)
	}
	if g, _ := s.Get(ctx, "expired-inflight"); g != nil {
		t.Fatal("hard cutoff should have reaped in-flight job")
	}
	if g, _ := s.Get(ctx, "fresh"); g == nil {
		t.Fatal("fresh job (expires_at=1000) must survive hard cutoff at 200")
	}
}

func TestMemory_DeleteExpired(t *testing.T) { runExpireSuite(t, NewMemory()) }

func TestLibSQL_DeleteExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s, err := OpenLibSQL("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runExpireSuite(t, s)
}

func TestLibSQL_MigrationFromV1(t *testing.T) {
	// Simulate an existing v1 database without expires_at; ensure the v2
	// migration runs cleanly and preserves data.
	path := filepath.Join(t.TempDir(), "v1.db")

	// Use a raw sql.DB to write a v1-shaped schema then bump user_version=1.
	rawDriver, rawDSN := resolveDriver("file:" + path)
	rawDB, err := sql.Open(rawDriver, rawDSN)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE jobs (
  local_id       TEXT PRIMARY KEY,
  kind           TEXT NOT NULL,
  model          TEXT NOT NULL,
  vendor_path    TEXT NOT NULL,
  vendor_task_id TEXT NOT NULL,
  created_at     INTEGER NOT NULL,
  completed_at   INTEGER NOT NULL DEFAULT 0,
  status         TEXT NOT NULL,
  result_urls    TEXT,
  err_code       INTEGER NOT NULL DEFAULT 0,
  err_message    TEXT NOT NULL DEFAULT ''
);
PRAGMA user_version = 1;`); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO jobs (local_id, kind, model, vendor_path, vendor_task_id, created_at, status, result_urls)
VALUES ('legacy', 'video', 'sora', 'p', 'vt', 100, 'completed', '["https://x/v.mp4"]')`); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	rawDB.Close()

	// Now open through our migration path.
	s, err := OpenLibSQL("file:" + path)
	if err != nil {
		t.Fatalf("open after migration: %v", err)
	}
	defer s.Close()

	got, err := s.Get(context.Background(), "legacy")
	if err != nil || got == nil {
		t.Fatalf("legacy row lost: %+v / %v", got, err)
	}
	if got.ExpiresAt != 0 {
		t.Fatalf("legacy ExpiresAt should be 0 (default), got %d", got.ExpiresAt)
	}
}
