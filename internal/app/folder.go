package app

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/danielmaier42/claudeq/internal/execenv"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// hostGOOS is the system the daemon runs on, a variable for tests.
var hostGOOS = runtime.GOOS

// ErrFolderUnreachable is a task folder none of the task's providers can open.
var ErrFolderUnreachable = errors.New("no provider of this task can open its folder")

// environments lists where a task can run: the provider it names, the
// members of its pool, or the default provider. A script runs on this
// computer.
func environments(cfg store.Config, t task.Task) []provider.Instance {
	if t.IsScript() {
		return []provider.Instance{{ID: "script", Name: "the script"}}
	}
	byID := func(id string) (provider.Instance, bool) {
		for _, p := range cfg.Providers {
			if p.ID == id {
				return provider.InstanceOf(p), true
			}
		}
		return provider.Instance{}, false
	}
	var out []provider.Instance
	switch {
	case t.Pool != "":
		for _, p := range cfg.Pools {
			if p.ID != t.Pool {
				continue
			}
			for _, m := range p.Members {
				if inst, ok := byID(m.Provider); ok {
					out = append(out, inst)
				}
			}
		}
	case t.Provider != "":
		if inst, ok := byID(t.Provider); ok {
			out = append(out, inst)
		}
	default:
		if inst, ok := byID(cfg.Settings.DefaultProvider); ok {
			out = append(out, inst)
		}
	}
	return out
}

// placeFolder checks a task's folder against the environments it can run in,
// before it is stored. On macOS (and for native providers anywhere) every
// folder passes as before. On Windows a Linux path is accepted for a task
// that runs in exactly one WSL distribution and stored as that distribution's
// share (\\wsl.localhost\Ubuntu\home\…), the form Windows can show and open;
// and a folder none of the task's providers can open is refused, so it is not
// discovered at three in the morning.
func placeFolder(cfg store.Config, t *task.Task) error {
	dir := t.WorkingDir
	envs := environments(cfg, *t)
	if dir == "" || len(envs) == 0 {
		return nil
	}
	if hostGOOS == "windows" && execenv.IsLinuxPath(dir) {
		distros := map[string]bool{}
		for _, inst := range envs {
			if inst.Execution.Type == provider.ExecWSL {
				distros[inst.Execution.Distro] = true
			}
		}
		if len(distros) != 1 {
			return fmt.Errorf("%w: %s is a Linux path; give the full path Windows uses, e.g. \\\\wsl.localhost\\Ubuntu\\home\\…", ErrFolderUnreachable, dir)
		}
		for d := range distros {
			dir = execenv.FromLinux(dir, d, "")
		}
	}
	var reasons []error
	for _, inst := range envs {
		err := execenv.CanReach(inst.Execution, dir, hostGOOS)
		if err == nil {
			t.WorkingDir = dir
			return nil
		}
		reasons = append(reasons, fmt.Errorf("%s: %w", inst.Label(), err))
	}
	return fmt.Errorf("%w: %w", ErrFolderUnreachable, errors.Join(reasons...))
}
