//go:build !unix && !windows

package executor

import "os/exec"

// configureProcessGroup is a no-op on platforms with neither POSIX process
// groups nor Job Objects.
func configureProcessGroup(_ *exec.Cmd) {}
