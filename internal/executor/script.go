package executor

// Script jobs: the other half of what the queue runs. An agent job hands its
// prompt to a harness; a script job runs it as a program, with no model, no
// provider and no allowance to spend. The two share everything around the run
// — the log, the idle watchdog, the process group, the environment that lets a
// run queue follow-up work — and differ only in what is started.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// defaultShell runs a script that does not name its own interpreter.
const defaultShell = "/bin/sh"

// RunsScripts reports whether this build can run a script job on goos (a
// runtime.GOOS value). runScript needs /bin/sh or a #! line, which a native
// Windows system has neither of, so there it reports false until a Windows
// script runner exists. The assistant reads this instead of assuming.
func RunsScripts(goos string) bool { return goos != "windows" }

// runScript executes a script task and classifies the outcome by its exit
// status: zero is a success, anything else a failure. There is nothing to parse
// — a script reports through its exit code, so dispatchq does not guess at its
// output the way it has to with a harness's event stream.
//
// The script is written to a temporary file rather than passed on a command
// line, so a shebang decides the interpreter (`#!/usr/bin/env python3` works as
// it would anywhere else) and nothing in the text is ever re-parsed by a shell.
func (e *Executor) runScript(ctx context.Context, req Request) (provider.Result, error) {
	dir, err := os.MkdirTemp("", "dispatchq-script")
	if err != nil {
		return provider.Result{}, fmt.Errorf("prepare script: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	src := req.Task.Prompt
	path := filepath.Join(dir, "job")
	if err := os.WriteFile(path, []byte(src), 0o700); err != nil { //nolint:gosec // the script must be executable to honour its own shebang
		return provider.Result{}, fmt.Errorf("write script: %w", err)
	}
	name, args := defaultShell, []string{path}
	if strings.HasPrefix(src, "#!") {
		name, args = path, nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...) //nolint:gosec // the script is the operator's own job definition
	cmd.Dir = req.Task.WorkingDir
	cmd.Env = e.runEnv(req, nil) // lets the script queue agent jobs (self-queue)
	configureProcessGroup(cmd)   // so a killed run takes its whole process tree with it
	// What the jobs it waited for answered arrives on stdin, where a script can
	// read it. Without dependencies that is the empty string, which closes stdin
	// immediately — an unattended run must never sit waiting for input.
	cmd.Stdin = strings.NewReader(req.Stdin)

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	log := &syncWriter{w: req.Log}
	live := &activityWriter{w: log, last: &lastActivity}
	// Both streams go to the run log; only stdout is kept as the job's answer,
	// so a script that logs its progress to stderr does not turn it into the
	// result a notification quotes or a dependent job consolidates.
	answer := &tailBuffer{max: store.MaxFinalOutput - len(scriptOutputCut)}
	cmd.Stdout = io.MultiWriter(live, answer)
	cmd.Stderr = live
	cmd.WaitDelay = pipeReleaseDelay // a daemon the script left behind must not hold the run open

	if err := cmd.Start(); err != nil {
		return provider.Result{}, fmt.Errorf("start script: %w", err)
	}

	var idleKilled atomic.Bool
	if req.IdleTimeout > 0 {
		done := make(chan struct{})
		defer close(done)
		go idleWatch(req.IdleTimeout, &lastActivity, &idleKilled, cancel, done)
	}

	exitCode, orphaned, err := waitCode(cmd, &idleKilled)
	if err != nil {
		return provider.Result{}, err
	}
	if orphaned {
		noteOrphanedOutput(log)
	}
	// A script that exited on its own was not hung, even if the watchdog fired
	// while Wait gave a held pipe its grace period.
	if idleKilled.Load() && !orphaned {
		return provider.Result{
			Status:   store.StatusFailed,
			ExitCode: exitCode,
			Message:  fmt.Sprintf("stopped after %s of no output (looked hung)", req.IdleTimeout),
		}, nil
	}

	res := provider.Result{Status: store.StatusSuccess, ExitCode: exitCode, FinalOutput: answer.answer()}
	if exitCode != 0 {
		res.Status = store.StatusFailed
		res.Message = fmt.Sprintf("the script exited with code %d", exitCode)
	}
	return res, nil
}

// waitCode waits for the process and reports its exit status, and whether a
// process the script started kept the output open after it exited. A failure
// that is not the process exiting non-zero is a dispatchq-side error, except
// when the watchdog killed the run — that kill is the answer, not a broken run.
func waitCode(cmd *exec.Cmd, idleKilled *atomic.Bool) (code int, orphaned bool, err error) {
	err = cmd.Wait()
	if err == nil {
		return 0, false, nil
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return 0, true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), false, nil
	}
	if idleKilled.Load() {
		return -1, false, nil
	}
	return 0, false, fmt.Errorf("wait for the script: %w", err)
}

// activityWriter notes the time of every write, which is what tells the idle
// watchdog that the run is still alive.
type activityWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.last.Store(time.Now().UnixNano())
	return a.w.Write(p)
}

// scriptOutputCut opens a script's answer when there was more output than a run
// record may carry. Whoever reads the answer — a notification, a dependent job's
// context block — has to be told that what they see is the end of it and not all
// of it; the record's own truncation flag cannot say that, because it describes
// a text cut at the other end.
const scriptOutputCut = "[only the end of this script's output is kept; the complete output is in the run's log]\n"

// tailBuffer keeps the last max bytes written to it. A script's answer is what
// it says last — a summary line, a count, an id — so when the output is longer
// than a run record may carry, the end is the part worth keeping.
type tailBuffer struct {
	max int
	buf []byte
	cut bool // something was dropped off the front
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
		t.cut = true
	}
	return len(p), nil
}

// answer is what the run reports as the job's result: the kept output, said to
// be only the end of it when it is.
func (t *tailBuffer) answer() string {
	if !t.cut {
		return t.String()
	}
	kept := t.String()
	// Start at a line boundary, so the answer does not open mid-word.
	if _, rest, ok := strings.Cut(kept, "\n"); ok {
		kept = rest
	}
	return scriptOutputCut + kept
}

// String is the kept output, with a half character at the cut dropped so the
// result is always valid UTF-8.
func (t *tailBuffer) String() string {
	b := t.buf
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return string(b)
}
