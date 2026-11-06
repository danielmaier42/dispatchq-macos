package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// daemonLockFile is held for as long as a daemon owns the data directory. It is
// deliberately not the same file as lockFile: that one is taken and released
// around every read-modify-write, and a daemon holding it forever would block
// every dispatchq CLI process.
const daemonLockFile = ".daemon.lock"

// ErrDaemonRunning means another process already owns the data directory. It
// carries the holder's pid when the lock file names one.
type ErrDaemonRunning struct {
	// Home is the data directory that is already owned.
	Home string
	// PID is the process that owns it, or 0 when the lock file said nothing.
	PID int
}

func (e *ErrDaemonRunning) Error() string {
	who := "another dispatchqd"
	if e.PID > 0 {
		who = fmt.Sprintf("dispatchqd (pid %d)", e.PID)
	}
	return fmt.Sprintf("%s is already running for %s", who, e.Home)
}

// DaemonLock is one daemon's claim on a data directory, released by Close or by
// the process exiting.
type DaemonLock struct {
	mu sync.Mutex
	f  *os.File
}

// Close releases the claim. It is safe to call from several goroutines and to
// call twice: the daemon releases it on the way out of a deferred call, and the
// shutdown path may already have done it.
func (l *DaemonLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = unlockFile(f)
	return f.Close()
}

// LockDaemon claims the data directory for this process, so a second daemon
// started against the same store refuses to run instead of scheduling the same
// tasks in parallel. Two daemons on one store double every run they both see,
// and each one's startup reconciliation writes off the other's in-flight run as
// interrupted.
//
// The claim is an OS file lock (flock on macOS, LockFileEx on Windows), so it dies with the process: a daemon killed with
// SIGKILL or lost to a power cut leaves no stale lock behind to clear by hand.
// It waits up to wait for a predecessor to let go, which covers the hand-over
// during an update (the new daemon is bootstrapped while the old one is still
// shutting down) without waiting on a daemon that is simply there to stay.
//
// The lock is per data directory, so a second daemon with its own DISPATCHQ_HOME
// is unaffected.
func (s *Store) LockDaemon(wait time.Duration) (*DaemonLock, error) {
	path := s.path(daemonLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err = tryLockExclusive(f)
		if err == nil {
			break
		}
		if !errors.Is(err, errLockHeld) {
			_ = f.Close()
			return nil, fmt.Errorf("acquire daemon lock: %w", err)
		}
		if time.Now().After(deadline) {
			holder := readLockPID(path)
			_ = f.Close()
			return nil, &ErrDaemonRunning{Home: s.home, PID: holder}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Name the owner while the lock is held, so the process that loses the race
	// can say who has it. Best-effort: an unwritable pid costs nothing but the
	// nicer message.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &DaemonLock{f: f}, nil
}

// readLockPID reads the pid out of the lock file, or 0 when it holds no number.
func readLockPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}
