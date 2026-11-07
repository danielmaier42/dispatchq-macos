//go:build windows

package binpath

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// CommandContext is exec.CommandContext for a provider CLI. A .cmd or .bat
// shim cannot be started by CreateProcess directly, and cmd.exe would
// reinterpret the arguments, so such a shim is resolved to the program it
// starts, or run through cmd.exe only when every argument is inert. See
// planBatch.
//
// The returned command has no SysProcAttr other than a raw command line when
// cmd.exe is needed; callers add their creation flags to it afterwards.
func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".cmd" && ext != ".bat" {
		return exec.CommandContext(ctx, path, args...), nil //nolint:gosec // a configured provider binary
	}
	shim, err := readShim(path)
	if err != nil {
		return nil, fmt.Errorf("read provider launcher %s: %w", path, err)
	}
	plan, err := planBatch(path, args, shim, batchEnv{
		Exists: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && !fi.IsDir()
		},
		LookPath: exec.LookPath,
		ComSpec:  os.Getenv("ComSpec"),
	})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, plan.Name, plan.Args...) //nolint:gosec // resolved from a configured provider launcher
	if plan.CmdLine != "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: plan.CmdLine}
	}
	return cmd, nil
}

// maxShimSize bounds how much of a shim is read; real ones are under 2 KiB.
const maxShimSize = 256 << 10

func readShim(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxShimSize))
}
