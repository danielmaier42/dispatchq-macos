package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/danielmaier42/claudeq/internal/procown"
	"github.com/danielmaier42/claudeq/internal/store"
)

// An execution environment is where a provider instance's CLI runs. The zero
// value is the daemon's own operating system, which is what every instance
// configured before environments existed means. DispatchQ for Windows can also
// run an instance inside a Linux distribution of the Windows Subsystem for
// Linux (WSL); that environment belongs to the instance, never to the daemon,
// so a native and a WSL instance of the same harness sit side by side.
//
// Every process and every file of an instance is reached through its [Host]:
// runs, readiness and model probes, limit readings, asides and the Codex app
// server. The native host is the code DispatchQ always ran; nothing about a
// native instance reaches WSL.

// ExecType names an execution environment kind.
type ExecType string

const (
	// ExecNative is the daemon's own operating system.
	ExecNative ExecType = ""
	// ExecWSL is a Linux distribution of the Windows Subsystem for Linux.
	ExecWSL ExecType = "wsl"
)

// Execution is where an instance runs.
type Execution struct {
	// Type is the environment kind.
	Type ExecType `json:"type,omitempty"`
	// Distro is the WSL distribution.
	Distro string `json:"distro,omitempty"`
	// User is the Linux user to run as; empty is the distribution's default.
	User string `json:"user,omitempty"`
}

// IsNative reports whether this is the daemon's own operating system. A table
// that names a distribution or user without a type is not native: it is a
// configuration that must be rejected, never quietly run on Windows.
func (e Execution) IsNative() bool { return e == Execution{} }

// Label names the environment for the operator.
func (e Execution) Label() string {
	switch {
	case e.IsNative():
		return "this computer"
	case e.Type == ExecWSL && e.User != "":
		return fmt.Sprintf("Linux (%s, user %s)", e.Distro, e.User)
	case e.Type == ExecWSL:
		return fmt.Sprintf("Linux (%s)", e.Distro)
	}
	return fmt.Sprintf("unknown environment %q", e.Type)
}

// key is the environment's part of an instance's cache fingerprint.
func (e Execution) key() string {
	if e.IsNative() {
		return ""
	}
	return string(e.Type) + "\x00" + e.Distro + "\x00" + e.User
}

func executionOf(p *store.ProviderExecution) Execution {
	if p == nil {
		return Execution{}
	}
	return Execution{Type: ExecType(p.Type), Distro: p.Distro, User: p.User}
}

func (e Execution) stored() *store.ProviderExecution {
	if e.IsNative() {
		return nil
	}
	return &store.ProviderExecution{Type: string(e.Type), Distro: e.Distro, User: e.User}
}

// ErrWSLUnsupported is returned for a WSL instance where WSL cannot exist.
var ErrWSLUnsupported = errors.New("providers that run in Linux (WSL) exist only in DispatchQ for Windows")

// wslUserRe is the shape of a Linux user name useradd accepts by default.
var wslUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)

// CheckExecution reports why an execution environment cannot be stored. It
// checks the shape only; whether the distribution is installed is asked of WSL
// by whoever saves it, and later by the readiness check.
func CheckExecution(reg *Registry, e Execution) error {
	if e.IsNative() {
		return nil
	}
	if e.Type != ExecWSL {
		return fmt.Errorf("unknown execution environment %q (known: wsl)", e.Type)
	}
	if reg == nil || reg.WSL() == nil {
		return ErrWSLUnsupported
	}
	if err := CheckDistroName(e.Distro); err != nil {
		return err
	}
	return CheckWSLUser(e.User)
}

// CheckWSLUser reports whether u can name a Linux user; empty is the
// distribution's default user. Like the distribution, it reaches wsl.exe as an
// argument of its own, so this refuses what could read as an option.
func CheckWSLUser(u string) error {
	if u != "" && !wslUserRe.MatchString(u) {
		return fmt.Errorf("%q is not a valid Linux user name", u)
	}
	return nil
}

// CheckDistroName reports whether name can be a WSL distribution name. The
// value reaches wsl.exe as one argument of its own, so this is not escaping:
// it refuses what would read as an option or could not be a name at all.
func CheckDistroName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("a Linux (WSL) provider needs a distribution")
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("distribution %q may not start with '-'", name)
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' || r == '/' }):
		return fmt.Errorf("distribution %q contains characters a distribution name cannot have", name)
	case len(name) > 64:
		return fmt.Errorf("distribution name %q is too long", name)
	}
	return nil
}

// Host is the environment an instance's processes and files live in.
type Host interface {
	// Prober runs a readiness probe and returns its combined output, bounded
	// by ProbeTimeout.
	Prober
	// Execution names the environment.
	Execution() Execution
	// StatDir returns nil for a usable directory, ErrNotDirectory for a file,
	// and the stat error otherwise.
	StatDir(ctx context.Context, path string) error
	// Executable reports why path cannot be started as a provider CLI there.
	Executable(ctx context.Context, path string) error
	// ReadFile reads at most limit bytes of a file.
	ReadFile(ctx context.Context, path string, limit int64) ([]byte, error)
	// WorkDir turns a task's stored working directory into the path the
	// environment runs in, or says why it cannot reach it.
	WorkDir(ctx context.Context, stored string) (string, error)
	// Output runs an aside turn and returns its standard output. dir is the
	// daemon's scratch directory; a host that is not native uses its own.
	Output(ctx context.Context, c Command, dir string) ([]byte, error)
	// Command prepares a run or a streaming conversation. It is not started.
	Command(ctx context.Context, c Command, o CmdOptions) (*Process, error)
}

// CmdOptions shape a prepared [Process].
type CmdOptions struct {
	// Dir is the working directory in the environment's own form.
	Dir string
	// Vars are DispatchQ's own variables of a run (DISPATCHQ_*), in order.
	Vars []string
	// Stream keeps the program's standard input open as Process.Stdin, for a
	// conversation like the Codex app server. Otherwise Command.Stdin is
	// written and closed.
	Stream bool
}

// Process is a prepared, not yet started program.
type Process struct {
	// Cmd is the process DispatchQ starts and waits for.
	Cmd *exec.Cmd
	// Stdin is the program's standard input, set for CmdOptions.Stream.
	Stdin io.WriteCloser
	// Interrupt, when set, asks the environment to stop the program's whole
	// process tree and waits a bounded time for that, before the caller's own
	// hard stop. It is how a cancel reaches Linux descendants that a Windows
	// Job Object cannot see.
	Interrupt func()
}

// Start starts the program as the owner of its whole process tree (see
// procown.Start): cancelling the context the command was built with stops the
// program and every process it started, Interrupt first. On success the
// caller waits with Cmd.Wait and calls release afterwards; on failure nothing
// is left running.
func (p *Process) Start() (release func(), err error) {
	return procown.Start(p.Cmd, p.Interrupt)
}

// ErrNotDirectory is what StatDir returns for a path that is not a directory.
var ErrNotDirectory = errors.New("not a directory")

// WSLDistro is one installed distribution.
type WSLDistro struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	State   string `json:"state,omitempty"`
	Version int    `json:"version,omitempty"`
}

// WSLDetection is what DispatchQ found inside a distribution for one kind.
type WSLDetection struct {
	Binary   string   `json:"binary,omitempty"`
	Home     string   `json:"home"`
	User     string   `json:"user"`
	Arch     string   `json:"arch"`
	Interop  bool     `json:"interop"`
	Warnings []string `json:"warnings,omitempty"`
}

// WSLService answers the questions about WSL that configuring a provider
// needs. Only DispatchQ for Windows has one. Every method but Available starts
// wsl.exe, so it is called only when the operator configures or uses a WSL
// provider.
type WSLService interface {
	// Available reports whether wsl.exe is installed, without starting it.
	Available() bool
	Distros(ctx context.Context) ([]WSLDistro, error)
	Users(ctx context.Context, distro string) ([]string, error)
	Detect(ctx context.Context, e Execution, kind Kind) (WSLDetection, error)
	// Host is the environment of a WSL execution.
	Host(e Execution) Host
	// Reap stops process trees a previous, dead owner left behind in e.
	Reap(ctx context.Context, e Execution) error
}

// HostFor returns the environment an instance runs in. A native instance
// always gets [NativeHost], without asking the WSL service anything.
func (r *Registry) HostFor(inst Instance) (Host, error) {
	e := inst.Execution
	switch {
	case e.IsNative():
		return NativeHost{}, nil
	case e.Type != ExecWSL:
		return nil, fmt.Errorf("%s runs in an unknown execution environment %q; this version of DispatchQ cannot run it", inst.Label(), e.Type)
	}
	svc := r.WSL()
	if svc == nil {
		return nil, fmt.Errorf("%s: %w", inst.Label(), ErrWSLUnsupported)
	}
	return svc.Host(e), nil
}

// SetWSL installs the WSL service. Only the Windows build calls it.
func (r *Registry) SetWSL(svc WSLService) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wsl = svc
}

// WSL returns the WSL service, or nil where there is none.
func (r *Registry) WSL() WSLService {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.wsl
}

// CheckConfigDir is the readiness check every adapter makes of an instance's
// configuration directory: an unreadable account directory is a configuration
// problem, not a missing installation.
func CheckConfigDir(ctx context.Context, h Host, inst Instance) (Health, bool) {
	if inst.ConfigDir == "" {
		return Health{}, true
	}
	err := h.StatDir(ctx, inst.ConfigDir)
	switch {
	case errors.Is(err, ErrNotDirectory):
		return Health{
			State:  HealthInvalidConfiguration,
			Reason: fmt.Sprintf("The configuration directory %s is not a directory.", inst.ConfigDir),
		}, false
	case err != nil:
		return Health{
			State:  HealthInvalidConfiguration,
			Reason: fmt.Sprintf("The configuration directory %s is not accessible: %s.", inst.ConfigDir, RedactLine(err.Error())),
		}, false
	}
	return Health{}, true
}

// RedactLine keeps a probe's own words out of a reason that is logged and
// served over the API: the first non-empty line only, cut short.
func RedactLine(s string) string {
	line := ""
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	const limit = 160
	if r := []rune(line); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return line
}

// WithLegacyNames repeats each DISPATCHQ_ variable under the CLAUDEQ_ name it
// had before the rename, so a script outside dispatchq that still reads
// $CLAUDEQ_BIN or $CLAUDEQ_HOME keeps working after the update.
func WithLegacyNames(env []string) []string {
	var legacy []string
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, "DISPATCHQ_"); ok {
			legacy = append(legacy, "CLAUDEQ_"+rest)
		}
	}
	return append(env, legacy...)
}

// nativeEnviron is os.Environ, replaceable by tests of the native host.
var nativeEnviron = os.Environ
