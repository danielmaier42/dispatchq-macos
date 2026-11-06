//go:build windows

package system

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// NoWindow keeps a child from opening a console window. The daemon and the app
// are GUI-subsystem programs with no console of their own, so without this
// every console tool they start (a provider's --version check, PowerShell,
// taskkill) would flash a window on the user's desktop.
func NoWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
}
