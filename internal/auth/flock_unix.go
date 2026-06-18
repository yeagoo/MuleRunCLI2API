//go:build unix

package auth

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// withCacheLock acquires an exclusive flock on `path` (creating the file
// with mode 0600 if it doesn't exist) for the duration of fn. The lock is
// released on return. This serializes the read-refresh-write window so two
// cli2api processes — or cli2api + a concurrent `mulerun login` — can't
// each consume the rotating refresh_token and invalidate the other.
//
// If the lock file cannot be opened (e.g. read-only home), fn runs anyway
// without locking: better degraded coordination than a hard failure on the
// startup path. The caller gets the same outcome as before this helper was
// introduced.
func withCacheLock(path string, fn func() error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		// Common when the cache lives under a directory cli2api can read
		// but not write (e.g. another user's $HOME). Run unlocked rather
		// than blocking startup.
		return fn()
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
			// Filesystem doesn't support flock (some FUSE mounts, NFS
			// without lockd). Degrade gracefully.
			return fn()
		}
		return fmt.Errorf("flock %s: %w", path, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	return fn()
}
