//go:build windows

package store

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// errLockHeld means a non-blocking lock attempt found the lock owned by another
// process.
var errLockHeld = errors.New("lock held by another process")

// lockOffset is where the locked byte lives.
const lockOffset = 0x40000000

// lockRegion locks the first byte of the file. LockFileEx byte-range locks are
// mandatory and, like flock, are released by the OS when the process dies, so a
// killed daemon leaves no stale claim behind.
//
// The byte sits far past the end of the file: a mandatory lock on byte 0 would
// stop the process that loses the race from reading the holder's pid out of the
// daemon lock file.
func lockRegion(f *os.File, flags uint32) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol)
}

// lockExclusive blocks until f holds an exclusive lock.
func lockExclusive(f *os.File) error {
	return lockRegion(f, windows.LOCKFILE_EXCLUSIVE_LOCK)
}

// tryLockExclusive takes the exclusive lock without waiting, or returns
// errLockHeld.
func tryLockExclusive(f *os.File) error {
	err := lockRegion(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errLockHeld
	}
	return err
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{Offset: lockOffset})
}

// replaceFile moves oldPath over newPath. Windows refuses the replace while any
// process has the destination open without delete sharing, which a concurrent
// reader (the daemon, the CLI, an editor, antivirus) does for a moment, so a
// short bounded retry stands in for the POSIX guarantee.
func replaceFile(oldPath, newPath string) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = os.Rename(oldPath, newPath); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return err
}
