//go:build unix

package procown

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillTreeTakesDescendantsInTheirOwnGroups is the case the Codex spike
// measured: a harness starts a tool process in a *different* process group, so
// killing the harness's own group leaves that tool running, reparented to PID 1.
// A cancelled run that leaves work behind is worse than one that never started.
func TestKillTreeTakesDescendantsInTheirOwnGroups(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")

	// A stand-in harness: it starts a long sleep in a new session (and therefore
	// a new process group), records its pid, and then waits itself. macOS has no
	// setsid(1), so perl — which it does ship — makes the call.
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("no perl here to put a child in a process group of its own")
	}
	script := filepath.Join(dir, "harness.sh")
	body := "#!/bin/sh\n" +
		perl + " -e 'use POSIX; POSIX::setsid(); exec(\"sleep\", \"300\")' & echo $! > " + pidFile + "\n" +
		"sleep 300\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write harness: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, script)
	release, err := Start(cmd, nil)
	if err != nil {
		t.Fatalf("start harness: %v", err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait(); release() })

	grandchild := waitForPID(t, pidFile)
	if grandchild == cmd.Process.Pid {
		t.Fatal("the test needs the sleep in a process of its own")
	}
	// The pid is recorded the moment the shell forks, which is before the child
	// has called setsid — so wait for the group to actually change rather than
	// reading it too early and concluding the case cannot be tested.
	if !waitForOwnGroup(grandchild, cmd.Process.Pid) {
		t.Skip("this system kept the child in the harness's process group; " +
			"the descendant-group case cannot be exercised here")
	}

	cancel()
	_ = cmd.Wait()

	if alive(grandchild) {
		_ = syscall.Kill(grandchild, syscall.SIGKILL) // do not leave it behind either way
		t.Fatalf("process %d survived the cancel in its own process group", grandchild)
	}
}

// waitForOwnGroup reports whether pid ended up in a process group of its own,
// which is the situation this test exists for.
func waitForOwnGroup(pid, harness int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pgid, err := syscall.Getpgid(pid)
		if err != nil {
			return false
		}
		if pgid != harness {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestStartFailureLeavesNothing: a program that cannot be started is reported
// and leaves no process and no cancel hook that could fire later.
func TestStartFailureLeavesNothing(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if _, err := Start(cmd, nil); err == nil {
		t.Fatal("starting a missing program reported success")
	}
	if cmd.Process != nil {
		t.Fatal("a failed start left a process")
	}
}

// TestStartKeepsTheCallersAttributes: ownership adds the process group to what
// the caller configured instead of replacing it.
func TestStartKeepsTheCallersAttributes(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	attr := &syscall.SysProcAttr{}
	cmd.SysProcAttr = attr
	release, err := Start(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr != attr || !attr.Setpgid {
		t.Fatalf("attributes lost or group missing: %+v", cmd.SysProcAttr)
	}
}

// TestCancelRunsInterruptFirst: the interrupt an environment supplies runs
// ahead of the hard stop of the tree.
func TestCancelRunsInterruptFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 300")
	var order []string
	release, err := Start(cmd, func() {
		order = append(order, "interrupt")
		if cmd.Process != nil && syscall.Kill(cmd.Process.Pid, 0) == nil {
			order = append(order, "alive")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancel()
	err = cmd.Wait()
	if strings.Join(order, ",") != "interrupt,alive" {
		t.Fatalf("interrupt did not run ahead of the stop: %v", order)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) && !errors.Is(err, context.Canceled) {
		t.Fatalf("wait: %v", err)
	}
}

// waitForPID reads the pid the harness recorded, waiting for it to appear.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the harness never recorded its child's pid")
	return 0
}

// alive reports whether a process still exists, giving the kill a moment to
// land — signals are delivered asynchronously.
func alive(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
