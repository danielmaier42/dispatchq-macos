package systemd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnitContainsProgramRestartAndLogs(t *testing.T) {
	u, err := Unit(Config{
		BinPath: "/usr/lib/dispatchq/dispatchqd", Args: []string{"run"},
		StdoutPath: "/tmp/out.log", StderrPath: "/tmp/err.log",
	})
	if err != nil {
		t.Fatalf("Unit: %v", err)
	}
	for _, want := range []string{
		`ExecStart="/usr/lib/dispatchq/dispatchqd" "run"`,
		"Restart=always",
		// systemd must never stop restarting a daemon that keeps exiting. The
		// key only works in [Unit].
		"[Unit]\nDescription=DispatchQ background service\nDocumentation=https://www.dispatchq.de/\n" +
			"# Never give up restarting (see Restart below).\nStartLimitIntervalSec=0\n\n[Service]",
		"RestartMaxDelaySec=60",
		// The daemon winds its runs down itself (engine.ShutdownGrace).
		"KillMode=mixed",
		"TimeoutStopSec=60",
		"StandardOutput=append:/tmp/out.log",
		"StandardError=append:/tmp/err.log",
		"WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit missing %q\n%s", want, u)
		}
	}
}

func TestUnitEscapesSpecialCharacters(t *testing.T) {
	u, err := Unit(Config{
		BinPath:    `/home/a b/50% "q"/$x\dispatchqd`,
		Args:       []string{"run"},
		StdoutPath: "/home/a b/100%/out.log",
		StderrPath: "/tmp/err.log",
	})
	if err != nil {
		t.Fatalf("Unit: %v", err)
	}
	for _, want := range []string{
		`ExecStart="/home/a b/50%% \"q\"/$$x\\dispatchqd" "run"`,
		"StandardOutput=append:/home/a b/100%%/out.log",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit missing %q\n%s", want, u)
		}
	}
}

func TestUnitRejectsARelativeBinary(t *testing.T) {
	if _, err := Unit(Config{BinPath: "dispatchqd"}); err == nil {
		t.Fatal("Unit with a relative binary path: want an error")
	}
}

// fakeRunner records every command and fails the ones named in fail.
type fakeRunner struct {
	calls [][]string
	fail  map[string]bool
	out   map[string]string // canned output per command line
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	if f.fail[strings.Join(call, " ")] {
		return []byte("boom"), errors.New("exit status 1")
	}
	return []byte(f.out[strings.Join(call, " ")]), nil
}

func TestUnitSetsTheEnvironment(t *testing.T) {
	u, err := Unit(Config{
		BinPath: "/usr/bin/dispatchqd", Args: []string{"run"}, StdoutPath: "/o", StderrPath: "/e",
		Env: map[string]string{"DISPATCHQ_HOME": "/home/a b/100%/dq", "A": "1"},
	})
	if err != nil {
		t.Fatalf("Unit: %v", err)
	}
	// Sorted, quoted, and "%" escaped as in ExecStart.
	want := "ExecStart=\"/usr/bin/dispatchqd\" \"run\"\nEnvironment=\"A=1\"\nEnvironment=\"DISPATCHQ_HOME=/home/a b/100%%/dq\"\n"
	if !strings.Contains(u, want) {
		t.Fatalf("unit missing %q\n%s", want, u)
	}
}

func TestManagerEnvAndUserUnitDir(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	t.Setenv("XDG_CONFIG_HOME", "/shell/only") // the shell's value must not count
	show := "systemctl --user show-environment"
	cases := []struct{ env, want string }{
		{"HOME=/home/me\nXDG_CONFIG_HOME=/cfg\n", "/cfg/systemd/user"},
		{"HOME=/home/me\nDISPLAY=:0\n", "/home/me/.config/systemd/user"},
		{"XDG_CONFIG_HOME=relative\n", "/home/me/.config/systemd/user"},
	}
	for _, c := range cases {
		r := &fakeRunner{out: map[string]string{show: c.env}}
		got, err := UserUnitDir(context.Background(), r)
		if err != nil || got != c.want {
			t.Errorf("manager env %q: got (%q, %v), want %q", c.env, got, err, c.want)
		}
	}
	env, err := ManagerEnv(context.Background(), &fakeRunner{out: map[string]string{show: "A=1\nB=x=y\n\n"}})
	if err != nil || env["A"] != "1" || env["B"] != "x=y" || len(env) != 2 {
		t.Fatalf("ManagerEnv = %v, %v", env, err)
	}
	if _, err := ManagerEnv(context.Background(), &fakeRunner{fail: map[string]bool{show: true}}); err == nil {
		t.Fatal("ManagerEnv with a failing systemctl: want an error")
	}
}

func TestSessionRunnerWrapsTheCommand(t *testing.T) {
	r := &fakeRunner{}
	if _, err := (SessionRunner{Runner: r}).Run(context.Background(), "zenity", "--file-selection"); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemd-run", "--user", "--wait", "--pipe", "--quiet", "--collect", "--", "zenity", "--file-selection"}
	if !reflect.DeepEqual(r.calls[0], want) {
		t.Fatalf("call = %q\nwant   %q", r.calls[0], want)
	}
}

func TestInstallWritesTheUnitThenEnablesAndRestarts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "systemd", "user")
	r := &fakeRunner{}
	a := Agent{Runner: r, Dir: dir}
	if err := a.Install(context.Background(), "[Unit]\n"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, DefaultUnit))
	if err != nil || string(got) != "[Unit]\n" {
		t.Fatalf("unit file = %q, %v", got, err)
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", DefaultUnit},
		{"systemctl", "--user", "restart", DefaultUnit},
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v\nwant    %v", r.calls, want)
	}
}

func TestInstallReportsAFailingStep(t *testing.T) {
	r := &fakeRunner{fail: map[string]bool{"systemctl --user enable " + DefaultUnit: true}}
	err := Agent{Runner: r, Dir: t.TempDir()}.Install(context.Background(), "[Unit]\n")
	if err == nil || !strings.Contains(err.Error(), "enable") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the failing enable step and its output", err)
	}
}

func TestUninstallRemovesTheUnitEvenWhenTheServiceIsNotLoaded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultUnit)
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{fail: map[string]bool{"systemctl --user disable --now " + DefaultUnit: true}}
	if err := (Agent{Runner: r, Dir: dir}).Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unit file still there: %v", err)
	}
	if last := r.calls[len(r.calls)-1]; strings.Join(last, " ") != "systemctl --user daemon-reload" {
		t.Fatalf("last call = %v, want daemon-reload", last)
	}
	// A second uninstall finds nothing to remove and still succeeds.
	if err := (Agent{Runner: r, Dir: dir}).Uninstall(context.Background()); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}
