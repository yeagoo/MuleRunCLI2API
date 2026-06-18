//go:build unix

package auth

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// withCacheLock acquires an exclusive flock over the read-refresh-write
// window on the cache file. Implementation note: flock(2) locks an
// underlying inode via the open file description, but our writeback path
// uses tmp + os.Rename, which REPLACES the inode at `path`. A lock on
// the renamed-away inode would be silently bypassed by any process that
// reopens `path` after the rename and locks the new inode.
//
// Workaround: lock a SIDECAR file (`path + ".lock"`) that is created once
// and never replaced. The lock file is treated as a coordination primitive,
// not data — its contents are empty and meaningless. Concurrent cli2api
// processes (and a concurrent `mulerun login` if it adopts the same
// convention) all flock the same stable inode.
//
// Graceful degradation: when the lock dir is read-only (e.g. running
// against another user's $HOME), we fall back to unlocked execution
// rather than crashing startup. The lock is best-effort coordination,
// not a security boundary.
func withCacheLock(path string, fn func() error) error {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		// Can't create the lock file (read-only mount, EACCES on the
		// directory, etc.). Run fn unlocked — better degraded coordination
		// than a hard failure on the startup path.
		return fn()
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
			// Filesystem doesn't support flock (some FUSE mounts, NFS
			// without lockd). Degrade gracefully.
			return fn()
		}
		return fmt.Errorf("flock %s: %w", lockPath, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	return fn()
}
