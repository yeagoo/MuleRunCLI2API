//go:build !unix

package auth

// withCacheLock is a no-op on non-unix platforms. cli2api targets Linux
// containers and macOS dev hosts; Windows isn't supported. If we ever add
// Windows builds, replace this with LockFileEx via golang.org/x/sys/windows.
func withCacheLock(_ string, fn func() error) error { return fn() }
