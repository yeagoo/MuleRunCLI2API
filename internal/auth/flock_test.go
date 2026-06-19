//go:build linux

// flock(2) semantics differ across unix platforms:
//   * Linux: per-open-file-description. Each open() call returns a
//     distinct OFD, so multiple opens within ONE process contend —
//     which is what TestFlock_SidecarSurvivesCacheRename relies on.
//   * macOS / *BSD: per-process (or per-file-table-entry). Same-process
//     opens can both "acquire" the same lock, so the goroutine-based
//     contention assertion (maxSeen == 1) trivially breaks even though
//     INTER-process locking still works correctly.
//
// The production code (flock_unix.go) is portable across all unix; only
// the test's same-process contention model is Linux-specific. CI runs
// ubuntu-latest, so gating to Linux here is the cheapest fix. macOS
// devs running `go test ./...` locally will see this test silently
// skipped rather than misleadingly fail.

package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// Regression for codex review finding #3: the lock previously sat on the
// cache file's inode, but writeOAuthCacheFile replaces that inode via
// os.Rename. A second process that opened the cache path AFTER a rename
// would lock the NEW inode and enter the critical section concurrently.
//
// With the sidecar lock file (`path + ".lock"`) the lock target is stable
// across cache writes. This test exercises the contention model: N
// goroutines all race through tryCacheFile, each doing a refresh + write.
// The withCacheLock guard must serialize them so only ONE refresh
// reaches the (fake) server — proving the lock survives the writeback's
// os.Rename.
func TestFlock_SidecarSurvivesCacheRename(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "oauth_cache.json")

	// Seed the cache so writeOAuthCacheFile has something to rename.
	payload, _ := json.Marshal(map[string]any{"access_token": "seed"})
	if err := os.WriteFile(cachePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	const N = 8
	var inCritical atomic.Int32
	var maxSeen atomic.Int32

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = withCacheLock(cachePath, func() error {
				now := inCritical.Add(1)
				// Track the peak — if the lock works, peak must stay 1.
				for {
					prev := maxSeen.Load()
					if now <= prev || maxSeen.CompareAndSwap(prev, now) {
						break
					}
				}
				// Simulate the rename-during-critical-section that broke
				// the old (file-inode) lock: rewrite the cache file with
				// os.Rename, which atomically replaces the inode.
				tmp := cachePath + ".tmp.test"
				if err := os.WriteFile(tmp, []byte(`{"access_token":"x"}`), 0o600); err == nil {
					_ = os.Rename(tmp, cachePath)
				}
				inCritical.Add(-1)
				return nil
			})
		}(i)
	}
	wg.Wait()

	if max := maxSeen.Load(); max != 1 {
		t.Fatalf("expected lock to serialize all %d goroutines (max in critical section = 1), got max = %d", N, max)
	}

	// And the sidecar `.lock` file should exist (it was created on first
	// call and persists for future calls).
	if _, err := os.Stat(cachePath + ".lock"); err != nil {
		t.Fatalf("sidecar lock file should persist: %v", err)
	}
}

func TestFlock_ReadOnlyDirDegradesGracefully(t *testing.T) {
	// When the lock file can't be created (e.g., dir is read-only), fn
	// should still run rather than failing the startup. The contract is:
	// best-effort coordination, not a security boundary.
	//
	// Skip under root — chmod 0o500 doesn't restrict UID 0 from creating
	// files, so the degradation path is never exercised on root-CI and
	// the test would pass vacuously (claude review #5).
	if os.Geteuid() == 0 {
		t.Skip("chmod is bypassed by root; run as non-root to exercise the read-only fallback")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("cannot test read-only dir: chmod failed")
	}
	defer os.Chmod(dir, 0o700)

	cachePath := filepath.Join(dir, "oauth_cache.json")
	ran := false
	err := withCacheLock(cachePath, func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("unlocked fallback should not error: %v", err)
	}
	if !ran {
		t.Fatal("fn must run even without a lock")
	}
}
