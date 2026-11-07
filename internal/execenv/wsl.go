package execenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// How a WSL instance is reached.
//
// wsl.exe is always started with a fixed argument list:
//
//	wsl.exe --distribution D [--user U] --cd / --exec <helper> <operation>
//
// The distribution and user are configured selectors, each its own argument
// (never part of a shell line); the helper path comes from wslpath; the
// operation is one of a fixed set. The program's arguments, its environment
// with any secret, its folder and its input all travel as frames on the
// helper's standard input (internal/wslproto). Two bootstrap questions run
// before the helper is known — `uname -m` and `wslpath -u <helper>` — and carry
// only the installation's own helper path.
//
// Everything runs as the configured Linux user (or the distribution's default
// user when none is set), so the helper sees exactly that user's files,
// login and PATH.

// Timing of the WSL transport.
const (
	// targetTimeout bounds the bootstrap questions, which may start a stopped
	// distribution.
	targetTimeout = 60 * time.Second
	// opTimeout bounds one file question.
	opTimeout = 30 * time.Second
	// leaseInterval is how often a run tells its helper the daemon is alive;
	// the helper stops the tree after Lease without hearing anything.
	leaseInterval = 3 * time.Second
	// Lease is how long a helper keeps a run without word from the daemon.
	Lease = 15 * time.Second
	// interruptWait is how long a cancel waits for the helper to stop the
	// tree before the caller's own hard stop ends wsl.exe.
	interruptWait = 6 * time.Second
	// targetTTL is how long a distribution's bootstrap answers are reused.
	targetTTL = 10 * time.Minute
	// pipeRelease matches the executor's wait for output after a program ends.
	pipeRelease = 5 * time.Second
)

// queueBinVar is the run variable that names DispatchQ's CLI. A WSL run gets
// its Linux path, so a program in Linux can start it through interop.
const queueBinVar = "DISPATCHQ_BIN"

// mountVar tells the Windows CLI started from a WSL run where that
// distribution mounts Windows drives, so it can turn Linux paths it is given
// back into Windows ones.
const mountVar = "DISPATCHQ_WSL_MNT"

// binaryNames are the CLI names each kind's helper lookup searches for.
var binaryNames = map[provider.Kind]string{
	provider.KindClaudeCode: "claude",
	provider.KindCodex:      "codex",
	provider.KindOpencode:   "opencode",
}

// Service is the daemon's access to WSL. Only the Windows build creates one.
type Service struct {
	// Exe is wsl.exe.
	Exe string
	// HelperDir holds dispatchq-wsl-linux-amd64 and -arm64.
	HelperDir string
	// Owner identifies this installation in the helper's run records.
	Owner string
	// VerifyHelper checks a helper file before it is used; nil is CheckELF.
	VerifyHelper func(path, arch string) error
	// Environ is wsl.exe's environment before adjustment; nil is os.Environ.
	Environ func() []string

	mu      sync.Mutex
	targets map[provider.Execution]*target
}

var _ provider.WSLService = (*Service)(nil)

// OwnerFor derives the run-record owner from a data folder: stable for an
// installation, different for a second one beside it.
func OwnerFor(dataDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dataDir))))
	return hex.EncodeToString(sum[:12])
}

// target is what the bootstrap learned about one distribution and user.
type target struct {
	helper    string // Linux path of the helper
	arch      string
	info      wslproto.Result
	expiresAt time.Time
}

// Available implements provider.WSLService. It starts nothing.
func (s *Service) Available() bool {
	fi, err := os.Stat(s.Exe)
	return err == nil && fi.Mode().IsRegular()
}

// environ is wsl.exe's environment: the daemon's own without WSLENV, so no
// Windows variable is shared into Linux by accident, and asking for UTF-8.
func (s *Service) environ() []string {
	src := os.Environ
	if s.Environ != nil {
		src = s.Environ
	}
	var env []string
	for _, kv := range src() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "WSLENV") || strings.EqualFold(k, "WSL_UTF8") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "WSL_UTF8=1")
}

// wslCmd prepares wsl.exe with args.
func (s *Service) wslCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.Exe, args...) //nolint:gosec // wsl.exe with a fixed, data-free argument list
	system.NoWindow(cmd)
	cmd.Env = s.environ()
	return cmd
}

// execArgs is the argument list that runs prog as e's user.
func execArgs(e provider.Execution, prog string, rest ...string) []string {
	a := []string{"--distribution", e.Distro}
	if e.User != "" {
		a = append(a, "--user", e.User)
	}
	a = append(a, "--cd", "/", "--exec", prog)
	return append(a, rest...)
}

// checkExec refuses an execution wsl.exe must never see.
func checkExec(e provider.Execution) error {
	if e.Type != provider.ExecWSL {
		return fmt.Errorf("not a Linux (WSL) environment: %q", e.Type)
	}
	if err := provider.CheckDistroName(e.Distro); err != nil {
		return err
	}
	return provider.CheckWSLUser(e.User)
}

// output runs wsl.exe and returns standard output, with standard error in the
// error when it fails.
func (s *Service) output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := s.wslCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		msg := firstLine(stderr.Bytes())
		if msg == "" {
			msg = firstLine(out)
		}
		if msg != "" {
			return out, fmt.Errorf("%w: %s", err, msg)
		}
		return out, err
	}
	return out, nil
}

// target returns the bootstrap answers for e, asking WSL when they are not
// cached.
func (s *Service) target(ctx context.Context, e provider.Execution) (*target, error) {
	if err := checkExec(e); err != nil {
		return nil, err
	}
	s.mu.Lock()
	t := s.targets[e]
	s.mu.Unlock()
	if t != nil && time.Now().Before(t.expiresAt) {
		return t, nil
	}
	ctx, cancel := context.WithTimeout(ctx, targetTimeout)
	defer cancel()
	t, err := s.bootstrap(ctx, e)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.targets == nil {
		s.targets = map[provider.Execution]*target{}
	}
	s.targets[e] = t
	s.mu.Unlock()
	return t, nil
}

// where names e for an error message.
func where(e provider.Execution) string { return e.Label() }

func (s *Service) bootstrap(ctx context.Context, e provider.Execution) (*target, error) {
	out, err := s.output(ctx, execArgs(e, "/usr/bin/uname", "-m")...)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be started: %w", where(e), err)
	}
	machine := strings.TrimSpace(decodeWSL(out))
	arch, ok := linuxArch(machine)
	if !ok {
		return nil, fmt.Errorf("%s runs on %q, and DispatchQ ships its Linux helper for x86-64 and arm64 only", where(e), machine)
	}
	win := filepath.Join(s.HelperDir, helperName(arch))
	verify := s.VerifyHelper
	if verify == nil {
		verify = CheckELF
	}
	if err := verify(win, arch); err != nil {
		return nil, fmt.Errorf("DispatchQ's Linux helper for %s is missing or damaged, reinstall DispatchQ: %w", arch, err)
	}
	out, err = s.output(ctx, execArgs(e, "/usr/bin/wslpath", "-u", win)...)
	linux := strings.TrimSpace(decodeWSL(out))
	if err == nil && !strings.HasPrefix(linux, "/") {
		err = fmt.Errorf("wslpath answered %q", linux)
	}
	if err != nil {
		return nil, fmt.Errorf("%s can't reach DispatchQ's helper at %s. Windows drives are probably not mounted there ([automount] enabled=false in /etc/wsl.conf); turn drive mounting on, or run this provider in Windows: %w", where(e), win, err)
	}
	t := &target{helper: linux, arch: arch, expiresAt: time.Now().Add(targetTTL)}
	info, err := s.call(ctx, e, t, wslproto.Header{Op: wslproto.OpInfo})
	if err != nil {
		return nil, err
	}
	if info.Arch != arch {
		return nil, fmt.Errorf("%s answered as %s, expected %s", where(e), info.Arch, arch)
	}
	t.info = info
	return t, nil
}

// call runs one question operation and returns its answer.
func (s *Service) call(ctx context.Context, e provider.Execution, t *target, hdr wslproto.Header) (wslproto.Result, error) {
	hdr.V = wslproto.Version
	payload, err := json.Marshal(hdr)
	if err != nil {
		return wslproto.Result{}, err
	}
	cmd := s.wslCmd(ctx, execArgs(e, t.helper, hdr.Op)...)
	var in bytes.Buffer
	_ = wslproto.WriteFrame(&in, wslproto.FrameHeader, payload)
	cmd.Stdin = &in
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	var res wslproto.Result
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		msg := firstLine(stderr.Bytes())
		if msg == "" {
			msg = firstLine(stdout.Bytes())
		}
		if runErr == nil {
			runErr = errors.New("no answer")
		}
		return wslproto.Result{}, fmt.Errorf("DispatchQ's helper in %s failed (%s): %v", where(e), hdr.Op, joinMsg(runErr, msg))
	}
	return res, nil
}

func joinMsg(err error, msg string) string {
	if msg == "" {
		return err.Error()
	}
	return err.Error() + ": " + msg
}

// op runs a question for e, bootstrapping first.
func (s *Service) op(ctx context.Context, e provider.Execution, hdr wslproto.Header) (wslproto.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	t, err := s.target(ctx, e)
	if err != nil {
		return wslproto.Result{}, err
	}
	return s.call(ctx, e, t, hdr)
}

// Distros implements provider.WSLService.
func (s *Service) Distros(ctx context.Context) ([]provider.WSLDistro, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	quiet, err := s.output(ctx, "--list", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("WSL has no Linux distribution DispatchQ can use (is WSL installed?): %w", err)
	}
	names := lines(decodeWSL(quiet))
	out := make([]provider.WSLDistro, 0, len(names))
	for _, n := range names {
		if provider.CheckDistroName(n) == nil {
			out = append(out, provider.WSLDistro{Name: n})
		}
	}
	// The verbose list adds the default mark, the state and the WSL version.
	// Its headings are localised, so it is read by position and only for
	// names the quiet list already gave.
	if verbose, err := s.output(ctx, "--list", "--verbose"); err == nil {
		for _, l := range lines(decodeWSL(verbose))[min(1, len(names)):] {
			def := strings.HasPrefix(l, "*")
			f := strings.Fields(strings.TrimPrefix(l, "*"))
			if len(f) < 3 {
				continue
			}
			for i := range out {
				if out[i].Name != f[0] {
					continue
				}
				out[i].Default = def
				out[i].State = strings.Join(f[1:len(f)-1], " ")
				if v, err := strconv.Atoi(f[len(f)-1]); err == nil {
					out[i].Version = v
				}
			}
		}
	}
	return out, nil
}

// Users implements provider.WSLService. It asks as the default user.
func (s *Service) Users(ctx context.Context, distro string) ([]string, error) {
	res, err := s.op(ctx, provider.Execution{Type: provider.ExecWSL, Distro: distro}, wslproto.Header{Op: wslproto.OpUsers})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Users, nil
}

// Detect implements provider.WSLService.
func (s *Service) Detect(ctx context.Context, e provider.Execution, kind provider.Kind) (provider.WSLDetection, error) {
	ctx, cancel := context.WithTimeout(ctx, targetTimeout)
	defer cancel()
	t, err := s.target(ctx, e)
	if err != nil {
		return provider.WSLDetection{}, err
	}
	d := provider.WSLDetection{Home: t.info.Home, User: t.info.User, Arch: t.info.Arch, Interop: t.info.Interop}
	if name := binaryNames[kind]; name != "" {
		res, err := s.call(ctx, e, t, wslproto.Header{Op: wslproto.OpLookup, Path: name})
		switch {
		case err != nil:
			return provider.WSLDetection{}, err
		case res.Error != "":
			d.Warnings = append(d.Warnings, fmt.Sprintf("%s is not installed for %s in %s (Windows programs are not used). Install it inside Linux.", name, t.info.User, e.Distro))
		default:
			d.Binary = res.Path
		}
	}
	if !t.info.WSL2 {
		d.Warnings = append(d.Warnings, "This is a WSL 1 distribution. Providers are meant for WSL 2; convert it with wsl --set-version "+e.Distro+" 2.")
	}
	if !t.info.Interop {
		d.Warnings = append(d.Warnings, InteropOffDetail)
	}
	return d, nil
}

// InteropOffDetail explains a distribution without Windows interop.
const InteropOffDetail = "Windows interop is off in this Linux system, so its runs can't queue follow-up jobs, publish artifacts or send notifications."

// Reap implements provider.WSLService.
func (s *Service) Reap(ctx context.Context, e provider.Execution) error {
	if s.Owner == "" {
		return errors.New("no owner to reap for")
	}
	res, err := s.op(ctx, e, wslproto.Header{Op: wslproto.OpReap, Owner: s.Owner})
	if err != nil {
		return err
	}
	if res.Error != "" {
		return errors.New(res.Error)
	}
	return nil
}

// Host implements provider.WSLService.
func (s *Service) Host(e provider.Execution) provider.Host { return &Host{svc: s, ex: e} }

// Host is one WSL distribution and user, as a provider.Host.
type Host struct {
	svc *Service
	ex  provider.Execution
}

var _ provider.Host = (*Host)(nil)

// Execution implements provider.Host.
func (h *Host) Execution() provider.Execution { return h.ex }

func (h *Host) fileOp(ctx context.Context, op, p string, limit int64) (wslproto.Result, error) {
	res, err := h.svc.op(ctx, h.ex, wslproto.Header{Op: op, Path: p, Limit: limit})
	if err != nil {
		return res, err
	}
	if res.Error != "" {
		if res.NotFound {
			return res, fmt.Errorf("%s: %w", p, fs.ErrNotExist)
		}
		return res, errors.New(res.Error)
	}
	return res, nil
}

// StatDir implements provider.Host.
func (h *Host) StatDir(ctx context.Context, p string) error {
	res, err := h.fileOp(ctx, wslproto.OpStat, p, 0)
	if err != nil {
		return err
	}
	if !res.IsDir {
		return provider.ErrNotDirectory
	}
	return nil
}

// Executable implements provider.Host. A bare name is looked up on the
// user's login PATH, skipping Windows programs.
func (h *Host) Executable(ctx context.Context, p string) error {
	if !strings.Contains(p, "/") {
		_, err := h.fileOp(ctx, wslproto.OpLookup, p, 0)
		return err
	}
	res, err := h.fileOp(ctx, wslproto.OpStat, p, 0)
	switch {
	case err != nil:
		return err
	case res.IsDir:
		return fmt.Errorf("%s is a directory", p)
	case !res.Executable:
		return fmt.Errorf("%s is not executable", p)
	}
	return nil
}

// ReadFile implements provider.Host. The data stays in the daemon's memory.
func (h *Host) ReadFile(ctx context.Context, p string, limit int64) ([]byte, error) {
	res, err := h.fileOp(ctx, wslproto.OpRead, p, limit)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// WorkDir implements provider.Host.
func (h *Host) WorkDir(ctx context.Context, stored string) (string, error) {
	if err := CanReach(h.ex, stored, "windows"); err != nil {
		return "", err
	}
	if stored == "" || IsLinuxPath(stored) {
		return stored, nil
	}
	if _, linux, ok := splitWSLShare(stored); ok {
		return linux, nil
	}
	t, err := h.svc.target(ctx, h.ex)
	if err != nil {
		return "", err
	}
	return ToLinux(h.ex, stored, t.info.MountRoot)
}

// Probe implements provider.Prober: combined output, bounded like a native
// probe.
func (h *Host) Probe(ctx context.Context, c provider.Command) ([]byte, error) {
	// Starting a stopped distribution can take longer than a probe may; that
	// first contact has its own bound, and the probe's starts after it.
	if _, err := h.svc.target(ctx, h.ex); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, provider.ProbeTimeout)
	defer cancel()
	var out lockedBuffer
	err := h.runTo(ctx, c, wslproto.CwdHome, &out, &out)
	return out.Bytes(), err
}

// Output implements provider.Host. The aside runs in DispatchQ's own empty
// scratch folder inside Linux.
func (h *Host) Output(ctx context.Context, c provider.Command, _ string) ([]byte, error) {
	var stdout, stderr lockedBuffer
	err := h.runTo(ctx, c, wslproto.CwdAside, &stdout, &stderr)
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, shorten(msg, 300))
		}
	}
	return stdout.Bytes(), err
}

func (h *Host) runTo(ctx context.Context, c provider.Command, cwd string, stdout, stderr io.Writer) error {
	proc, err := h.prepare(ctx, c, cwd, nil, false)
	if err != nil {
		return err
	}
	proc.Cmd.Stdout, proc.Cmd.Stderr = stdout, stderr
	proc.Cmd.WaitDelay = time.Second
	return proc.Cmd.Run()
}

// Command implements provider.Host.
func (h *Host) Command(ctx context.Context, c provider.Command, o provider.CmdOptions) (*provider.Process, error) {
	return h.prepare(ctx, c, o.Dir, o.Vars, o.Stream)
}

// prepare builds wsl.exe for one run. The returned command is not started; a
// goroutine writes the header, the program's input, and then liveness pings
// for as long as wsl.exe reads them.
func (h *Host) prepare(ctx context.Context, c provider.Command, cwd string, vars []string, stream bool) (*provider.Process, error) {
	t, err := h.svc.target(ctx, h.ex)
	if err != nil {
		return nil, err
	}
	hdr := wslproto.Header{
		V:             wslproto.Version,
		Op:            wslproto.OpRun,
		Argv:          append([]string{c.Path}, c.Args...),
		LoginEnv:      true,
		Cwd:           cwd,
		Owner:         h.svc.Owner,
		LeaseMS:       int(Lease / time.Millisecond),
		PipeReleaseMS: int(pipeRelease / time.Millisecond),
	}
	if len(vars) > 0 {
		hdr.Env, hdr.ShareWithWindows = runVars(vars, t.info.MountRoot, h.ex)
	}
	hdr.Env = append(hdr.Env, c.Env...)
	payload, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	cmd := h.svc.wslCmd(ctx, execArgs(h.ex, t.helper, wslproto.OpRun)...)
	w, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	conn := newFrameConn(w)
	proc := &provider.Process{Cmd: cmd, Interrupt: conn.interrupt}
	if stream {
		proc.Stdin = &frameStdin{conn: conn}
	}
	// A caller that sets its own hard stop (the executor's Job Object) runs
	// Interrupt first itself; this default covers probes, asides and the
	// app server.
	cmd.Cancel = func() error {
		conn.interrupt()
		return cmd.Process.Kill()
	}
	go conn.serve(ctx, payload, c.Stdin, stream)
	return proc, nil
}

// runVars turns DispatchQ's run variables into a WSL run's environment: the
// CLI path in its Linux form (started through interop), the mount root, and
// the list of names a Windows program started from the run must see.
func runVars(vars []string, mountRoot string, ex provider.Execution) (env, share []string) {
	all := append(provider.WithLegacyNames(append([]string(nil), vars...)), mountVar+"="+mountRoot)
	for _, kv := range all {
		k, v, _ := strings.Cut(kv, "=")
		if (k == queueBinVar || k == "CLAUDEQ_BIN") && v != "" {
			if l, err := ToLinux(ex, v, mountRoot); err == nil {
				v = l
			}
		}
		env = append(env, k+"="+v)
		share = append(share, k)
	}
	return env, append(share, "WSL_DISTRO_NAME")
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// lockedBuffer is a bytes.Buffer two copying goroutines may write.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.b.Bytes()...)
}

func (l *lockedBuffer) String() string { return string(l.Bytes()) }
