//go:build !unix && !windows

package api

import "os/exec"

// detach is a no-op on platforms without sessions; dispatchq targets macOS.
func detach(_ *exec.Cmd) {}
