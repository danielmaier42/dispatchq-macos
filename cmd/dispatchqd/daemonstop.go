package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

// This file decides which running processes are the daemon of this
// installation and data directory, so install, uninstall and the installer
// stop those and nothing else. It is system-neutral so its rules run under
// test everywhere; the Windows platform supplies the real processes
// (platform_windows.go).
//
// A process name says nothing about ownership: a second installation, a beta
// in another folder or a daemon for another DISPATCHQ_HOME all run a
// dispatchqd. Neither does the pid in the lock file: it is written after the
// lock is taken, can be stale, and a pid is reused. What identifies the
// daemon of a data directory is that it has the directory's daemon lock file
// open while the lock is held. The operating system reports those processes
// by instance (pid and start time, the Windows Restart Manager's
// RM_UNIQUE_PROCESS). A process is acted on only when the handle opened for it
// shows that same start time, so it is the instance that had the file open,
// and only when it runs one of this installation's daemon executables. No
// clock comparison is involved, so a clock set back cannot make a reused pid
// pass. Old daemons need no new lock format: they keep the file open too.

// processInstance is one process as the operating system identifies it.
type processInstance struct {
	PID     uint32
	Started time.Time
}

// daemonProcess is one process opened for inspection. The handle stays open
// from verification to termination, so the pid cannot pass to another process
// in between.
type daemonProcess interface {
	// Image is the full path of the executable the process runs.
	Image() (string, error)
	// Created is when the process started, as the system recorded it.
	Created() (time.Time, error)
	// Terminate ends the process.
	Terminate() error
	// Wait reports whether the process has exited within d.
	Wait(d time.Duration) bool
	// Close releases the handle.
	Close() error
}

// processOpener opens the process with the given pid.
type processOpener func(pid uint32) (daemonProcess, error)

// daemonStopper stops the daemon that owns one data directory.
type daemonStopper struct {
	// home is the data directory whose daemon is stopped.
	home string
	// self is the calling process, which is never stopped.
	self uint32
	// images are the daemon executables of this installation.
	images []string
	// probe reports whether home's daemon lock is held.
	probe func() (store.DaemonHolder, error)
	// lockUsers lists the processes that have home's daemon lock file open,
	// as the operating system reports them.
	lockUsers func() ([]processInstance, error)
	// open opens a process by pid.
	open processOpener
	// sameFile reports whether two paths name the same executable.
	sameFile func(a, b string) bool
	// sleep waits between checks.
	sleep func(time.Duration)
	// attempts bounds how often the lock is checked before giving up.
	attempts int
	// exitWait is how long a terminated daemon gets to exit.
	exitWait time.Duration
}

// stopRetryInterval is the pause between two checks of the daemon lock.
const stopRetryInterval = 100 * time.Millisecond

// foreignDaemonError means a process that is not one of this installation's
// daemon executables has the data directory's lock file open. Nothing is
// stopped.
type foreignDaemonError struct {
	Home  string
	PID   uint32
	Image string
	Want  []string
}

func (e *foreignDaemonError) Error() string {
	return fmt.Sprintf("the data directory %s is in use by pid %d running %s, which is not this installation's daemon (%s), "+
		"so nothing was stopped; quit that copy of DispatchQ (run its own \"dispatchqd uninstall\", or end pid %d in Task Manager > Details), then try again",
		e.Home, e.PID, e.Image, strings.Join(e.Want, " or "), e.PID)
}

// unverifiedDaemonError means the data directory stayed owned by a process
// whose identity could not be confirmed. That process was not stopped.
type unverifiedDaemonError struct {
	Home    string
	PID     int
	Stopped []uint32
	Reason  error
}

func (e *unverifiedDaemonError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "could not confirm which process owns the data directory %s", e.Home)
	if e.PID > 0 {
		fmt.Fprintf(&b, " (its lock file names pid %d)", e.PID)
	}
	if e.Reason != nil {
		fmt.Fprintf(&b, ": %v", e.Reason)
	}
	b.WriteString("; that process was not stopped")
	if len(e.Stopped) > 0 {
		fmt.Fprintf(&b, " (this installation's daemon, pid %s, was stopped and has exited)", joinPIDs(e.Stopped))
	}
	b.WriteString(". End dispatchqd.exe for this data directory in Task Manager > Details, or sign out and back in, then try again")
	return b.String()
}

func (e *unverifiedDaemonError) Unwrap() error { return e.Reason }

// stop ends the daemon that owns s.home when it is one of s.images, and waits
// until the data directory is free. It returns the pids that were ended and
// have exited. A data directory nobody owns is not an error. A foreign or
// unverifiable owner is left running and reported.
func (s daemonStopper) stop() ([]uint32, error) {
	var stopped []uint32
	var reason error
	lockPID := 0
	for i := 0; i < s.attempts; i++ {
		if i > 0 {
			s.sleep(stopRetryInterval)
		}
		h, err := s.probe()
		if err != nil {
			return stopped, fmt.Errorf("check which daemon owns %s: %w", s.home, err)
		}
		if !h.Held {
			return stopped, nil
		}
		lockPID = h.PID
		procs, err := s.verifyOwners()
		if err != nil {
			var foreign *foreignDaemonError
			if errors.As(err, &foreign) {
				return stopped, err
			}
			// Unconfirmed: a listed process exited or its pid moved on.
			// Look again.
			reason = err
			continue
		}
		err = func() error {
			defer func() {
				for _, p := range procs {
					_ = p.proc.Close()
				}
			}()
			for _, p := range procs {
				terr := p.proc.Terminate()
				exited := p.proc.Wait(s.exitWait)
				switch {
				case exited:
					stopped = append(stopped, p.pid)
				case terr != nil:
					return fmt.Errorf("stop this installation's daemon (pid %d) for %s: %w", p.pid, s.home, terr)
				default:
					reason = fmt.Errorf("pid %d was asked to stop but did not exit within %s", p.pid, s.exitWait)
				}
			}
			return nil
		}()
		if err != nil {
			return stopped, err
		}
	}
	return stopped, &unverifiedDaemonError{Home: s.home, PID: lockPID, Stopped: stopped, Reason: reason}
}

// openDaemon is a verified process of this installation, opened.
type openDaemon struct {
	pid  uint32
	proc daemonProcess
}

// verifyOwners opens every process that has the lock file open and confirms
// each is this installation's daemon, the same instance the system listed.
// On success the caller owns the returned handles; on error all are closed.
func (s daemonStopper) verifyOwners() ([]openDaemon, error) {
	users, err := s.lockUsers()
	if err != nil {
		return nil, fmt.Errorf("list the processes using the lock file: %w", err)
	}
	var procs []openDaemon
	fail := func(err error) ([]openDaemon, error) {
		for _, p := range procs {
			_ = p.proc.Close()
		}
		return nil, err
	}
	for _, u := range users {
		if u.PID == s.self {
			continue
		}
		p, err := s.open(u.PID)
		if err != nil {
			return fail(fmt.Errorf("open pid %d: %w", u.PID, err))
		}
		procs = append(procs, openDaemon{pid: u.PID, proc: p})
		created, err := p.Created()
		if err != nil {
			return fail(fmt.Errorf("read the start time of pid %d: %w", u.PID, err))
		}
		if !created.Equal(u.Started) {
			return fail(fmt.Errorf("pid %d is no longer the process that had the lock file open", u.PID))
		}
		image, err := p.Image()
		if err != nil {
			return fail(fmt.Errorf("read the executable of pid %d: %w", u.PID, err))
		}
		if !s.ours(image) {
			return fail(&foreignDaemonError{Home: s.home, PID: u.PID, Image: image, Want: s.images})
		}
	}
	if len(procs) == 0 {
		return nil, errors.New("the system reports no process using the lock file")
	}
	return procs, nil
}

func (s daemonStopper) ours(image string) bool {
	for _, want := range s.images {
		if s.sameFile(image, want) {
			return true
		}
	}
	return false
}

// stopInstallationProcesses ends the processes among candidates that run one
// of images, and leaves every other one alone. Candidates are pids
// preselected by executable name, which is never enough on its own; a process
// that cannot be opened or examined is not acted on. It returns the pids that
// were ended and have exited, and the errors of those that were not.
func stopInstallationProcesses(candidates []uint32, self uint32, images []string, open processOpener,
	sameFile func(a, b string) bool, exitWait time.Duration) ([]uint32, error) {
	var stopped []uint32
	var errs []error
	for _, pid := range candidates {
		if pid == self {
			continue
		}
		p, err := open(pid)
		if err != nil {
			continue // gone, or not ours to open
		}
		path, err := p.Image()
		ours := false
		for _, image := range images {
			if err == nil && sameFile(path, image) {
				ours = true
			}
		}
		if !ours {
			_ = p.Close()
			continue
		}
		terr := p.Terminate()
		exited := p.Wait(exitWait)
		_ = p.Close()
		switch {
		case exited:
			stopped = append(stopped, pid)
		case terr != nil:
			errs = append(errs, fmt.Errorf("stop %s (pid %d): %w", path, pid, terr))
		default:
			errs = append(errs, fmt.Errorf("%s (pid %d) was asked to stop but did not exit within %s", path, pid, exitWait))
		}
	}
	return stopped, errors.Join(errs...)
}

// uninstallInOrder stops this installation's processes and only then removes
// the autostart entry, so a refusal leaves the installation as it was.
func uninstallInOrder(stopWindows func() error, stopDaemon func() ([]uint32, error),
	removeAutostart func() error, entry, home string, out io.Writer) error {
	if err := stopWindows(); err != nil {
		return err
	}
	stopped, err := stopDaemon()
	if err != nil {
		return err
	}
	if err := removeAutostart(); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed the %s autostart entry\n", entry)
	if len(stopped) == 0 {
		fmt.Fprintf(out, "no daemon was running for %s\n", home)
	} else {
		fmt.Fprintf(out, "stopped the daemon for %s (pid %s)\n", home, joinPIDs(stopped))
	}
	return nil
}

// sameExecutable reports whether a and b are the same existing file. A path
// that cannot be examined matches nothing.
func sameExecutable(a, b string) bool {
	ia, err := os.Stat(a)
	if err != nil {
		return false
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

func joinPIDs(pids []uint32) string {
	s := make([]string, len(pids))
	for i, p := range pids {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ", ")
}
