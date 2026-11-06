//go:build !windows

package system

import "os/exec"

// NoWindow keeps a child from opening a console window. Only Windows opens one;
// everywhere else this does nothing.
func NoWindow(_ *exec.Cmd) {}
