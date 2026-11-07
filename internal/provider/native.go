package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

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
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, shortenText(string(ee.Stderr), 300))
		}
		return out, err
	}
	return out, nil
}

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
