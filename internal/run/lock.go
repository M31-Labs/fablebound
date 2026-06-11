package run

import (
	"fmt"
	"os"
	"syscall"
)

// flockWrite opens path for writing (creating or truncating), acquires an
// exclusive advisory lock via syscall.Flock, calls fn to perform the write,
// then closes (which implicitly releases the lock).
//
// The lock is advisory: all tiller writers must use flockWrite for the
// guarantee to hold.
func flockWrite(path string, fn func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock %s: %w", path, err)
	}
	// Lock released implicitly on f.Close().

	return fn(f)
}
