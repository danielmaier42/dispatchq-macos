//go:build unix

package execenv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/wslhelper"
)

// These tests run the daemon side against a fake wsl.exe: the test binary
// itself, which checks and records its argument list the way wsl.exe parses
// it, then runs the real helper code (wslhelper) or the program --exec names,
// with real processes and pipes. Only wsl.exe's own translation is faked:
// `wslpath -u` and the drive mount live under a temporary folder.

const (
	envRole   = "EXECENV_ROLE"
	envLog    = "EXECENV_ARGV_LOG"
	envUsers  = "EXECENV_USERS"
	envPids   = "EXECENV_PIDS"
	envListUT = "EXECENV_LIST_UTF16"
)

func TestMain(m *testing.M) {
	switch os.Getenv(envRole) {
	case "wsl":
		os.Exit(fakeWSL(os.Args[1:]))
	case "tree":
		roleTree()
	case "sleeper":
		roleSleeper()
	}
	os.Exit(m.Run())
}

// fakeWSL parses the options DispatchQ uses, in wsl.exe's grammar.
func fakeWSL(args []string) int {
	if f, err := os.OpenFile(os.Getenv(envLog), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600); err == nil {
		raw, _ := json.Marshal(args)
		_, _ = fmt.Fprintf(f, "%s\n", raw)
		_ = f.Close()
	}
	if os.Getenv("WSLENV") != "" {
		fmt.Fprintln(os.Stderr, "fake wsl: WSLENV reached wsl.exe")
		return 2
	}
	if len(args) >= 1 && args[0] == "--list" {
		names := "Ubuntu-24.04\r\nDebian\r\n"
		if len(args) > 1 && args[1] == "--verbose" {
			names = "  NAME            STATE           VERSION\r\n* Ubuntu-24.04    Running         2\r\n  Debian          Stopped         1\r\n"
		}
		if os.Getenv(envListUT) != "" {
			u := utf16.Encode([]rune(names))
			b := make([]byte, 0, 2*len(u))
			for _, c := range u {
				b = append(b, byte(c), byte(c>>8))
			}
			_, _ = os.Stdout.Write(b)
		} else {
			fmt.Print(names)
		}
		return 0
	}
	var distro, user, cd string
	i := 0
	for ; i < len(args); i++ {
		switch args[i] {
		case "--distribution", "-d":
			i++
			distro = args[i]
		case "--user", "-u":
			i++
			user = args[i]
		case "--cd":
			i++
			cd = args[i]
		case "--exec", "-e":
			i++
			goto exec
		default:
			fmt.Fprintf(os.Stderr, "fake wsl: unexpected option %q\n", args[i])
			return 2
		}
	}
	fmt.Fprintln(os.Stderr, "fake wsl: no --exec")
	return 2
exec:
	if distro != "Ubuntu-24.04" {
		fmt.Fprintf(os.Stderr, "There is no distribution with the supplied name.\n")
		return 1
	}
	if user != "" && !strings.Contains(","+os.Getenv(envUsers)+",", ","+user+",") {
		fmt.Fprintf(os.Stderr, "User not found.\n")
		return 1
	}
	if cd != "/" {
		fmt.Fprintf(os.Stderr, "fake wsl: --cd %q\n", cd)
		return 2
	}
	_ = os.Chdir("/")
	prog, rest := args[i], args[i+1:]
	switch {
	case prog == "/usr/bin/uname":
		fmt.Println(map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH])
		return 0
	case prog == "/usr/bin/wslpath":
		fmt.Println(rest[len(rest)-1]) // the test's helper path is already a Unix path
		return 0
	case strings.HasPrefix(filepath.Base(prog), "dispatchq-wsl-linux-"):
		os.Args = append([]string{prog}, rest...)
		return wslhelper.Main()
	}
	fmt.Fprintf(os.Stderr, "fake wsl: unknown program %s\n", prog)
	return 2
}

func roleTree() {
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), envRole+"=sleeper", "DQ_DEPTH=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if child.Start() != nil {
		os.Exit(3)
	}
	fmt.Println("tree started")
	time.Sleep(time.Hour)
}

func roleSleeper() {
	f, _ := os.OpenFile(os.Getenv(envPids), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	if os.Getenv("DQ_DEPTH") == "1" {
		g := exec.Command(os.Args[0])
		g.Env = append(os.Environ(), envRole+"=sleeper", "DQ_DEPTH=2")
		g.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		_ = g.Start()
	}
	time.Sleep(time.Hour)
}

type fixture struct {
	svc   *Service
	log   string
	mount string // the fake drive mount root, with a trailing slash
	tmp   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tmp := t.TempDir()
	if r, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = r
	}
	bin := filepath.Join(tmp, "bin")
	mount := filepath.Join(tmp, "mnt") + "/"
	helperDir := filepath.Join(tmp, "Program Files", "DispatchQ", "wsl")
	run := filepath.Join(tmp, "run")
	for _, d := range []string{bin, mount + "c/repo", helperDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	// wslpath as the helper calls it, for the drive mount root.
	write(t, filepath.Join(bin, "wslpath"), "#!/bin/sh\ncase \"$2\" in 'C:\\') echo '"+mount+"c';; *) exit 1;; esac\n", 0o755)
	// A login shell that runs the helper's fixed script without any profile.
	write(t, filepath.Join(bin, "loginsh"), "#!/bin/sh\nexec /bin/sh -c \"$2\"\n", 0o755)
	// The provider CLI the instance names by its bare name.
	write(t, filepath.Join(bin, "claude"), "#!/bin/sh\necho claude 2.1.0\n", 0o755)
	write(t, filepath.Join(helperDir, helperName(runtime.GOARCH)), "not used: the fake wsl.exe runs the helper code itself\n", 0o755)
	log := filepath.Join(tmp, "argv.log")
	env := []string{
		envRole + "=wsl", envLog + "=" + log, envUsers + "=alice",
		"PATH=" + bin + ":/usr/bin:/bin", "SHELL=" + filepath.Join(bin, "loginsh"),
		"HOME=" + tmp, "XDG_RUNTIME_DIR=" + run,
		"WSLENV=SHOULD_NOT_PASS", "TMPDIR=" + tmp,
	}
	svc := &Service{
		Exe:       os.Args[0],
		HelperDir: helperDir,
		Owner:     OwnerFor(filepath.Join(tmp, "data")),
		VerifyHelper: func(p, arch string) error {
			if arch != runtime.GOARCH {
				return fmt.Errorf("arch %s", arch)
			}
			_, err := os.Stat(p)
			return err
		},
		Environ: func() []string { return env },
	}
	return &fixture{svc: svc, log: log, mount: mount, tmp: tmp}
}

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

// argv returns every wsl.exe argument list the fixture saw.
func (f *fixture) argv(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(f.log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if l == "" {
			continue
		}
		var a []string
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// assertNoData fails when any wsl.exe argument holds one of the secrets.
func (f *fixture) assertNoData(t *testing.T, secrets ...string) {
	t.Helper()
	for _, a := range f.argv(t) {
		joined := strings.Join(a, "\x00")
		for _, s := range secrets {
			if strings.Contains(joined, s) {
				t.Errorf("wsl.exe argv %q carries data %q", a, s)
			}
		}
	}
}

var ubuntu = provider.Execution{Type: provider.ExecWSL, Distro: "Ubuntu-24.04"}

func sh(script string) provider.Command {
	return provider.Command{Path: "/bin/sh", Args: []string{"-c", script}}
}

func TestRunCarriesArgsEnvStdinOnlyInFrames(t *testing.T) {
	f := newFixture(t)
	h := f.svc.Host(ubuntu)
	const secret, prompt = "sk-ant-SECRET-123", "Fix the bug; rm -rf / %PATH% !x \"q\" 'q' ünï\nline 2"
	c := sh(`printf 'key=%s\n' "$ANTHROPIC_API_KEY"; printf 'arg=%s\n' "$0"; printf 'pwd=%s\n' "$(pwd)"; printf 'stdin='; cat; printf '\nbin=%s home=%s mnt=%s\nwslenv=%s\n' "$DISPATCHQ_BIN" "$DISPATCHQ_HOME" "$DISPATCHQ_WSL_MNT" "$WSLENV"; exit 7`)
	c.Args = append(c.Args, prompt)
	c.Env = []string{"ANTHROPIC_API_KEY=" + secret}
	c.Stdin = prompt
	dir, err := h.WorkDir(context.Background(), `C:\repo`)
	if err != nil {
		t.Fatal(err)
	}
	if want := f.mount + "c/repo"; dir != want {
		t.Fatalf("WorkDir = %q, want %q", dir, want)
	}
	proc, err := h.Command(context.Background(), c, provider.CmdOptions{Dir: dir, Vars: []string{
		`DISPATCHQ_HOME=C:\Users\dm\AppData\Local\DispatchQ`, `DISPATCHQ_BIN=C:\Program Files\DispatchQ\dispatchq.exe`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := proc.Cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("exit = %v, want 7; output %s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		"key=" + secret, "arg=" + prompt, "pwd=" + f.mount + "c/repo", "stdin=" + prompt,
		"bin=" + f.mount + "c/Program Files/DispatchQ/dispatchq.exe",
		`home=C:\Users\dm\AppData\Local\DispatchQ`, "mnt=" + f.mount,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "DISPATCHQ_HOME/w") || !strings.Contains(got, "WSL_DISTRO_NAME/w") || strings.Contains(got, "ANTHROPIC_API_KEY/w") {
		t.Errorf("WSLENV shares the wrong names:\n%s", got)
	}
	f.assertNoData(t, secret, "Fix the bug", "dispatchq.exe", "AppData", "c/repo")
}

func TestUserIsPassedAsItsOwnArgument(t *testing.T) {
	f := newFixture(t)
	alice := ubuntu
	alice.User = "alice"
	if _, err := f.svc.Host(alice).Probe(context.Background(), sh("echo hi")); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.argv(t) {
		if len(a) < 4 || a[2] != "--user" || a[3] != "alice" {
			t.Errorf("argv %q does not run as alice", a)
		}
	}
	bob := ubuntu
	bob.User = "bob"
	_, err := f.svc.Host(bob).Probe(context.Background(), sh("echo hi"))
	if err == nil || !strings.Contains(err.Error(), "User not found") {
		t.Fatalf("an unknown user = %v, want wsl.exe's message", err)
	}
}

func TestUnknownDistroIsAnActionableError(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Host(provider.Execution{Type: provider.ExecWSL, Distro: "Gone"}).StatDir(context.Background(), "/")
	if err == nil || !strings.Contains(err.Error(), "no distribution") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileQuestions(t *testing.T) {
	f := newFixture(t)
	h := f.svc.Host(ubuntu)
	ctx := context.Background()
	cfg := filepath.Join(f.tmp, ".claude")
	write(t, filepath.Join(f.tmp, "creds.json"), `{"token":"x"}`, 0o600)
	_ = os.Mkdir(cfg, 0o700)
	if err := h.StatDir(ctx, cfg); err != nil {
		t.Errorf("StatDir(dir) = %v", err)
	}
	if err := h.StatDir(ctx, filepath.Join(f.tmp, "creds.json")); !errors.Is(err, provider.ErrNotDirectory) {
		t.Errorf("StatDir(file) = %v", err)
	}
	if err := h.StatDir(ctx, filepath.Join(f.tmp, "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("StatDir(missing) = %v", err)
	}
	if data, err := h.ReadFile(ctx, "~/creds.json", 5); err != nil || string(data) != `{"tok` {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if err := h.Executable(ctx, "claude"); err != nil {
		t.Errorf("Executable(bare name) = %v", err)
	}
	if err := h.Executable(ctx, filepath.Join(f.tmp, "creds.json")); err == nil {
		t.Error("a non-executable file passed")
	}
	d, err := f.svc.Detect(ctx, ubuntu, provider.KindClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binary != filepath.Join(f.tmp, "bin", "claude") || d.Home != f.tmp || d.Arch != runtime.GOARCH {
		t.Errorf("Detect = %+v", d)
	}
	if out, err := h.Probe(ctx, provider.Command{Path: "claude", Args: []string{"--version"}}); err != nil || !strings.Contains(string(out), "claude 2.1.0") {
		t.Errorf("Probe = %q, %v", out, err)
	}
	if out, err := h.Output(ctx, sh("pwd; echo warn >&2"), ""); err != nil || strings.TrimSpace(string(out)) != filepath.Join(f.tmp, ".cache", "dispatchq", "aside") {
		t.Errorf("Output = %q, %v", out, err)
	}
}

func TestDistrosInEitherEncoding(t *testing.T) {
	for _, utf16 := range []bool{false, true} {
		f := newFixture(t)
		if utf16 {
			env := f.svc.Environ()
			f.svc.Environ = func() []string { return append(env, envListUT+"=1") }
		}
		got, err := f.svc.Distros(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := []provider.WSLDistro{{Name: "Ubuntu-24.04", Default: true, State: "Running", Version: 2}, {Name: "Debian", State: "Stopped", Version: 1}}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("utf16=%v: Distros = %+v, want %+v", utf16, got, want)
		}
	}
}

// A conversation's input travels as data frames; cancel and pings never reach
// the program, and closing the input ends it.
func TestStreamConversation(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := f.svc.Host(ubuntu).Command(ctx, sh(`while IFS= read -r l; do echo "got:$l"; done; echo eof`), provider.CmdOptions{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := proc.Cmd.StdoutPipe()
	if err := proc.Cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	big := strings.Repeat("x", 200<<10) // crosses several data frames
	for _, l := range []string{`{"id":1}`, big} {
		if _, err := io.WriteString(proc.Stdin, l+"\n"); err != nil {
			t.Fatal(err)
		}
		if !sc.Scan() || sc.Text() != "got:"+l {
			t.Fatalf("reply %.40q", sc.Text())
		}
	}
	_ = proc.Stdin.Close()
	if !sc.Scan() || sc.Text() != "eof" {
		t.Fatalf("after close: %q", sc.Text())
	}
	if err := proc.Cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func pidsOf(t *testing.T, file string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(file)
		var pids []int
		for _, l := range strings.Fields(string(raw)) {
			if p, err := strconv.Atoi(l); err == nil {
				pids = append(pids, p)
			}
		}
		if len(pids) >= n {
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the tree did not start")
	return nil
}

func gone(pid int) bool {
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func killAll(pids []int) {
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

func treeCommand(pidFile string) provider.Command {
	return provider.Command{Path: os.Args[0], Env: []string{envRole + "=tree", envPids + "=" + pidFile}}
}

// Cancel reaches the Linux descendants in their own process groups, through
// the cancel frame alone: the hard stop of wsl.exe is not what stops them.
func TestInterruptStopsTheLinuxTree(t *testing.T) {
	f := newFixture(t)
	pidFile := filepath.Join(f.tmp, "pids")
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := f.svc.Host(ubuntu).Command(ctx, treeCommand(pidFile), provider.CmdOptions{Vars: []string{"DISPATCHQ_TASK_ID=t1"}})
	if err != nil {
		t.Fatal(err)
	}
	hardStops := 0
	proc.Cmd.Cancel = func() error {
		proc.Interrupt()
		hardStops++
		return nil // no hard stop: the interrupt must be enough
	}
	if err := proc.Cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pids := pidsOf(t, pidFile, 2)
	defer killAll(pids)
	cancel()
	done := make(chan error, 1)
	go func() { done <- proc.Cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("wsl.exe did not end after the interrupt")
	}
	for _, p := range pids {
		if !gone(p) {
			t.Errorf("Linux descendant %d survived the cancel", p)
		}
	}
	if hardStops != 1 {
		t.Errorf("hard stops = %d", hardStops)
	}
}

// A daemon that dies closes wsl.exe's input; the helper stops the tree.
func TestDaemonGoneStopsTheLinuxTree(t *testing.T) {
	f := newFixture(t)
	pidFile := filepath.Join(f.tmp, "pids")
	// A conversation exposes the channel, so the test can drop the daemon's
	// end of it the way process death does, without a cancel frame.
	proc, err := f.svc.Host(ubuntu).Command(context.Background(), treeCommand(pidFile), provider.CmdOptions{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pids := pidsOf(t, pidFile, 2)
	defer killAll(pids)
	_ = proc.Stdin.(*frameStdin).conn.w.Close()
	done := make(chan error, 1)
	go func() { done <- proc.Cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the helper did not end after the daemon's channel closed")
	}
	for _, p := range pids {
		if !gone(p) {
			t.Errorf("Linux descendant %d survived the daemon", p)
		}
	}
}

// Reap stops a tree whose helper was killed outright, only for its owner.
func TestReapAfterTheHelperWasKilled(t *testing.T) {
	f := newFixture(t)
	pidFile := filepath.Join(f.tmp, "pids")
	proc, err := f.svc.Host(ubuntu).Command(context.Background(), treeCommand(pidFile), provider.CmdOptions{Vars: []string{"DISPATCHQ_TASK_ID=t1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pids := pidsOf(t, pidFile, 2)
	defer killAll(pids)
	// The fake wsl.exe is the helper process: SIGKILL gives it no chance to
	// stop its tree, as when Windows ends wsl.exe and the relay drops.
	_ = proc.Cmd.Process.Signal(syscall.SIGKILL)
	_ = proc.Cmd.Wait()
	time.Sleep(200 * time.Millisecond)

	other := &Service{Exe: f.svc.Exe, HelperDir: f.svc.HelperDir, Owner: OwnerFor("/another/installation"),
		VerifyHelper: f.svc.VerifyHelper, Environ: f.svc.Environ}
	if err := other.Reap(context.Background(), ubuntu); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(pids[0], 0) != nil {
		t.Fatal("another installation's reap stopped this installation's tree")
	}
	if err := f.svc.Reap(context.Background(), ubuntu); err != nil {
		t.Fatal(err)
	}
	if !gone(pids[0]) {
		t.Error("the orphaned tree survived its owner's reap")
	}
}
