//go:build unix

package datadirlock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes a non-blocking exclusive advisory lock (flock LOCK_EX|LOCK_NB).
// EWOULDBLOCK/EAGAIN means another process holds it, reported as ErrLocked. The
// lock is bound to the open file descriptor, so the kernel drops it when the
// process exits, giving automatic recovery after a crash.
func tryLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrLocked
	}
	return err
}

func unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
