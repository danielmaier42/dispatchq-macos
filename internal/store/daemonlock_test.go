package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockDaemonRefusesASecondDaemon(t *testing.T) {
	st := openTempStore(t)

	first, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("first LockDaemon: %v", err)
	}
	defer func() { _ = first.Close() }()

	_, err = st.LockDaemon(50 * time.Millisecond)
	var running *ErrDaemonRunning
	if !errors.As(err, &running) {
		t.Fatalf("second LockDaemon = %v, want ErrDaemonRunning", err)
	}
	if running.PID != os.Getpid() {
		t.Errorf("holder pid = %d, want %d", running.PID, os.Getpid())
	}
	if running.Home != st.Home() {
		t.Errorf("home = %q, want %q", running.Home, st.Home())
	}
}

func TestLockDaemonAfterReleaseAndWait(t *testing.T) {
	st := openTempStore(t)

	first, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("first LockDaemon: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}
	// Closing twice is what a daemon does when it both defers the release and
	// hits an error path that released already.
	if err := first.Close(); err != nil {
		t.Fatalf("release again: %v", err)
	}

	second, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("LockDaemon after release: %v", err)
	}
	defer func() { _ = second.Close() }()

	// A predecessor that lets go inside the wait window is waited for rather
	// than reported as a running daemon — the update hand-over case.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = second.Close()
	}()
	third, err := st.LockDaemon(2 * time.Second)
	if err != nil {
		t.Fatalf("LockDaemon while the predecessor shuts down: %v", err)
	}
	_ = third.Close()
}

// The daemon holds its claim for its whole life, so it must not be the lock the
// CLI takes for a config or state update — that would wedge every dispatchq
// command while the daemon runs.
func TestLockDaemonLeavesWritesAlone(t *testing.T) {
	st := openTempStore(t)

	lock, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("LockDaemon: %v", err)
	}
	defer func() { _ = lock.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- st.UpdateState(func(s *State) error {
			s.SetNotifiedProviderState("codex", "ready")
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpdateState while the daemon lock is held: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateState blocked on the daemon lock")
	}
}

func TestLockDaemonIsPerDataDirectory(t *testing.T) {
	first := openTempStore(t)
	second := openTempStore(t)

	a, err := first.LockDaemon(0)
	if err != nil {
		t.Fatalf("lock first home: %v", err)
	}
	defer func() { _ = a.Close() }()

	b, err := second.LockDaemon(0)
	if err != nil {
		t.Fatalf("lock second home: %v", err)
	}
	_ = b.Close()
}

func TestReadLockPIDIgnoresGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	if got := readLockPID(path); got != 0 {
		t.Errorf("missing file = %d, want 0", got)
	}
	if err := os.WriteFile(path, []byte("not a pid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLockPID(path); got != 0 {
		t.Errorf("garbage = %d, want 0", got)
	}
	if err := os.WriteFile(path, []byte(" 4711 \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLockPID(path); got != 4711 {
		t.Errorf("pid = %d, want 4711", got)
	}
}

func openTempStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st
}

// The probe names the holder and the time it wrote the lock file, and does not
// create a data directory where none exists.
func TestProbeDaemonLock(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	h, err := ProbeDaemonLock(missing)
	if err != nil || h.Held {
		t.Fatalf("probe of a missing store = %+v, %v; want not held", h, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created %s: %v", missing, err)
	}

	st := openTempStore(t)
	h, err = ProbeDaemonLock(st.Home())
	if err != nil || h.Held {
		t.Fatalf("probe before any daemon = %+v, %v; want not held", h, err)
	}

	lock, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("LockDaemon: %v", err)
	}
	h, err = ProbeDaemonLock(st.Home())
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !h.Held || h.PID != os.Getpid() {
		t.Errorf("probe = %+v, want held by pid %d", h, os.Getpid())
	}
	if got := DaemonLockPath(st.Home()); got != filepath.Join(st.Home(), ".daemon.lock") {
		t.Errorf("DaemonLockPath = %q", got)
	}
	// The probe must not have taken the lock from the holder.
	if _, err := st.LockDaemon(0); err == nil {
		t.Fatal("lock was free while the daemon held it")
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}
	h, err = ProbeDaemonLock(st.Home())
	if err != nil || h.Held {
		t.Fatalf("probe after release = %+v, %v; want not held", h, err)
	}
	// The probe left the lock free for the next daemon.
	next, err := st.LockDaemon(0)
	if err != nil {
		t.Fatalf("LockDaemon after probe: %v", err)
	}
	_ = next.Close()
}
