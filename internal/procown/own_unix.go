//go:build unix

package procown

import (
	"os/exec"
	"syscall"

	"github.com/danielmaier42/claudeq/internal/proctree"
)

// prepare puts the program in its own process group and, on context
// cancellation (a user cancel, the idle watchdog, a request deadline,
// shutdown), tears down its whole descendant tree.
//
// Killing the program's own process group is not enough. The Codex spike
// measured it: a tool process Codex starts lands in a *different* process group,
// so killing the parent's group left `/bin/sleep` running, reparented to PID 1.
// A cancelled run that leaves work behind is worse than one that never started,
// so the descendants are enumerated and their groups killed leaf-first, then the
// program's own group, and what is left is checked.
func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		proctree.KillTree(cmd.Process.Pid)
		return cmd.Process.Kill()
	}
}

// attach has nothing to do where process groups already own the tree; see
// prepare.
func attach(_ *exec.Cmd) (func(), error) { return func() {}, nil }
