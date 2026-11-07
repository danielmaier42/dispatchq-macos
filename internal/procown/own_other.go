//go:build !unix && !windows

package procown

import "os/exec"

// prepare is a no-op on platforms with neither POSIX process groups nor Job
// Objects: cancellation stops the program itself, as exec does by default.
func prepare(_ *exec.Cmd) {}

// attach has nothing to own on such platforms.
func attach(_ *exec.Cmd) (func(), error) { return func() {}, nil }
