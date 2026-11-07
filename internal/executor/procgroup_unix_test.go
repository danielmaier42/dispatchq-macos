//go:build unix

package executor

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

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

func TestTerminalRateLimitStopsHarnessAndChildren(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	pidFile := filepath.Join(dir, "child.pid")
	body := `#!/bin/sh
sleep 300 &
echo $! > "` + pidFile + `"
printf '%s\n' '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791544800}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"session_id":"resume-session","result":"Session limit","total_cost_usd":4.15,"num_turns":48}'
wait
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := claudeExecutor().Run(ctx, Request{
		Task: sampleTask(), Provider: claudeInstance(bin), SessionID: "initial-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("terminal limit waited for the caller's timeout")
	}
	if res.Status != store.StatusRateLimited || res.SessionID != "resume-session" || !res.ResetAt.Equal(time.Unix(1791544800, 0)) {
		t.Fatalf("lost resumable limit result: %+v", res)
	}
	if res.Metrics == nil || res.Metrics.NumTurns != 48 || res.Metrics.CostUSD != 4.15 || res.FinalOutput != "Session limit" {
		t.Fatalf("lost final result evidence: %+v", res)
	}
	child := waitForPID(t, pidFile)
	if alive(child) {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Fatalf("tool child %d survived terminal rate limit", child)
	}
}

// escapedHarness writes a stand-in harness that leaves a daemon behind: a
// process that forked twice into a session of its own, was reparented to PID 1
// and so is no descendant any more, but still holds the run's stdout and
// stderr. Build servers and file watchers a tool call starts do exactly that.
// After the daemon is in place the harness runs tail, its last shell line.
func escapedHarness(t *testing.T, tail string) (path string, daemonPID func() int) {
	t.Helper()
	old := pipeReleaseDelay
	pipeReleaseDelay = 200 * time.Millisecond
	t.Cleanup(func() { pipeReleaseDelay = old })
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("no perl here to daemonize a child")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "daemon.pid")
	path = filepath.Join(dir, "claude")
	body := "#!/bin/sh\n" +
		perl + ` -e 'use POSIX; exit if fork; POSIX::setsid(); exit if fork; open(my $f, ">", "` + pidFile + `.tmp"); print $f $$; close($f); rename("` + pidFile + `.tmp", "` + pidFile + `"); sleep 300'` + "\n" +
		"while [ ! -s " + pidFile + " ]; do sleep 0.05; done\n" +
		tail + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write harness: %v", err)
	}
	return path, func() int {
		pid := waitForPID(t, pidFile)
		t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
		return pid
	}
}

// TestRunCancelReturnsDespiteAnEscapedDaemon is the run that stayed "running"
// after Cancel task: the harness was killed, but a daemon it had left behind
// kept the output pipe open, so the run never ended and was never recorded.
func TestRunCancelReturnsDespiteAnEscapedDaemon(t *testing.T) {
	path, daemonPID := escapedHarness(t, "sleep 300")
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := claudeExecutor().Run(ctx, Request{Task: tk, Provider: claudeInstance(path), SessionID: "sid", Log: io.Discard})
		done <- err
	}()
	daemonPID()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(pipeReleaseDelay + 10*time.Second):
		t.Fatal("Run never returned after the cancel")
	}
}

// TestRunFinishesDespiteAnEscapedDaemon: a harness that ends on its own must end
// the run too, even with a daemon of its still holding the output.
func TestRunFinishesDespiteAnEscapedDaemon(t *testing.T) {
	path, daemonPID := escapedHarness(t, `echo '{"type":"result","is_error":false,"result":"ok","session_id":"s"}'`)
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	type outcome struct {
		res provider.Result
		err error
	}
	var log bytes.Buffer
	done := make(chan outcome, 1)
	go func() {
		res, err := claudeExecutor().Run(context.Background(), Request{Task: tk, Provider: claudeInstance(path), SessionID: "sid", Log: &log})
		done <- outcome{res, err}
	}()
	daemonPID()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("Run: %v", o.err)
		}
		if o.res.Status != store.StatusSuccess {
			t.Fatalf("status = %q, want success", o.res.Status)
		}
		if !strings.Contains(log.String(), "kept its output open") {
			t.Fatalf("log = %q, want a note about the output left open", log.String())
		}
	case <-time.After(pipeReleaseDelay + 10*time.Second):
		t.Fatal("Run never returned after the harness exited")
	}
}

// TestScriptCancelReturnsDespiteAnEscapedDaemon: the same for a script job.
func TestScriptCancelReturnsDespiteAnEscapedDaemon(t *testing.T) {
	path, daemonPID := escapedHarness(t, "sleep 300")
	src, err := os.ReadFile(path) //nolint:gosec // the test's own stand-in
	if err != nil {
		t.Fatal(err)
	}
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	tk.Kind = task.KindScript
	tk.Prompt = string(src)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&Executor{}).Run(ctx, Request{Task: tk, Log: io.Discard})
		done <- err
	}()
	daemonPID()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(pipeReleaseDelay + 10*time.Second):
		t.Fatal("the script run never returned after the cancel")
	}
}
