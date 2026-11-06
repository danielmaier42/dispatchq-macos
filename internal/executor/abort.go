package executor

import "os/exec"

// abortStarted stops a command that started but cannot be run under the
// ownership the executor guarantees, and reaps it. A run is never left going
// without its process tree being owned.
func abortStarted(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}
