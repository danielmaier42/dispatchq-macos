//go:build unix

package wslhelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/proctree"
	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// The test binary plays every program these tests need, chosen by DQ_ROLE:
// the helper itself (so its real code runs as a separate process with real
// pipes), and the programs it starts.
func TestMain(m *testing.M) {
	switch os.Getenv("DQ_ROLE") {
	case "helper":
		h := &Helper{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
			RegistryDir: os.Getenv("DQ_REGISTRY"), PasswdFile: "/nonexistent",
			Wslpath: func(context.Context, string) (string, error) { return "", errors.New("no wslpath here") }}
		os.Exit(h.Run(os.Args[1:]))
	case "echo":
		roleEcho()
	case "tree":
		roleTree()
	case "sleeper":
		roleSleeper()
	case "holder":
		roleHolder()
	}
	os.Exit(m.Run())
}

func roleEcho() {
	fmt.Printf("secret=%s args=%q\n", os.Getenv("DQ_SECRET"), os.Args[1:])
	fmt.Printf("wslenv=%s\n", os.Getenv("WSLENV"))
	in, _ := io.ReadAll(os.Stdin)
	fmt.Printf("stdin=%q\n", in)
	code, _ := strconv.Atoi(os.Getenv("DQ_EXIT"))
	os.Exit(code)
}

// roleTree starts a child in a process group of its own, which starts a
// grandchild in yet another group, writes all pids and waits.
func roleTree() {
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), "DQ_ROLE=sleeper", "DQ_DEPTH=1")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	fmt.Println("tree started")
	time.Sleep(time.Hour)
}

func roleSleeper() {
	pids := os.Getenv("DQ_PIDS")
	f, _ := os.OpenFile(pids, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	if os.Getenv("DQ_DEPTH") == "1" {
		g := exec.Command(os.Args[0])
		g.Env = append(os.Environ(), "DQ_ROLE=sleeper", "DQ_DEPTH=2")
		g.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		_ = g.Start()
	}
	time.Sleep(time.Hour)
}

// roleHolder leaves a grandchild holding its output open and exits.
func roleHolder() {
	g := exec.Command(os.Args[0])
	g.Env = append(os.Environ(), "DQ_ROLE=sleeper", "DQ_DEPTH=2")
	g.Stdout = os.Stdout
	_ = g.Start()
	fmt.Println("holder done")
	os.Exit(0)
}

type session struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bytes.Buffer
	errOut *bytes.Buffer
	done   chan error
}

func startHelper(t *testing.T, op string, hdr wslproto.Header, env ...string) *session {
	t.Helper()
	hdr.V, hdr.Op = wslproto.Version, op
	cmd := exec.Command(os.Args[0], op)
	cmd.Env = append(os.Environ(), append([]string{"DQ_ROLE=helper"}, env...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &session{t: t, cmd: cmd, stdin: stdin, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, done: make(chan error, 1)}
	cmd.Stdout, cmd.Stderr = s.out, s.errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.done <- cmd.Wait() }()
	raw, _ := json.Marshal(hdr)
	s.frame(wslproto.FrameHeader, raw)
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
	})
	return s
}

func (s *session) frame(typ byte, p []byte) {
	if err := wslproto.WriteFrame(s.stdin, typ, p); err != nil {
		s.t.Fatalf("frame %c: %v", typ, err)
	}
}

func (s *session) wait(d time.Duration) int {
	s.t.Helper()
	select {
	case err := <-s.done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		if err != nil {
			s.t.Fatalf("helper: %v", err)
		}
		return 0
	case <-time.After(d):
		s.t.Fatalf("helper did not finish within %s; stderr: %s", d, s.errOut)
		return -1
	}
}

func selfArgv(args ...string) []string { return append([]string{os.Args[0]}, args...) }

func TestRunRelaysOutputStdinEnvAndExitCode(t *testing.T) {
	s := startHelper(t, wslproto.OpRun, wslproto.Header{
		Argv:             selfArgv("it's", "%PATH%", "line\nbreak"),
		Env:              []string{"DQ_ROLE=echo", "DQ_SECRET=s3cr3t", "DQ_EXIT=7", "DISPATCHQ_HOME=C:\\data"},
		ShareWithWindows: []string{"DISPATCHQ_HOME", "bad;name"},
	})
	s.frame(wslproto.FrameData, []byte("prompt with \"quotes\" and ünïcode"))
	s.frame(wslproto.FrameEnd, nil)
	if code := s.wait(10 * time.Second); code != 7 {
		t.Fatalf("exit = %d, want the program's 7; stderr %s", code, s.errOut)
	}
	out := s.out.String()
	for _, want := range []string{"secret=s3cr3t", `"it's" "%PATH%" "line\nbreak"`, `stdin="prompt with \"quotes\" and ünïcode"`, "DISPATCHQ_HOME/w"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "bad;name") {
		t.Error("an invalid variable name reached WSLENV")
	}
	if strings.Contains(s.errOut.String(), "s3cr3t") {
		t.Error("the helper wrote a secret to stderr")
	}
}

// Control frames are never program input: a stream that carries data that
// looks like a control message reaches the program byte for byte, and only
// a real cancel frame stops it.
func TestStreamKeepsDataAndControlApart(t *testing.T) {
	s := startHelper(t, wslproto.OpRun, wslproto.Header{Argv: []string{"/bin/cat"}})
	payload := []byte("{\"op\":\"cancel\"}\nC\x00\x00\x00\x00P\n")
	s.frame(wslproto.FrameData, payload)
	s.frame(wslproto.FramePing, nil)
	s.frame(wslproto.FrameData, []byte("more"))
	s.frame(wslproto.FrameEnd, nil)
	if code := s.wait(10 * time.Second); code != 0 {
		t.Fatalf("exit = %d; stderr %s", code, s.errOut)
	}
	if got, want := s.out.String(), string(payload)+"more"; got != want {
		t.Fatalf("program read %q, want %q", got, want)
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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		// A killed child of a dead parent is a zombie until init reaps it.
		if out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output(); strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func killAll(pids []int) {
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

func TestTreeStops(t *testing.T) {
	cases := map[string]func(s *session){
		"cancel frame": func(s *session) { s.frame(wslproto.FrameCancel, nil) },
		"daemon gone (EOF)": func(s *session) {
			_ = s.stdin.Close()
		},
		"daemon silent (lease)": func(*session) {},
		"broken channel":        func(s *session) { _, _ = s.stdin.Write([]byte{'Z', 0, 0}) },
	}
	for name, stop := range cases {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids")
			s := startHelper(t, wslproto.OpRun, wslproto.Header{
				Argv: selfArgv(), Env: []string{"DQ_ROLE=tree", "DQ_PIDS=" + pidFile}, LeaseMS: 600,
			})
			if name != "daemon silent (lease)" {
				s.frame(wslproto.FramePing, nil)
			}
			pids := pidsOf(t, pidFile, 2)
			defer killAll(pids)
			stop(s)
			s.wait(10 * time.Second)
			for _, p := range pids {
				if !gone(p) {
					t.Errorf("descendant %d survived", p)
				}
			}
		})
	}
}

// Pings within the lease keep a run alive past it.
func TestPingsKeepTheRunAlive(t *testing.T) {
	s := startHelper(t, wslproto.OpRun, wslproto.Header{Argv: []string{"/bin/cat"}, LeaseMS: 300})
	for i := 0; i < 8; i++ {
		time.Sleep(100 * time.Millisecond)
		s.frame(wslproto.FramePing, nil)
	}
	select {
	case <-s.done:
		t.Fatalf("the run stopped although the daemon kept pinging; stderr %s", s.errOut)
	default:
	}
	s.frame(wslproto.FrameEnd, nil)
	s.wait(5 * time.Second)
}

func TestHeldOutputReleasesTheRun(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids")
	s := startHelper(t, wslproto.OpRun, wslproto.Header{
		Argv: selfArgv(), Env: []string{"DQ_ROLE=holder", "DQ_PIDS=" + pidFile}, PipeReleaseMS: 300,
	})
	go func() {
		for i := 0; i < 50; i++ {
			time.Sleep(100 * time.Millisecond)
			if wslproto.WriteFrame(s.stdin, wslproto.FramePing, nil) != nil {
				return
			}
		}
	}()
	pids := pidsOf(t, pidFile, 1)
	defer killAll(pids)
	if code := s.wait(5 * time.Second); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(s.out.String(), "holder done") {
		t.Fatalf("output %q", s.out)
	}
	// A process the program left running on purpose is not killed.
	if syscall.Kill(pids[0], 0) != nil {
		t.Fatal("the helper killed a process the program left running after a normal exit")
	}
}

func TestHelperRefusesBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		op  string
		hdr wslproto.Header
	}{
		"op mismatch":   {"run", wslproto.Header{Op: "read"}},
		"no program":    {"run", wslproto.Header{}},
		"relative cwd":  {"run", wslproto.Header{Argv: []string{"/bin/cat"}, Cwd: "repo"}},
		"missing prog":  {"run", wslproto.Header{Argv: []string{"no-such-program-dq"}}},
		"windows match": {"run", wslproto.Header{Argv: []string{"/x/claude.exe"}}},
	} {
		t.Run(name, func(t *testing.T) {
			hdr := tc.hdr
			if hdr.Op == "" {
				hdr.Op = tc.op
			}
			cmd := exec.Command(os.Args[0], tc.op)
			cmd.Env = append(os.Environ(), "DQ_ROLE=helper")
			var in bytes.Buffer
			hdr.V = wslproto.Version
			raw, _ := json.Marshal(hdr)
			_ = wslproto.WriteFrame(&in, wslproto.FrameHeader, raw)
			cmd.Stdin = &in
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != ExitHelperFailed {
				t.Fatalf("err = %v, want exit %d", err, ExitHelperFailed)
			}
		})
	}
}

func TestLookPathSkipsWindowsPrograms(t *testing.T) {
	dir := t.TempDir()
	mnt := filepath.Join(dir, "mnt") + "/"
	winDir := filepath.Join(mnt, "c", "npm")
	linDir := filepath.Join(dir, "home", "bin")
	for _, d := range []string{winDir, linDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(winDir, "claude"), filepath.Join(linDir, "claude.exe"), filepath.Join(linDir, "claude")} {
		if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture
			t.Fatal(err)
		}
	}
	h := &Helper{}
	got, err := h.lookPath("claude", []string{"PATH=" + winDir + ":" + linDir}, mnt)
	if err != nil || got != filepath.Join(linDir, "claude") {
		t.Fatalf("lookPath = %q, %v; want the Linux one", got, err)
	}
	if _, err := h.lookPath("claude", []string{"PATH=" + winDir}, mnt); err == nil {
		t.Fatal("a program under the Windows mount was accepted")
	}
}

func TestReadIsLimited(t *testing.T) {
	f := filepath.Join(t.TempDir(), "creds")
	if err := os.WriteFile(f, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Helper{}
	if r := h.read(f, 4); string(r.Data) != "0123" {
		t.Fatalf("read = %+v", r)
	}
	if r := h.read(f+".missing", 4); !r.NotFound {
		t.Fatalf("missing file = %+v, want NotFound", r)
	}
}

func TestUsersFromPasswd(t *testing.T) {
	f := filepath.Join(t.TempDir(), "passwd")
	_ = os.WriteFile(f, []byte("root:x:0:0::/root:/bin/bash\ndm:x:1000:1000::/home/dm:/bin/bash\nsvc:x:1001:1001::/:/usr/sbin/nologin\nnobody:x:65534:65534::/:/bin/sh\nann:x:1002:1002::/home/ann:/bin/zsh\n"), 0o600)
	h := &Helper{PasswdFile: f}
	if got := strings.Join(h.users().Users, ","); got != "dm,ann" {
		t.Fatalf("users = %s", got)
	}
}

func TestRegistryDirMustBePrivate(t *testing.T) {
	base := t.TempDir()
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil { //nolint:gosec // the unsafe case under test
		t.Fatal(err)
	}
	_ = os.Chmod(open, 0o777) //nolint:gosec // the unsafe case under test
	link := filepath.Join(base, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{open, link} {
		if _, err := (&Helper{RegistryDir: dir}).registryDir(); err == nil {
			t.Errorf("%s was accepted as a private registry", dir)
		}
	}
}

// startSleeper starts a process outside any helper, as an unrelated or
// orphaned tree would be.
func startSleeper(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "DQ_ROLE=sleeper", "DQ_DEPTH=2", "DQ_PIDS="+filepath.Join(t.TempDir(), "p"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() { _ = cmd.Wait() }()
	start, err := proctree.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid, start
}

func writeRecord(t *testing.T, dir string, r record) {
	t.Helper()
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("run-%d-%d.json", r.HelperPID, r.PID)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReapStopsOnlyProvablyOrphanedTreesOfItsOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reg")
	h := &Helper{RegistryDir: dir}
	if _, err := h.registryDir(); err != nil {
		t.Fatal(err)
	}
	selfStart, err := proctree.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	deadHelper := 0x7ffffff0 // no such process

	orphan, orphanStart := startSleeper(t)
	foreign, foreignStart := startSleeper(t)
	reused, _ := startSleeper(t)
	guarded, guardedStart := startSleeper(t)

	writeRecord(t, dir, record{Owner: "me", HelperPID: deadHelper, HelperStart: "x", PID: orphan, Start: orphanStart})
	writeRecord(t, dir, record{Owner: "other-install", HelperPID: deadHelper, HelperStart: "x", PID: foreign, Start: foreignStart})
	writeRecord(t, dir, record{Owner: "me", HelperPID: deadHelper, HelperStart: "x", PID: reused, Start: "an earlier process with this pid"})
	writeRecord(t, dir, record{Owner: "me", HelperPID: os.Getpid(), HelperStart: selfStart, PID: guarded, Start: guardedStart})

	res := h.reap("me")
	if res.Error != "" || res.Reaped != 1 {
		t.Fatalf("reap = %+v, want exactly one tree stopped", res)
	}
	if !gone(orphan) {
		t.Error("the orphan of a dead helper survived")
	}
	for name, pid := range map[string]int{"another installation's run": foreign, "a reused pid": reused, "a run whose helper is alive": guarded} {
		if syscall.Kill(pid, 0) != nil {
			t.Errorf("reap killed %s", name)
		}
	}
}
