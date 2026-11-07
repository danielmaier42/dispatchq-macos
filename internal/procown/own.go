// Package procown starts a program DispatchQ runs as the owner of its whole
// process tree: a cancel or deadline ends the program and every process it
// started, not only the program itself.
//
// It is the one place that knows how that ownership is established on each
// system: a process group plus a descendant walk on Unix (see proctree), a Job
// Object on Windows. Every caller that starts a provider program goes through
// [Start]: queued runs and scripts, the assistant and prompt-review asides,
// health probes and the Codex app server. A tool a provider starts must not
// outlive a cancelled request, and must not hold its output open either.
//
// What happens after a program ends on its own is the caller's choice and
// unchanged by ownership: the release function [Start] returns gives up the
// tree without stopping it, so a daemon a tool left running on purpose keeps
// running.
package procown

import "os/exec"

// Start starts cmd as the owner of its whole process tree.
//
// cmd must come from exec.CommandContext (exec refuses a cancel hook without
// a context) and must not have been started. Start replaces cmd.Cancel with a
// hard stop of the whole tree; interrupt, when set, runs ahead of that stop,
// for an
// environment that has to stop a tree of its own first (a WSL run's Linux
// processes, which a Windows Job Object cannot see).
//
// On success the program is running and owned; the caller waits for it with
// cmd.Wait and calls release afterwards. On failure no process is left behind:
// a program that started but could not be owned is stopped and reaped before
// Start returns. A command is therefore never left created but not running,
// which matters on Windows, where it is created suspended until it is owned.
func Start(cmd *exec.Cmd, interrupt func()) (release func(), err error) {
	prepare(cmd)
	if interrupt != nil {
		hard := cmd.Cancel
		cmd.Cancel = func() error {
			interrupt()
			return hard()
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	release, err = attach(cmd)
	if err != nil {
		abort(cmd)
		return nil, err
	}
	return release, nil
}

// abort stops a command that started but cannot be run under the ownership
// Start guarantees, and reaps it. A program is never left going without its
// process tree being owned.
func abort(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}
