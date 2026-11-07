package provider

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/store"
)

// tripwireWSL is a WSL service that fails the test on any call. Native code
// paths run against a registry that has it must never reach it.
type tripwireWSL struct{ t *testing.T }

func (w tripwireWSL) fail(what string) { w.t.Helper(); w.t.Errorf("native path reached WSL: %s", what) }
func (w tripwireWSL) Available() bool  { return true }
func (w tripwireWSL) Distros(context.Context) ([]WSLDistro, error) {
	w.fail("Distros")
	return nil, errors.New("tripwire")
}
func (w tripwireWSL) Users(context.Context, string) ([]string, error) {
	w.fail("Users")
	return nil, errors.New("tripwire")
}
func (w tripwireWSL) Detect(context.Context, Execution, Kind) (WSLDetection, error) {
	w.fail("Detect")
	return WSLDetection{}, errors.New("tripwire")
}
func (w tripwireWSL) Host(Execution) Host { w.fail("Host"); return NativeHost{} }
func (w tripwireWSL) Reap(context.Context, Execution) error {
	w.fail("Reap")
	return nil
}

func TestHostForNativeNeverAsksWSL(t *testing.T) {
	reg := NewRegistry()
	reg.SetWSL(tripwireWSL{t})
	h, err := reg.HostFor(Instance{ID: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.(NativeHost); !ok {
		t.Fatalf("host = %T, want NativeHost", h)
	}
}

func TestHostForRefusesWhatItCannotRun(t *testing.T) {
	reg := NewRegistry() // no WSL service: macOS, Linux, or a test
	cases := map[string]Execution{
		"wsl without WSL":           {Type: ExecWSL, Distro: "Ubuntu"},
		"unknown type":              {Type: "docker", Distro: "x"},
		"distro without type":       {Distro: "Ubuntu"},
		"user without type or name": {User: "dm"},
	}
	for name, ex := range cases {
		t.Run(name, func(t *testing.T) {
			if ex.IsNative() {
				t.Fatal("a non-empty execution table read as native")
			}
			if _, err := reg.HostFor(Instance{ID: "p", Execution: ex}); err == nil {
				t.Fatal("HostFor ran an instance it cannot run instead of refusing")
			}
		})
	}
}

func TestExecutionSurvivesTheStoreShape(t *testing.T) {
	inst := Instance{ID: "w", Kind: KindClaudeCode, Execution: Execution{Type: ExecWSL, Distro: "Ubuntu", User: "dm"}}
	if got := InstanceOf(inst.Stored()); got.Execution != inst.Execution {
		t.Fatalf("round trip = %+v, want %+v", got.Execution, inst.Execution)
	}
	if (Instance{ID: "n"}).Stored().Execution != nil {
		t.Fatal("a native instance is stored with an execution table")
	}
	if got := InstanceOf(store.Provider{ID: "odd", Execution: &store.ProviderExecution{Distro: "Ubuntu"}}); got.Execution.IsNative() {
		t.Fatal("a typeless table with a distro was read as native")
	}
}

func TestValidateExecution(t *testing.T) {
	plain := NewRegistry(&fakeAdapter{kind: "fake"})
	withWSL := NewRegistry(&fakeAdapter{kind: "fake"})
	withWSL.SetWSL(tripwireWSL{t})
	wsl := func(distro, user, bin string) Instance {
		return Instance{ID: "w", Kind: "fake", BinaryPath: bin, Execution: Execution{Type: ExecWSL, Distro: distro, User: user}}
	}
	cases := []struct {
		name    string
		reg     *Registry
		inst    Instance
		wantErr string
	}{
		{"native unchanged", plain, Instance{ID: "n", Kind: "fake"}, ""},
		{"wsl where WSL does not exist", plain, wsl("Ubuntu", "", ""), "only in DispatchQ for Windows"},
		{"wsl ok", withWSL, wsl("Ubuntu-24.04", "dm", "/home/dm/.local/bin/claude"), ""},
		{"no distro", withWSL, wsl("", "", ""), "needs a distribution"},
		{"option-like distro", withWSL, wsl("--shutdown", "", ""), "may not start with '-'"},
		{"control in distro", withWSL, wsl("Ubu\nntu", "", ""), "cannot have"},
		{"bad user", withWSL, wsl("Ubuntu", "-root", ""), "not a valid Linux user name"},
		{"shell user", withWSL, wsl("Ubuntu", "dm;rm", ""), "not a valid Linux user name"},
		{"windows binary path", withWSL, wsl("Ubuntu", "", `C:\claude.exe`), "must be absolute"},
		{"tilde binary path", withWSL, wsl("Ubuntu", "", "~/.local/bin/claude"), "must be absolute"},
		{"unknown type", withWSL, Instance{ID: "x", Kind: "fake", Execution: Execution{Type: "vm"}}, "unknown execution environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.reg, tc.inst)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// The native host builds a run exactly as the executor did before hosts
// existed: the daemon's environment, the run variables, their legacy names,
// then the adapter's additions; the stored directory; the prompt on stdin.
func TestNativeHostRunCommandIsTheOldOne(t *testing.T) {
	t.Setenv("DISPATCHQ_CLAUDE_BIN", "/x/claude")
	vars := []string{"DISPATCHQ_HOME=/data", "DISPATCHQ_RUN_ID=r1"}
	adapterEnv := []string{"CLAUDE_CONFIG_DIR=/acct"}
	proc, err := NativeHost{}.Command(context.Background(), Command{Path: "/bin/echo", Args: []string{"a b"}, Env: adapterEnv, Stdin: "prompt"},
		CmdOptions{Dir: "/repo", Vars: vars})
	if err != nil {
		t.Fatal(err)
	}
	var old []string
	old = append(old, os.Environ()...)
	old = append(old, vars...)
	var legacy []string
	for _, kv := range old {
		if rest, ok := strings.CutPrefix(kv, "DISPATCHQ_"); ok {
			legacy = append(legacy, "CLAUDEQ_"+rest)
		}
	}
	old = append(append(old, legacy...), adapterEnv...)
	if !reflect.DeepEqual(proc.Cmd.Env, old) {
		t.Fatalf("env differs from the pre-host composition")
	}
	if proc.Cmd.Dir != "/repo" || proc.Cmd.Path != "/bin/echo" || !reflect.DeepEqual(proc.Cmd.Args, []string{"/bin/echo", "a b"}) {
		t.Fatalf("cmd = %q %q in %q", proc.Cmd.Path, proc.Cmd.Args, proc.Cmd.Dir)
	}
	if proc.Interrupt != nil || proc.Stdin != nil {
		t.Fatal("a native run grew an interrupt or an open stdin")
	}
}

func TestNativeHostFiles(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/f"
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NativeHost{}
	ctx := context.Background()
	if err := h.StatDir(ctx, dir); err != nil {
		t.Fatalf("StatDir(dir) = %v", err)
	}
	if err := h.StatDir(ctx, file); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("StatDir(file) = %v, want ErrNotDirectory", err)
	}
	if got, err := h.ReadFile(ctx, file, 4); err != nil || string(got) != "0123" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if got, _ := h.WorkDir(ctx, `C:\repo`); got != `C:\repo` {
		t.Fatalf("WorkDir changed a native path: %q", got)
	}
}
