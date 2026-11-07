//go:build unix

package wslhelper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/danielmaier42/claudeq/internal/proctree"
	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// The registry records which process trees a helper owns, so the next daemon
// start can stop a tree whose helper was killed outright (and so could not
// stop it itself). It lives in a directory private to the Linux user.
//
// A record is acted on only when all of this holds: it belongs to the same
// DispatchQ installation (Owner, derived from its data folder); the helper
// that wrote it is provably gone (its pid is free or now a different process,
// told apart by start time); and the program it started is provably the same
// process (pid and start time both match). Anything else is left alone: a
// live helper still enforces its own lease, and a reused pid is someone
// else's process.

type record struct {
	Owner       string    `json:"owner"`
	HelperPID   int       `json:"helper_pid"`
	HelperStart string    `json:"helper_start"`
	PID         int       `json:"pid"`
	Start       string    `json:"start"`
	Created     time.Time `json:"created"`
}

// registryDir returns the private record directory, creating it, or an error
// when it is not safe to use: not a real directory, not the user's own, or
// open to others.
func (h *Helper) registryDir() (string, error) {
	dir := h.RegistryDir
	if dir == "" {
		base := os.Getenv("XDG_RUNTIME_DIR")
		if !strings.HasPrefix(base, "/") {
			base = os.TempDir()
		}
		dir = filepath.Join(base, "dispatchq-wsl-"+strconv.Itoa(os.Getuid()))
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s belongs to another user", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is open to other users", dir)
	}
	return dir, nil
}

// register records the run and returns what removes the record. A registry
// that cannot be used costs only the next-start cleanup, never the run; the
// lease still stops the tree when the daemon goes away.
func (h *Helper) register(owner string, pid int) func() {
	if owner == "" {
		return func() {}
	}
	dir, err := h.registryDir()
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "dispatchq-wsl: not recording the run for cleanup: %v\n", err)
		return func() {}
	}
	self := os.Getpid()
	selfStart, err1 := proctree.StartTime(self)
	start, err2 := proctree.StartTime(pid)
	if err1 != nil || err2 != nil {
		_, _ = fmt.Fprintf(h.Stderr, "dispatchq-wsl: not recording the run for cleanup: start time unreadable\n")
		return func() {}
	}
	rec := record{Owner: owner, HelperPID: self, HelperStart: selfStart, PID: pid, Start: start, Created: time.Now().UTC()}
	data, _ := json.Marshal(rec)
	name := filepath.Join(dir, fmt.Sprintf("run-%d-%d.json", self, pid))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // private directory checked above
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "dispatchq-wsl: not recording the run for cleanup: %v\n", err)
		return func() {}
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr != nil || cerr != nil {
		_ = os.Remove(name)
		return func() {}
	}
	return func() { _ = os.Remove(name) }
}

// alive reports whether pid is still the process that had start.
func alive(pid int, start string) bool {
	if pid <= 1 {
		return false
	}
	got, err := proctree.StartTime(pid)
	return err == nil && got == start
}

// reap stops the trees of this owner's runs whose helper is gone.
func (h *Helper) reap(owner string) wslproto.Result {
	if owner == "" {
		return wslproto.Result{Error: "reap needs an owner"}
	}
	dir, err := h.registryDir()
	if err != nil {
		return errResult(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return errResult(err)
	}
	n := 0
	for _, e := range ents {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		name := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(name) //nolint:gosec // a regular file in the private directory
		if err != nil {
			continue
		}
		var rec record
		if json.Unmarshal(raw, &rec) != nil || rec.Owner != owner {
			continue
		}
		if alive(rec.HelperPID, rec.HelperStart) {
			continue // its helper is running and enforces its own lease
		}
		if alive(rec.PID, rec.Start) {
			proctree.KillTree(rec.PID)
			n++
		}
		_ = os.Remove(name)
	}
	return wslproto.Result{Reaped: n}
}
