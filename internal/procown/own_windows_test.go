//go:build windows

package procown

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// These tests need a Windows machine. They run the test binary itself as the
// "provider": a helper that starts a descendant as its very first action, which
// is exactly the work a Job Object assigned after Start would miss.

const helperEnv = "DISPATCHQ_JOB_HELPER"

// TestMain lets the test binary act as the helper processes below.
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "spawner":
		// Start a long-lived child immediately, record its pid, then idle.
		child := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
		child.Env = append(os.Environ(), helperEnv+"=sleeper")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		pidFile := os.Getenv("DISPATCHQ_PID_FILE")
		tmp := pidFile + ".tmp"
		_ = os.WriteFile(tmp, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		_ = os.Rename(tmp, pidFile)
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "sleeper":
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	case "owner":
		// Stand in for the daemon: own a spawner through the real code path and
		// then wait to be killed abruptly.
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), helperEnv+"=spawner")
		if _, err := Start(cmd, nil); err != nil {
			os.Exit(3)
		}
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func readPID(t *testing.T, file string) uint32 {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return uint32(n)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

func waitGone(t *testing.T, pid uint32) {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return // already gone
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, _ := windows.WaitForSingleObject(h, 10_000)
	if ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("process %d survived", pid)
	}
}

func startSpawner(ctx context.Context, t *testing.T) (*exec.Cmd, uint32, func()) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=spawner", "DISPATCHQ_PID_FILE="+pidFile)
	release, err := Start(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, readPID(t, pidFile), release
}

// A descendant started as the helper's first action is in the job: cancelling
// the run ends it.
func TestCancelKillsDescendantStartedImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd, child, release := startSpawner(ctx, t)
	defer release()
	cancel()
	_ = cmd.Wait()
	waitGone(t, child)
}

// The daemon dying abruptly (no cancel, no release) still takes the tree along.
func TestOwnerDeathKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	owner := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	owner.Env = append(os.Environ(), helperEnv+"=owner", "DISPATCHQ_PID_FILE="+pidFile)
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	child := readPID(t, pidFile)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	waitGone(t, child)
}

// A process the job cannot own is not left running.
func TestAttachFailureDoesNotResume(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=sleeper")
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill() // gone before it can be assigned
	_ = cmd.Wait()
	if _, err := attach(cmd); err == nil {
		t.Fatal("attaching a dead process must fail, not report success")
	}
	if _, ok := jobs.Load(cmd); ok {
		t.Fatal("a failed attach left its job object registered")
	}
}

// The interrupt an environment supplies runs ahead of the hard stop.
func TestCancelRunsInterruptFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), helperEnv+"=sleeper")
	interrupted := make(chan struct{})
	release, err := Start(cmd, func() { close(interrupted) })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cancel()
	_ = cmd.Wait()
	select {
	case <-interrupted:
	default:
		t.Fatal("cancel did not run the environment's interrupt")
	}
}

// A program that ends on its own gives its job back: release leaves no handle
// registered, and a second program is owned independently.
func TestReleaseForgetsTheJob(t *testing.T) {
	for range 2 {
		cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^$")
		release, err := Start(cmd, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := jobs.Load(cmd); !ok {
			t.Fatal("a started program has no job object")
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		release()
		if _, ok := jobs.Load(cmd); ok {
			t.Fatal("release left the job object registered")
		}
	}
}

// A program that cannot be created is reported, and nothing is registered.
func TestStartFailureLeavesNothing(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "missing.exe"))
	if _, err := Start(cmd, nil); err == nil {
		t.Fatal("starting a missing program reported success")
	}
	if cmd.Process != nil {
		t.Fatal("a failed start left a process")
	}
	if _, ok := jobs.Load(cmd); ok {
		t.Fatal("a failed start registered a job object")
	}
}
