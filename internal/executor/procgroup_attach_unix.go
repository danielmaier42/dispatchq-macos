//go:build !windows

package executor

import "os/exec"

// attachProcessTree has nothing to do where process groups already own the
// tree; see configureProcessGroup.
func attachProcessTree(_ *exec.Cmd) func() { return func() {} }
