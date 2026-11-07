//go:build !windows

package system

import "os/exec"

// NoWindow keeps a child from opening a console window. Only Windows opens one;
// everywhere else this does nothing.
func NoWindow(_ *exec.Cmd) {}

// NoConsole keeps a child from opening a console window while letting it show
// its own windows. Only Windows opens a console; everywhere else this does
// nothing.
func NoConsole(_ *exec.Cmd) {}
