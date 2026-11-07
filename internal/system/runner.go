// Package system provides a small, injectable wrapper around running external
// commands (pmset, launchctl), so the wake and launchd packages can be tested
// without touching the real system.
package system

import (
	"context"
	"os/exec"
)

// Runner runs an external command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Real runs commands via os/exec, in the background: on Windows a child gets
// no console and starts hidden (see NoWindow).
type Real struct{}

// Run executes name with args and returns combined stdout+stderr.
func (Real) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	NoWindow(cmd)
	return cmd.CombinedOutput()
}

// ShowingWindows returns the runner for commands the user must see.
func (Real) ShowingWindows() Runner { return Interactive{} }

// Interactive runs commands via os/exec that show the user a window, such as a
// native dialog. Like Real it opens no console window on Windows, but it does
// not start the child hidden, so the window it opens can appear.
type Interactive struct{}

// Run executes name with args and returns combined stdout+stderr.
func (Interactive) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	NoConsole(cmd)
	return cmd.CombinedOutput()
}

// ShowingWindows returns the runner for commands that must be able to show the
// user a window, such as a native dialog. A runner without such a variant (a
// test fake) is returned as it is.
func ShowingWindows(r Runner) Runner {
	if s, ok := r.(interface{ ShowingWindows() Runner }); ok {
		return s.ShowingWindows()
	}
	return r
}
