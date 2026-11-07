//go:build unix

// Package wslhelper is the DispatchQ helper that runs inside a WSL
// distribution (cmd/dispatchq-wsl). The Windows daemon starts it through
// wsl.exe with a fixed argument list and talks to it in the frames of
// internal/wslproto. It starts the provider CLI as the Linux user wsl.exe ran
// it as, owns that program's whole process tree, and stops the tree when the
// daemon cancels, goes silent or goes away.
//
// It never stops a process it did not start, never stops the distribution or
// the WSL VM, and never writes the frame (which holds secrets) anywhere.
//
// The package builds on every Unix so its behaviour is tested on the
// development machine with real processes; it is shipped for Linux only.
package wslhelper

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/danielmaier42/claudeq/internal/proctree"
	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// Exit codes of the helper itself, as opposed to the program it ran.
const (
	// ExitHelperFailed is the helper failing before or around the program.
	ExitHelperFailed = 125
)

// Defaults when a header leaves them out.
const (
	defaultLease       = 15 * time.Second
	defaultPipeRelease = 5 * time.Second
	loginEnvTimeout    = 5 * time.Second
	killWait           = 5 * time.Second
	defaultReadLimit   = 1 << 20
)

// Helper is one helper invocation. The zero values of the injectable fields
// are the real system; tests replace them.
type Helper struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// RegistryDir overrides where run records are kept.
	RegistryDir string
	// Wslpath translates a Windows path; nil runs `wslpath -u`.
	Wslpath func(ctx context.Context, win string) (string, error)
	// InteropFiles are the binfmt entries that say whether Windows interop is on.
	InteropFiles []string
	// PasswdFile is the user database.
	PasswdFile string
	// OSRelease is the kernel release file.
	OSRelease string
	// Shell overrides the login shell, for tests.
	Shell string
}

// Main runs the helper on the process's own standard streams.
func Main() int {
	h := &Helper{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	return h.Run(os.Args[1:])
}

func (h *Helper) failf(format string, a ...any) int {
	// Fixed text plus paths only: the frame's environment and arguments are
	// never part of a helper message.
	_, _ = fmt.Fprintf(h.Stderr, "dispatchq-wsl: "+format+"\n", a...)
	return ExitHelperFailed
}

// Run executes the operation named by args[0].
func (h *Helper) Run(args []string) int {
	if len(args) != 1 {
		return h.failf("usage: dispatchq-wsl <operation>")
	}
	frames := bufio.NewReaderSize(h.Stdin, 64<<10)
	typ, payload, err := wslproto.ReadFrame(frames)
	if err != nil || typ != wslproto.FrameHeader {
		return h.failf("no header frame on standard input")
	}
	var hdr wslproto.Header
	if err := json.Unmarshal(payload, &hdr); err != nil {
		return h.failf("unreadable header frame")
	}
	if hdr.V != wslproto.Version {
		return h.failf("protocol version %d is not supported (this helper speaks %d)", hdr.V, wslproto.Version)
	}
	if hdr.Op != args[0] {
		return h.failf("operation %q does not match the header", args[0])
	}
	switch hdr.Op {
	case wslproto.OpRun:
		return h.run(hdr, frames)
	case wslproto.OpInfo:
		return h.answer(h.info())
	case wslproto.OpLookup:
		return h.answer(h.lookup(hdr.Path))
	case wslproto.OpStat:
		return h.answer(h.stat(hdr.Path))
	case wslproto.OpRead:
		return h.answer(h.read(hdr.Path, hdr.Limit))
	case wslproto.OpUsers:
		return h.answer(h.users())
	case wslproto.OpReap:
		return h.answer(h.reap(hdr.Owner))
	}
	return h.failf("unknown operation %q", hdr.Op)
}

func (h *Helper) answer(res wslproto.Result) int {
	if err := json.NewEncoder(h.Stdout).Encode(res); err != nil {
		return h.failf("writing the answer: %v", err)
	}
	return 0
}

func errResult(err error) wslproto.Result {
	return wslproto.Result{Error: err.Error(), NotFound: errors.Is(err, os.ErrNotExist)}
}

// home is the current user's home directory.
func (h *Helper) home() string {
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	if d, err := os.UserHomeDir(); err == nil {
		return d
	}
	return "/"
}

// expand resolves a leading "~" against the user's home.
func (h *Helper) expand(p string) string {
	if p == "~" {
		return h.home()
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return path.Join(h.home(), rest)
	}
	return p
}

func (h *Helper) wslpath(ctx context.Context, win string) (string, error) {
	if h.Wslpath != nil {
		return h.Wslpath(ctx, win)
	}
	out, err := exec.CommandContext(ctx, "wslpath", "-u", win).Output()
	if err != nil {
		return "", fmt.Errorf("wslpath: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// mountRoot is where Windows drives appear ("/mnt/"), read from wslpath so a
// customised [automount] root is honoured. Empty when drives are not mounted.
func (h *Helper) mountRoot() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := h.wslpath(ctx, `C:\`)
	if err != nil {
		return ""
	}
	p = strings.TrimSuffix(p, "/")
	if !strings.HasSuffix(strings.ToLower(p), "/c") {
		return ""
	}
	return p[:len(p)-1]
}

func (h *Helper) info() wslproto.Result {
	res := wslproto.Result{Arch: runtime.GOARCH, UID: os.Getuid(), Home: h.home(), MountRoot: h.mountRoot()}
	if u, err := user.Current(); err == nil {
		res.User = u.Username
	}
	files := h.InteropFiles
	if files == nil {
		files = []string{"/proc/sys/fs/binfmt_misc/WSLInterop", "/proc/sys/fs/binfmt_misc/WSLInterop-late"}
	}
	for _, f := range files {
		if raw, err := os.ReadFile(f); err == nil && strings.HasPrefix(strings.TrimSpace(string(raw)), "enabled") { //nolint:gosec // fixed system paths
			res.Interop = true
		}
	}
	rel := h.OSRelease
	if rel == "" {
		rel = "/proc/sys/kernel/osrelease"
	}
	if raw, err := os.ReadFile(rel); err == nil { //nolint:gosec // fixed system path
		k := strings.ToLower(string(raw))
		res.WSL2 = strings.Contains(k, "wsl2") || strings.Contains(k, "microsoft-standard")
	}
	return res
}

// windowsProgram reports whether p is a Windows program the Linux PATH offers
// through interop (appendWindowsPath): DispatchQ must run the Linux CLI, never
// silently the Windows one.
func windowsProgram(p, mountRoot string) bool {
	lower := strings.ToLower(p)
	for _, ext := range []string{".exe", ".cmd", ".bat", ".com", ".ps1"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return mountRoot != "" && strings.HasPrefix(p, mountRoot)
}

// lookPath resolves name on the PATH of env, skipping Windows programs.
func (h *Helper) lookPath(name string, env []string, mountRoot string) (string, error) {
	if strings.Contains(name, "/") {
		p := h.expand(name)
		if err := executable(p); err != nil {
			return "", err
		}
		return p, nil
	}
	for _, dir := range filepath.SplitList(getenv(env, "PATH")) {
		if dir == "" || !strings.HasPrefix(dir, "/") {
			continue
		}
		p := path.Join(dir, name)
		if windowsProgram(p, mountRoot) {
			continue
		}
		if executable(p) == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s was not found on the Linux PATH (Windows programs are skipped): %w", name, os.ErrNotExist)
}

func executable(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", p)
	}
	if err := syscall.Access(p, 0x1 /* X_OK */); err != nil {
		return fmt.Errorf("%s is not executable", p)
	}
	return nil
}

func (h *Helper) lookup(name string) wslproto.Result {
	p, err := h.lookPath(name, h.baseEnv(true), h.mountRoot())
	if err != nil {
		return errResult(err)
	}
	return wslproto.Result{Path: p, Executable: true}
}

func (h *Helper) stat(p string) wslproto.Result {
	p = h.expand(p)
	fi, err := os.Stat(p)
	if err != nil {
		return errResult(err)
	}
	return wslproto.Result{Path: p, IsDir: fi.IsDir(), Executable: !fi.IsDir() && executable(p) == nil}
}

func (h *Helper) read(p string, limit int64) wslproto.Result {
	if limit <= 0 {
		limit = defaultReadLimit
	}
	f, err := os.Open(h.expand(p)) //nolint:gosec // a path of the provider's own configuration, read as its own user
	if err != nil {
		return errResult(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return errResult(err)
	}
	return wslproto.Result{Data: data}
}

func (h *Helper) passwd() string {
	if h.PasswdFile != "" {
		return h.PasswdFile
	}
	return "/etc/passwd"
}

// users lists the distribution's people: uid 1000 and up, with a login shell.
func (h *Helper) users() wslproto.Result {
	raw, err := os.ReadFile(h.passwd())
	if err != nil {
		return errResult(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, ":")
		if len(f) < 7 {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil || uid < 1000 || uid >= 60000 {
			continue
		}
		if sh := f[6]; strings.HasSuffix(sh, "nologin") || strings.HasSuffix(sh, "false") || sh == "" {
			continue
		}
		out = append(out, f[0])
	}
	return wslproto.Result{Users: out}
}

// loginShell is the current user's shell from the user database.
func (h *Helper) loginShell() string {
	if h.Shell != "" {
		return h.Shell
	}
	if raw, err := os.ReadFile(h.passwd()); err == nil {
		uid := strconv.Itoa(os.Getuid())
		for _, line := range strings.Split(string(raw), "\n") {
			if f := strings.Split(line, ":"); len(f) >= 7 && f[2] == uid && strings.HasPrefix(f[6], "/") {
				return f[6]
			}
		}
	}
	if sh := os.Getenv("SHELL"); strings.HasPrefix(sh, "/") {
		return sh
	}
	return "/bin/sh"
}

// envMarker separates whatever a profile prints from the environment dump.
const envMarker = "\x00__DISPATCHQ_ENV__\x00"

// loginEnvScript is fixed. No data from the frame is ever part of it.
const loginEnvScript = `printf '\000__DISPATCHQ_ENV__\000'; exec env -0`

// baseEnv is the environment a program starts from: the user's login shell
// environment, so PATH holds what the profile adds (~/.local/bin, say), or
// the helper's own when that cannot be read in time.
func (h *Helper) baseEnv(login bool) []string {
	if !login {
		return os.Environ()
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginEnvTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.loginShell(), "-ilc", loginEnvScript) //nolint:gosec // the user's own shell with a fixed script
	cmd.Stdin = nil                                                        // /dev/null: the control channel is never the shell's input
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			proctree.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	i := strings.Index(string(out), envMarker)
	if err != nil || i < 0 {
		return os.Environ()
	}
	var env []string
	for _, kv := range strings.Split(string(out[i+len(envMarker):]), "\x00") {
		if strings.Contains(kv, "=") && !strings.HasPrefix(kv, "=") {
			env = append(env, kv)
		}
	}
	if len(env) == 0 {
		return os.Environ()
	}
	return env
}

func getenv(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}

// setenv replaces or adds KEY=VALUE.
func setenv(env []string, kv string) []string {
	key, _, _ := strings.Cut(kv, "=")
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return append(out, kv)
}

// validName is a variable name WSLENV can carry.
func validName(n string) bool {
	if n == "" {
		return false
	}
	for _, r := range n {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// runEnv builds the program's environment from the header.
func (h *Helper) runEnv(hdr wslproto.Header) []string {
	env := h.baseEnv(hdr.LoginEnv)
	for _, kv := range hdr.Env {
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
			env = setenv(env, kv)
		}
	}
	if len(hdr.ShareWithWindows) > 0 {
		var share []string
		if cur := getenv(env, "WSLENV"); cur != "" {
			share = append(share, cur)
		}
		for _, n := range hdr.ShareWithWindows {
			if validName(n) {
				share = append(share, n+"/w")
			}
		}
		env = setenv(env, "WSLENV="+strings.Join(share, ":"))
	}
	return env
}

func (h *Helper) workDir(cwd string) (string, error) {
	switch {
	case cwd == wslproto.CwdHome:
		return h.home(), nil
	case cwd == wslproto.CwdAside:
		d := path.Join(h.home(), ".cache", "dispatchq", "aside")
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", fmt.Errorf("preparing %s: %w", d, err)
		}
		return d, nil
	}
	d := h.expand(cwd)
	if !strings.HasPrefix(d, "/") {
		return "", fmt.Errorf("working directory %s is not an absolute Linux path", cwd)
	}
	return d, nil
}

// stdinQueue buffers program input so a program that does not read its input
// can never stall the control channel (and with it cancel and liveness).
type stdinQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	closed bool
}

func newStdinQueue() *stdinQueue {
	q := &stdinQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *stdinQueue) push(b []byte) {
	q.mu.Lock()
	if !q.closed {
		q.chunks = append(q.chunks, b)
	}
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *stdinQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Signal()
}

// drain writes everything to w and closes it at the end of input.
func (q *stdinQueue) drain(w io.WriteCloser) {
	defer func() { _ = w.Close() }()
	for {
		q.mu.Lock()
		for len(q.chunks) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.chunks) == 0 {
			q.mu.Unlock()
			return
		}
		b := q.chunks[0]
		q.chunks = q.chunks[1:]
		q.mu.Unlock()
		if _, err := w.Write(b); err != nil {
			q.mu.Lock()
			q.chunks, q.closed = nil, true
			q.mu.Unlock()
			return
		}
	}
}

// run starts the program and relays it until it ends or must be stopped.
func (h *Helper) run(hdr wslproto.Header, frames io.Reader) int {
	if len(hdr.Argv) == 0 {
		return h.failf("run: no program")
	}
	env := h.runEnv(hdr)
	prog, err := h.lookPath(hdr.Argv[0], env, h.mountRoot())
	if err != nil {
		return h.failf("%v", err)
	}
	dir, err := h.workDir(hdr.Cwd)
	if err != nil {
		return h.failf("%v", err)
	}
	cmd := exec.Command(prog, hdr.Argv[1:]...) //nolint:gosec // the provider CLI the daemon configured, no shell
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return h.failf("stdin: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return h.failf("stdout: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return h.failf("stderr: %v", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		return h.failf("starting %s: %v", prog, err)
	}
	_ = outW.Close()
	_ = errW.Close()
	pid := cmd.Process.Pid
	unregister := h.register(hdr.Owner, pid)
	defer unregister()

	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(h.Stdout, outR); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(h.Stderr, errR); copied <- struct{}{} }()

	queue := newStdinQueue()
	go queue.drain(stdin)

	exited := make(chan *os.ProcessState, 1)
	go func() {
		_ = cmd.Wait()
		exited <- cmd.ProcessState
	}()

	stop := make(chan string, 1)
	lastFrame := make(chan struct{}, 1)
	go h.control(frames, queue, stop, lastFrame)
	lease := time.Duration(hdr.LeaseMS) * time.Millisecond
	if lease <= 0 {
		lease = defaultLease
	}
	go watchLease(lease, lastFrame, stop)

	release := time.Duration(hdr.PipeReleaseMS) * time.Millisecond
	if release <= 0 {
		release = defaultPipeRelease
	}
	select {
	case st := <-exited:
		// The program ended on its own. Whatever it left running keeps
		// running, as it does on macOS and native Windows; only its output is
		// no longer waited for past the release delay.
		waitCopies(copied, release)
		return exitCode(st)
	case reason := <-stop:
		proctree.KillTree(pid)
		select {
		case st := <-exited:
			waitCopies(copied, time.Second)
			if reason != "" {
				_, _ = fmt.Fprintf(h.Stderr, "dispatchq-wsl: stopped the run's process tree (%s)\n", reason)
			}
			return exitCode(st)
		case <-time.After(killWait):
			return h.failf("the run did not stop within %s of %s", killWait, reason)
		}
	}
}

// control reads frames until the stream ends. Every frame refreshes the lease.
func (h *Helper) control(frames io.Reader, q *stdinQueue, stop chan<- string, alive chan<- struct{}) {
	signal := func(reason string) {
		select {
		case stop <- reason:
		default:
		}
	}
	for {
		typ, payload, err := wslproto.ReadFrame(frames)
		if err != nil {
			q.close()
			if errors.Is(err, io.EOF) {
				signal("the daemon's channel closed")
			} else {
				signal("the daemon's channel broke")
			}
			return
		}
		select {
		case alive <- struct{}{}:
		default:
		}
		switch typ {
		case wslproto.FrameData:
			q.push(payload)
		case wslproto.FrameEnd:
			q.close()
		case wslproto.FrameCancel:
			signal("cancelled")
		case wslproto.FrameHeader:
			signal("protocol error: a second header")
			return
		}
	}
}

// watchLease stops the run when no frame arrived for lease. It is
// sleep-aware: a gap between ticks far longer than the tick means the machine
// (and the WSL VM with it) was suspended, which is not a silent daemon.
func watchLease(lease time.Duration, alive <-chan struct{}, stop chan<- string) {
	tick := lease / 4
	if tick < 10*time.Millisecond {
		tick = 10 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	last := time.Now()
	lastTick := last
	for {
		select {
		case <-alive:
			last = time.Now()
		case now := <-t.C:
			if now.Sub(lastTick) > 3*tick {
				last = now
			}
			lastTick = now
			if now.Sub(last) > lease {
				select {
				case stop <- "no word from the daemon for " + lease.String():
				default:
				}
				return
			}
		}
	}
}

func waitCopies(done <-chan struct{}, d time.Duration) {
	deadline := time.After(d)
	for n := 0; n < 2; n++ {
		select {
		case <-done:
		case <-deadline:
			return
		}
	}
}

func exitCode(st *os.ProcessState) int {
	if st == nil {
		return ExitHelperFailed
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}
