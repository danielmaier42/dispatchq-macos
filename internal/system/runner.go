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

// Real runs commands via os/exec.
type Real struct{}

// Run executes name with args and returns combined stdout+stderr.
func (Real) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	NoWindow(cmd)
	return cmd.CombinedOutput()
}
