//go:build !windows

package binpath

import (
	"context"
	"os/exec"
)

// CommandContext is exec.CommandContext for a provider CLI. Only Windows has
// launchers that need translating (see command_windows.go).
func CommandContext(ctx context.Context, path string, args ...string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, path, args...), nil //nolint:gosec // a configured provider binary
}
