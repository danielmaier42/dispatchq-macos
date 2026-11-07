//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/store"
)

type uninstallRun struct {
	name string
	args []string
	// choices is the content of the -applyChoiceChangesXML file, read while
	// it still exists.
	choices string
}

func newUninstallEnv(t *testing.T, stdin string) (uninstallEnv, *bytes.Buffer, *[]uninstallRun) {
	t.Helper()
	out := &bytes.Buffer{}
	runs := &[]uninstallRun{}
	stageDir := t.TempDir()
	return uninstallEnv{
		exe: "/Applications/DispatchQ.app/Contents/MacOS/dispatchq", in: strings.NewReader(stdin), out: out,
		user: "dm", consoleUser: "dm",
		locate: func(string) (string, error) {
			return "/Applications/DispatchQ.app/Contents/Resources/Uninstall DispatchQ.pkg", nil
		},
		stage: func(string) (string, error) { return filepath.Join(stageDir, "Uninstall DispatchQ.pkg"), nil },
		run: func(name string, args ...string) error {
			r := uninstallRun{name: name, args: args}
			for i, a := range args {
				if a == "-applyChoiceChangesXML" {
					b, _ := os.ReadFile(args[i+1])
					r.choices = string(b)
				}
			}
			*runs = append(*runs, r)
			return nil
		},
	}, out, runs
}

func TestUninstallAsksFirst(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		run   bool
	}{
		{"no answer", nil, "", false},
		{"no", nil, "n\n", false},
		{"yes", nil, "y\n", true},
		{"--yes skips the question", []string{"--yes"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, out, runs := newUninstallEnv(t, tc.stdin)
			if err := runUninstall(tc.args, env); err != nil {
				t.Fatal(err)
			}
			if (len(*runs) == 1) != tc.run {
				t.Fatalf("runs = %v, want run=%v\n%s", *runs, tc.run, out)
			}
		})
	}
}

func TestUninstallRunsTheStagedPackage(t *testing.T) {
	for _, purge := range []bool{false, true} {
		env, _, runs := newUninstallEnv(t, "")
		args := []string{"--yes"}
		if purge {
			args = append(args, "--purge")
		}
		if err := runUninstall(args, env); err != nil {
			t.Fatal(err)
		}
		r := (*runs)[0]
		got := r.name + " " + strings.Join(r.args, " ")
		if !strings.HasPrefix(got, "sudo /usr/sbin/installer -pkg ") || !strings.Contains(got, " -target /") {
			t.Fatalf("ran %q", got)
		}
		if strings.Contains(got, "/Applications/DispatchQ.app") {
			t.Fatalf("ran the package from inside the bundle it deletes: %q", got)
		}
		if purge != strings.Contains(r.choices, "<string>data</string>") {
			t.Fatalf("purge=%v but choices = %q", purge, r.choices)
		}
	}
}

func TestUninstallOnlyForTheConsoleUser(t *testing.T) {
	for _, u := range []string{"root", "other"} {
		env, _, runs := newUninstallEnv(t, "")
		env.user = u
		if err := runUninstall([]string{"--yes"}, env); err == nil || len(*runs) != 0 {
			t.Fatalf("user %q: err = %v, runs = %v; want a refusal", u, err, *runs)
		}
	}
}

func TestUninstallPurgeWarnsAboutCustomHome(t *testing.T) {
	env, out, _ := newUninstallEnv(t, "")
	env.dataHome = "/Users/dm/dispatchq-data"
	if err := runUninstall([]string{"--yes", "--purge"}, env); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "delete /Users/dm/dispatchq-data yourself") {
		t.Fatalf("no DISPATCHQ_HOME warning:\n%s", out)
	}
}

func TestUninstallReportsInstallerFailure(t *testing.T) {
	env, _, _ := newUninstallEnv(t, "")
	env.run = func(string, ...string) error { return errors.New("exit status 1") }
	if err := runUninstall([]string{"--yes"}, env); err == nil {
		t.Fatal("a failed installer run was reported as success")
	}
}

// The uninstall must not open the store: opening it creates the data directory.
func TestUninstallDoesNotCreateDataDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "dispatchq")
	t.Setenv(store.EnvHome, home)
	_ = run([]string{"uninstall", "--no-such-flag"})
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("data directory was created (stat err = %v)", err)
	}
}
