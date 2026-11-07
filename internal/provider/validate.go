package provider

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidProvider is the base error for a provider configuration that
// dispatchq refuses to store. It is the first of the three check levels: a
// configuration that fails here is rejected outright, where a *readiness*
// problem (see [Health]) only keeps the instance from running, and a runtime
// problem is recorded on the run.
var ErrInvalidProvider = errors.New("invalid provider")

// CheckID reports whether id is usable as a provider id. Ids appear in API
// paths, task fields and CLI arguments, so they are restricted the same way a
// task id is: letters, digits, dot, dash and underscore, starting with a letter
// or digit.
func CheckID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: missing id", ErrInvalidProvider)
	}
	for i, r := range id {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			i > 0 && (r == '.' || r == '-' || r == '_')
		if !ok {
			return fmt.Errorf("%w: id %q may only contain letters, digits, '.', '-' and '_' (and must start with a letter or digit)",
				ErrInvalidProvider, id)
		}
	}
	return nil
}

// ExpandHome resolves a leading "~" to the current user's home directory, so a
// path field accepts what people actually type. Only the plain forms are
// expanded ("~" and "~/…"); "~someone/…" names another user's home, which
// dispatchq does not resolve and which the absolute-path check then rejects by
// name. An unresolvable home leaves the path untouched, for the same check to
// refuse.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Validate checks one instance's configuration against the registry: the id is
// usable, the kind is implemented, and the two paths — if given at all — are
// absolute. Whether those paths exist is a readiness question, answered by the
// health check, so a provider configured ahead of installing its CLI can still
// be saved.
func Validate(reg *Registry, inst Instance) error {
	if err := CheckID(inst.ID); err != nil {
		return err
	}
	if inst.Kind == "" {
		return fmt.Errorf("%w %q: missing kind", ErrInvalidProvider, inst.ID)
	}
	if _, err := reg.Lookup(inst.Kind); err != nil {
		return fmt.Errorf("%w %q: %w (known kinds: %s)", ErrInvalidProvider, inst.ID, err, kindList(reg))
	}
	if err := CheckExecution(reg, inst.Execution); err != nil {
		return fmt.Errorf("%w %q: %w", ErrInvalidProvider, inst.ID, err)
	}
	isAbs := filepath.IsAbs
	if !inst.Execution.IsNative() {
		// The paths of an instance that runs in Linux are Linux paths, whatever
		// the daemon's own system calls absolute.
		isAbs = isLinuxAbs
	}
	if p := inst.BinaryPath; p != "" && !isAbs(p) {
		return fmt.Errorf("%w %q: binary path %q must be absolute", ErrInvalidProvider, inst.ID, p)
	}
	if d := inst.ConfigDir; d != "" && !isAbs(d) {
		return fmt.Errorf("%w %q: configuration directory %q must be absolute", ErrInvalidProvider, inst.ID, d)
	}
	if err := checkEnv(inst.Env); err != nil {
		return fmt.Errorf("provider %q: %w", inst.ID, err)
	}
	return nil
}

// kindList names the registered kinds for an error message.
func kindList(reg *Registry) string {
	kinds := reg.Kinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// isLinuxAbs reports whether p is an absolute Linux path without a control
// character in it.
func isLinuxAbs(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}
