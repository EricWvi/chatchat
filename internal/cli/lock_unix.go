//go:build unix

package cli

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on f, released when f is closed.
func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
