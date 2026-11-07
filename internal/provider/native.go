package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider/binpath"
	"github.com/danielmaier42/claudeq/internal/system"
)

// NativeHost is the daemon's own operating system: exactly the process and
// file handling DispatchQ had before execution environments existed. It never
// starts wsl.exe.
type NativeHost struct {
	// Prober replaces the probe process, for tests. Nil runs [ExecProber].
	Prober Prober
}

var _ Host = NativeHost{}

// Probe implements Prober.
func (h NativeHost) Probe(ctx context.Context, c Command) ([]byte, error) {
	if h.Prober != nil {
		return h.Prober.Probe(ctx, c)
	}
	return ExecProber{}.Probe(ctx, c)
}

// Execution implements Host.
func (NativeHost) Execution() Execution { return Execution{} }

// StatDir implements Host.
func (NativeHost) StatDir(_ context.Context, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return ErrNotDirectory
	}
	return nil
}

// Executable implements Host with the platform rule in binpath.
func (NativeHost) Executable(_ context.Context, path string) error {
	return binpath.CheckExecutable(path)
}

// ReadFile implements Host.
func (NativeHost) ReadFile(_ context.Context, path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the instance's own configuration directory
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

// WorkDir implements Host: a native run uses the stored directory as it is.
func (NativeHost) WorkDir(_ context.Context, stored string) (string, error) { return stored, nil }

// Output implements Host. It returns stdout only, so a warning the CLI writes
// to stderr can never end up inside the JSON that gets parsed.
//
// The CLI runs as the owner of its whole process tree: a cancelled or expired
// request stops every tool it started, and a process that escaped the tree
// but still holds the output open cannot keep the request waiting past
// outputReleaseDelay.
func (NativeHost) Output(ctx context.Context, cmd Command, dir string) ([]byte, error) {
	c, err := binpath.CommandContext(ctx, cmd.Path, cmd.Args...) // the adapter built this from the configured provider
	if err != nil {
		return nil, err
	}
	system.NoWindow(c)
	c.Dir = dir
	if len(cmd.Env) > 0 {
		c.Env = append(nativeEnviron(), cmd.Env...)
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}
	var stdout bytes.Buffer
	stderr := &headBuffer{max: stderrKeep}
	c.Stdout = &stdout
	c.Stderr = stderr
	err = runOwned(ctx, c)
	out := stdout.Bytes()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && stderr.buf.Len() > 0 {
			return out, fmt.Errorf("%w: %s", err, shortenText(stderr.String(), 300))
		}
		return out, err
	}
	return out, nil
}

// outputReleaseDelay bounds how long a finished or stopped program's output is
// still drained when a process outside its owned tree holds the pipe open.
const outputReleaseDelay = time.Second

// stderrKeep is how much of a failing CLI's stderr is kept for its error.
const stderrKeep = 4 << 10

// runOwned runs c, prepared but not started, as the owner of its whole
// process tree and waits for it.
//
// A program that exited successfully while a process it left behind still
// held its output open has given its answer; giving up on that pipe after
// outputReleaseDelay is not a failure. That process is left running, as a
// daemon a tool starts on purpose in a queued run is.
//
// A program stopped because ctx ended reports that, even when it managed to
// exit successfully while its tree was being stopped (a shell's wait returns
// 0 once its children are killed).
func runOwned(ctx context.Context, c *exec.Cmd) error {
	c.WaitDelay = outputReleaseDelay
	release, err := (&Process{Cmd: c}).Start()
	if err != nil {
		return err
	}
	defer release()
	err = c.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && c.ProcessState != nil && c.ProcessState.Success() {
		err = nil
	}
	if err == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// headBuffer keeps the first max bytes written to it and discards the rest,
// so a CLI that floods stderr cannot grow the daemon's memory. The buffer is a
// field, not embedded, so exec's copying cannot reach bytes.Buffer.ReadFrom
// and bypass the cap.
type headBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *headBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		_, _ = b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *headBuffer) String() string { return b.buf.String() }

// Command implements Host.
//
// A run gets the daemon's environment, DispatchQ's run variables (repeated
// under their legacy names) and the adapter's additions, in that order, so a
// later entry wins. A streaming conversation gets the daemon's environment
// plus the adapter's additions and an open stdin.
func (NativeHost) Command(ctx context.Context, c Command, o CmdOptions) (*Process, error) {
	cmd, err := binpath.CommandContext(ctx, c.Path, c.Args...) // a configured binary path, no shell
	if err != nil {
		return nil, err
	}
	if o.Stream {
		system.NoWindow(cmd)
		if len(c.Env) > 0 {
			cmd.Env = append(cmd.Environ(), c.Env...)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin: %w", err)
		}
		return &Process{Cmd: cmd, Stdin: stdin}, nil
	}
	cmd.Dir = o.Dir
	cmd.Env = append(WithLegacyNames(append(nativeEnviron(), o.Vars...)), c.Env...)
	// A harness that takes its prompt on stdin gets it here. One that does not
	// sees stdin closed immediately, which is what it wants: a headless run must
	// never sit waiting for input nobody will type.
	cmd.Stdin = strings.NewReader(c.Stdin)
	return &Process{Cmd: cmd}, nil
}

func shortenText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
