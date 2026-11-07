//go:build unix

package executor

import (
	"os/exec"
	"syscall"

	"github.com/danielmaier42/claudeq/internal/proctree"
)

// configureProcessGroup puts the CLI in its own process group and, on context
// cancellation (a user cancel, the idle watchdog, shutdown), tears down its
// whole descendant tree.
//
// Killing the harness's own process group is not enough. The Codex spike
// measured it: a tool process Codex starts lands in a *different* process group,
// so killing the parent's group left `/bin/sleep` running, reparented to PID 1.
// A cancelled run that leaves work behind is worse than one that never started,
// so the descendants are enumerated and their groups killed leaf-first, then the
// harness's own group, and what is left is checked.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		proctree.KillTree(cmd.Process.Pid)
		return cmd.Process.Kill()
	}
}
