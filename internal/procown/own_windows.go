//go:build windows

package procown

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobs maps a started command to the Job Object that owns its process tree.
var jobs sync.Map // *exec.Cmd -> windows.Handle

// prepare gives the program its own process group without a console window
// and, on context cancellation (a user cancel, the idle watchdog, a request
// deadline, shutdown), tears down its whole descendant tree.
//
// Windows has no process groups to signal, so the tree is owned through a Job
// Object. A process joins a job only at the moment it is assigned, never its
// earlier children, so the process is created suspended: its first instruction
// has not run when attach puts it into the job and resumes it, and every
// descendant it ever starts is a member. Only Start calls prepare, and Start
// always attaches (and so resumes) or stops the program, so a command is never
// left suspended.
func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killTree(cmd)
		return cmd.Process.Kill()
	}
}

// attach puts the started, still suspended process into a new Job Object
// that kills every member when its last handle closes, so a daemon that dies
// takes its programs with it, and then lets it run. If ownership cannot be
// established the process is not resumed and an error is returned; the caller
// must then stop it (abort).
//
// The returned release detaches that kill-on-close behaviour and closes the
// handle: a program that finished on its own leaves nothing behind to kill,
// and a daemon a tool left running on purpose stays running, as on macOS.
func attach(cmd *exec.Cmd) (release func(), err error) {
	if cmd.Process == nil {
		return nil, errors.New("process tree: the command has not started")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open process %d: %w", cmd.Process.Pid, err)
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process %d to its job object: %w", cmd.Process.Pid, err)
	}
	jobs.Store(cmd, job)
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		jobs.Delete(cmd)
		_ = windows.CloseHandle(job) // kill-on-close ends the member
		return nil, err
	}
	return func() {
		jobs.Delete(cmd)
		info.BasicLimitInformation.LimitFlags = 0
		_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
		_ = windows.CloseHandle(job)
	}, nil
}

// resumeProcess resumes the threads of a process created suspended. exec has
// already closed the primary thread's handle, so the thread is found through a
// snapshot. The process has not run yet, so it has exactly one.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads of process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snap, &entry); err != nil {
		return fmt.Errorf("list threads of process %d: %w", pid, err)
	}
	resumed := 0
	for {
		if entry.OwnerProcessID == pid {
			th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return fmt.Errorf("open thread %d of process %d: %w", entry.ThreadID, pid, err)
			}
			_, err = windows.ResumeThread(th)
			_ = windows.CloseHandle(th)
			if err != nil {
				return fmt.Errorf("resume thread %d of process %d: %w", entry.ThreadID, pid, err)
			}
			resumed++
		}
		if err := windows.Thread32Next(snap, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return fmt.Errorf("list threads of process %d: %w", pid, err)
		}
	}
	if resumed == 0 {
		return fmt.Errorf("process %d has no thread to resume", pid)
	}
	return nil
}

// killTree terminates the process and everything it started. Every descendant
// is a member of the job from the first instruction, so ending the job ends the
// tree at once, with no window in which a child could be missed.
func killTree(cmd *exec.Cmd) {
	if v, ok := jobs.Load(cmd); ok {
		_ = windows.TerminateJobObject(v.(windows.Handle), 1)
	}
}
