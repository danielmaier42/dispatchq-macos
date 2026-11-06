//go:build windows

package api

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach starts the command outside this process's console and process group,
// so it outlives this process: the hand-over it performs stops this very daemon.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}
