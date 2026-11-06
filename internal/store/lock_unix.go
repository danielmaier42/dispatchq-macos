//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// errLockHeld means a non-blocking lock attempt found the lock owned by another
// process.
var errLockHeld = errors.New("lock held by another process")

// lockExclusive blocks until f holds an exclusive advisory lock.
func lockExclusive(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// tryLockExclusive takes the exclusive lock without waiting, or returns
// errLockHeld.
func tryLockExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}
	return err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// replaceFile moves oldPath over newPath atomically.
func replaceFile(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }
