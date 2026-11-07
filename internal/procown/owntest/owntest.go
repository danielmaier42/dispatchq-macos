//go:build unix

// Package owntest holds fixtures for tests of process-tree ownership: stand-in
// provider programs that leave harmless sleeping descendants behind, and the
// checks that tell whether those descendants survived. Every process a fixture
// starts is killed when the test ends, whatever the test concluded.
package owntest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Script writes an executable shell script with body and returns its path.
func Script(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// PIDFile returns a fresh path a fixture records a pid in. Whatever pid ends
// up recorded there is killed when the test ends, also when the test failed
// before it read the file.
func PIDFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.pid")
	t.Cleanup(func() {
		if pid := Await(path, 0); pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return path
}

// Spawn is the shell line that starts a 300-second sleep in the background
// and records its pid in pidFile. With holdOutput the sleep inherits the
// script's stdout and stderr, as a tool a CLI starts usually does; otherwise
// they go to /dev/null.
func Spawn(pidFile string, holdOutput bool) string {
	redirect := ""
	if !holdOutput {
		redirect = " >/dev/null 2>&1"
	}
	return "sleep 300" + redirect + " & echo $! > '" + pidFile + ".tmp' && mv '" + pidFile + ".tmp' '" + pidFile + "'"
}

// Escape is the shell line that leaves a daemon behind: a process that forked
// twice into a session of its own, so it is no descendant any more, but still
// holds the script's stdout and stderr. It records its pid in pidFile and the
// line returns once it is in place. Build servers and file watchers a tool
// starts do exactly this; no process-tree walk can find it.
func Escape(t *testing.T, pidFile string) string {
	t.Helper()
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("no perl here to daemonize a child")
	}
	return perl + ` -e 'use POSIX; exit if fork; POSIX::setsid(); exit if fork; open(my $f, ">", "` + pidFile + `.tmp"); print $f $$; close($f); rename("` + pidFile + `.tmp", "` + pidFile + `"); sleep 300'` + "\n" +
		"while [ ! -s '" + pidFile + "' ]; do sleep 0.05; done"
}

// PID waits for the pid a fixture records and makes sure that process is
// killed when the test ends.
func PID(t *testing.T, pidFile string) int {
	t.Helper()
	pid := Await(pidFile, 10*time.Second)
	if pid == 0 {
		t.Fatalf("the fixture never recorded a pid in %s", pidFile)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// Await waits up to limit for the pid a fixture records and returns it, or 0.
// Unlike PID it may be called from any goroutine.
func Await(pidFile string, limit time.Duration) int {
	deadline := time.Now().Add(limit)
	for {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 {
				return pid
			}
		}
		if !time.Now().Before(deadline) {
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// CancelWhenRecorded returns a context that is cancelled as soon as the
// fixture has recorded its pid in pidFile, that is once the tool it starts is
// running, or after limit at the latest. A fixed short deadline would race a
// slow first start of a fresh script.
func CancelWhenRecorded(pidFile string, limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		Await(pidFile, limit)
		cancel()
	}()
	return ctx, cancel
}

// Gone reports whether pid has ended, giving a kill a moment to land: signals
// are delivered asynchronously. A zombie its parent has not reaped yet counts
// as ended.
func Gone(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil || zombie(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// Alive reports whether pid is still running, without waiting.
func Alive(pid int) bool { return syscall.Kill(pid, 0) == nil && !zombie(pid) }

func zombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // a pid this test recorded
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// Within runs f and fails the test if it does not return within limit: the
// defect these fixtures exist for is a call that never returns.
func Within[T any](t *testing.T, limit time.Duration, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(limit):
		t.Fatalf("the call did not return within %s", limit)
	}
	var zero T
	return zero
}

// OpenFDs counts this process's open file descriptors among the first 4096.
func OpenFDs(t *testing.T) int {
	t.Helper()
	n := 0
	for fd := range 4096 {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			n++
		}
	}
	return n
}
