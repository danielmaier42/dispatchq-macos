//go:build unix

package provider

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/procown/owntest"
)

// These run the real NativeHost against stand-in CLIs that leave a harmless
// sleep behind. Before native auxiliary calls owned their process tree, a
// cancelled or expired request killed only the CLI itself: the tool it had
// started kept running, and one holding the output kept the request waiting
// for as long as it lived.

type outResult struct {
	out []byte
	err error
}

func output(ctx context.Context, t *testing.T, bin string) outResult {
	t.Helper()
	return owntest.Within(t, 15*time.Second, func() outResult {
		out, err := NativeHost{}.Output(ctx, Command{Path: bin}, t.TempDir())
		return outResult{out, err}
	})
}

// A cancelled request stops the tool the CLI started, also when the tool holds
// the CLI's output, and returns at once.
func TestOutputCancelStopsDescendants(t *testing.T) {
	for _, hold := range []bool{false, true} {
		name := map[bool]string{false: "own output", true: "holding the output"}[hold]
		t.Run(name, func(t *testing.T) {
			pidFile := owntest.PIDFile(t)
			bin := owntest.Script(t, "cli", owntest.Spawn(pidFile, hold)+"\nwait")
			ctx, cancel := owntest.CancelWhenRecorded(pidFile, 10*time.Second)
			defer cancel()
			res := output(ctx, t, bin)
			if res.err == nil {
				t.Fatal("a cancelled request reported success")
			}
			if child := owntest.PID(t, pidFile); !owntest.Gone(child) {
				t.Fatalf("tool %d survived the cancelled request", child)
			}
		})
	}
}

// A process that escaped the tree but holds the output open cannot keep an
// expired request waiting past the bounded drain.
func TestOutputDeadlineBoundsAnEscapedHolder(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := owntest.Script(t, "cli", owntest.Escape(t, pidFile)+"\nsleep 300")
	const deadline = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	start := time.Now()
	res := output(ctx, t, bin)
	owntest.PID(t, pidFile) // killed at cleanup
	if res.err == nil {
		t.Fatal("an expired request reported success")
	}
	if took := time.Since(start); took > deadline+outputReleaseDelay+3*time.Second {
		t.Fatalf("the request returned only after %s", took)
	}
}

// A CLI that answered and exited is done, even when a process it left behind
// still holds its output: the answer comes back after the bounded drain, and
// the process is left alone, as a daemon a queued run's tool starts is.
func TestOutputAnswerDespiteALeftoverHolder(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := owntest.Script(t, "cli", owntest.Spawn(pidFile, true)+"\necho answer")
	start := time.Now()
	res := output(context.Background(), t, bin)
	if res.err != nil || strings.TrimSpace(string(res.out)) != "answer" {
		t.Fatalf("got %q, %v", res.out, res.err)
	}
	if took := time.Since(start); took > outputReleaseDelay+3*time.Second {
		t.Fatalf("the answer came back only after %s", took)
	}
	if child := owntest.PID(t, pidFile); !owntest.Alive(child) {
		t.Fatal("a process a successful CLI left behind was stopped")
	}
}

// A failing CLI's stderr explains the error, bounded.
func TestOutputReportsStderr(t *testing.T) {
	bin := owntest.Script(t, "cli", "echo out; i=0; while [ $i -lt 2000 ]; do echo 'not logged in' >&2; i=$((i+1)); done; exit 3")
	res := output(context.Background(), t, bin)
	if res.err == nil || !strings.Contains(res.err.Error(), "not logged in") || len(res.err.Error()) > 400 {
		t.Fatalf("error: %v", res.err)
	}
	if strings.TrimSpace(string(res.out)) != "out" {
		t.Fatalf("stdout: %q", res.out)
	}
}

// A CLI that cannot start is reported as such.
func TestOutputStartFailure(t *testing.T) {
	res := output(context.Background(), t, t.TempDir()+"/missing")
	if res.err == nil {
		t.Fatal("a missing CLI reported success")
	}
}

// Many requests, cancelled, expired, answered and failing to start, leave no
// descriptor and no goroutine behind.
func TestOutputLeaksNothing(t *testing.T) {
	answer := owntest.Script(t, "answer", "echo ok")
	hung := owntest.Script(t, "hung", "sleep 300 & wait")
	missing := t.TempDir() + "/missing"
	round := func() {
		_ = output(context.Background(), t, answer)
		_ = output(context.Background(), t, missing)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = output(ctx, t, hung)
		cancel()
	}
	round() // warm up whatever the runtime opens once
	fds, routines := owntest.OpenFDs(t), runtime.NumGoroutine()
	for range 5 {
		round()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (owntest.OpenFDs(t) > fds || runtime.NumGoroutine() > routines) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := owntest.OpenFDs(t); n > fds {
		t.Fatalf("open descriptors grew from %d to %d", fds, n)
	}
	if n := runtime.NumGoroutine(); n > routines {
		t.Fatalf("goroutines grew from %d to %d", routines, n)
	}
}

// A probe owns its tree as well: cancelling it stops what it started.
func TestProbeDeadlineStopsDescendants(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := owntest.Script(t, "cli", owntest.Spawn(pidFile, true)+"\nwait")
	ctx, cancel := owntest.CancelWhenRecorded(pidFile, 10*time.Second)
	defer cancel()
	err := owntest.Within(t, 15*time.Second, func() error {
		_, err := ExecProber{}.Probe(ctx, Command{Path: bin})
		return err
	})
	if err == nil {
		t.Fatal("a cancelled probe reported success")
	}
	if child := owntest.PID(t, pidFile); !owntest.Gone(child) {
		t.Fatalf("tool %d survived the cancelled probe", child)
	}
}
