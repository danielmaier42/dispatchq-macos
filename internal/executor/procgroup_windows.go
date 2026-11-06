//go:build windows

package executor

import (
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/danielmaier42/claudeq/internal/system"
	"golang.org/x/sys/windows"
)

// jobs maps a started command to the Job Object that owns its process tree.
var jobs sync.Map // *exec.Cmd -> windows.Handle

// configureProcessGroup gives the CLI its own process group without a console
// window and, on context cancellation (a user cancel, the idle watchdog,
// shutdown), tears down its whole descendant tree.
//
// Windows has no process groups to signal, so the tree is owned through a Job
// Object (see attachProcessTree). `taskkill /T` backs it up for the window
// between Start and the assignment, and for a job the OS refused.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killTree(cmd)
		return cmd.Process.Kill()
	}
}

// attachProcessTree puts the started process into a new Job Object that kills
// every member when its last handle closes, so a daemon that dies takes its
// runs with it. The returned release detaches that kill-on-close behaviour and
// closes the handle: a run that finished on its own leaves nothing behind to
// kill, and a daemon a tool left running on purpose stays running, as on macOS.
func attachProcessTree(cmd *exec.Cmd) (release func()) {
	noop := func() {}
	if cmd.Process == nil {
		return noop
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return noop
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return noop
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return noop
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return noop
	}
	jobs.Store(cmd, job)
	return func() {
		jobs.Delete(cmd)
		info.BasicLimitInformation.LimitFlags = 0
		_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		_ = windows.CloseHandle(job)
	}
}

// killTree terminates the process and everything it started.
func killTree(cmd *exec.Cmd) {
	if v, ok := jobs.Load(cmd); ok {
		_ = windows.TerminateJobObject(v.(windows.Handle), 1)
	}
	// Covers a process that was not (yet) in the job. /T walks the parent chain
	// while the parent is alive, so it runs before the root is killed.
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	system.NoWindow(kill)
	_ = kill.Run()
}
