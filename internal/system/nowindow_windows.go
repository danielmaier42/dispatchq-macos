//go:build windows

package system

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow keeps a child from opening a console window and starts it hidden.
// The daemon and the app are GUI-subsystem programs with no console of their
// own, so without this every console tool they start (a provider's --version
// check, taskkill) would flash a window on the user's desktop.
func NoWindow(cmd *exec.Cmd) {
	NoConsole(cmd)
	cmd.SysProcAttr.HideWindow = true
}

// NoConsole keeps a child from opening a console window but leaves the windows
// it opens itself alone. HideWindow is not enough for a child that must show
// a window: it starts the child with SW_HIDE, which Windows applies to the
// first window the child shows, whatever that window asks for. A native dialog
// shown by PowerShell would open invisible and wait for an answer forever.
func NoConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
