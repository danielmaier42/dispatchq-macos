package claudecode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/binpath"
)

// fakeProber answers the adapter's probes from a script keyed by the first
// argument, so a readiness check is exercised without an installed CLI.
type fakeProber struct {
	out  map[string][]byte
	err  map[string]error
	cmds []provider.Command
}

func (f *fakeProber) Probe(_ context.Context, c provider.Command) ([]byte, error) {
	f.cmds = append(f.cmds, c)
	key := ""
	if len(c.Args) > 0 {
		key = c.Args[0]
	}
	return f.out[key], f.err[key]
}

func loggedIn() *fakeProber {
	return &fakeProber{out: map[string][]byte{
		"--version": []byte("2.1.7 (Claude Code)\n"),
		"auth":      []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"me@example.com"}`),
	}}
}

// fakeBinary writes an executable stand-in so the adapter's filesystem checks
// see a real, runnable file.
func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write fake binary: %v", err)
	}
	return p
}

func newAdapter(detected string) *Adapter {
	return &Adapter{detect: func() string { return detected }}
}

func TestCheckHealthReady(t *testing.T) {
	bin := fakeBinary(t)
	p := loggedIn()
	h := newAdapter("").CheckHealth(context.Background(), provider.Instance{
		ID: "claude", Kind: provider.KindClaudeCode, BinaryPath: bin, Enabled: true,
	}, provider.NativeHost{Prober: p})

	if h.State != provider.HealthReady {
		t.Fatalf("state = %q (%s), want ready", h.State, h.Reason)
	}
	if h.Binary != bin {
		t.Fatalf("binary = %q, want the configured path", h.Binary)
	}
	if !strings.Contains(h.Detail, "2.1.7") {
		t.Fatalf("detail = %q, want it to name the CLI version", h.Detail)
	}
	if len(p.cmds) != 2 || p.cmds[0].Args[0] != "--version" ||
		strings.Join(p.cmds[1].Args, " ") != "auth status --json" {
		t.Fatalf("probes = %+v, want a version and a login-status question", p.cmds)
	}
}

// TestCheckHealthNeverLeaksAccountDetails guards the one thing the login-status
// command hands back that must not travel: the account it belongs to.
func TestCheckHealthNeverLeaksAccountDetails(t *testing.T) {
	p := loggedIn()
	h := newAdapter("").CheckHealth(context.Background(), provider.Instance{
		ID: "claude", Kind: provider.KindClaudeCode, BinaryPath: fakeBinary(t), Enabled: true,
	}, provider.NativeHost{Prober: p})
	for _, field := range []string{h.Reason, h.Detail} {
		if strings.Contains(field, "me@example.com") {
			t.Fatalf("the verdict carries the account's email: %q", field)
		}
	}
}

func TestCheckHealthUnreadyStates(t *testing.T) {
	bin := fakeBinary(t)
	tests := []struct {
		name   string
		inst   func(t *testing.T) provider.Instance
		prober *fakeProber
		detect string
		want   provider.HealthState
		reason string
	}{
		{
			name:   "no binary anywhere",
			inst:   func(*testing.T) provider.Instance { return provider.Instance{ID: "claude", Enabled: true} },
			prober: loggedIn(),
			want:   provider.HealthNotInstalled,
			reason: "not found",
		},
		{
			name: "the configured path is not there",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: "/nowhere/claude", Enabled: true}
			},
			prober: loggedIn(),
			want:   provider.HealthNotInstalled,
			reason: "cannot be run",
		},
		{
			name: "the configured path is a directory",
			inst: func(t *testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: t.TempDir(), Enabled: true}
			},
			prober: loggedIn(),
			want:   provider.HealthNotInstalled,
			reason: "directory",
		},
		{
			name: "the binary does not answer --version",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, Enabled: true}
			},
			prober: &fakeProber{err: map[string]error{"--version": errors.New("exec format error")}},
			want:   provider.HealthCheckFailed,
			reason: "--version",
		},
		{
			name: "nobody is logged in",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, Enabled: true}
			},
			prober: &fakeProber{out: map[string][]byte{
				"--version": []byte("2.1.7"),
				"auth":      []byte(`{"loggedIn":false}`),
			}},
			want:   provider.HealthNotAuthenticated,
			reason: "claude auth login",
		},
		{
			name: "the login status cannot be asked for",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, Enabled: true}
			},
			prober: &fakeProber{
				out: map[string][]byte{"--version": []byte("2.1.7")},
				err: map[string]error{"auth": errors.New("signal: killed")},
			},
			want:   provider.HealthCheckFailed,
			reason: "login status",
		},
		{
			name: "nobody is logged in, reported with a non-zero exit",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, Enabled: true}
			},
			// What the real CLI does: a complete answer, and exit 1 to say no.
			prober: &fakeProber{
				out: map[string][]byte{
					"--version": []byte("2.1.7"),
					"auth":      []byte(`{"loggedIn":false,"authMethod":"none"}`),
				},
				err: map[string]error{"auth": errors.New("exit status 1")},
			},
			want:   provider.HealthNotAuthenticated,
			reason: "claude auth login",
		},
		{
			name: "the login status is not the shape dispatchq knows",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, Enabled: true}
			},
			prober: &fakeProber{out: map[string][]byte{
				"--version": []byte("2.1.7"),
				"auth":      []byte("not json at all"),
			}},
			want:   provider.HealthCheckFailed,
			reason: "does not understand",
		},
		{
			name: "the configuration directory is missing",
			inst: func(*testing.T) provider.Instance {
				return provider.Instance{ID: "claude", BinaryPath: bin, ConfigDir: "/nowhere/at/all", Enabled: true}
			},
			prober: loggedIn(),
			want:   provider.HealthInvalidConfiguration,
			reason: "not accessible",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdapter(tc.detect).CheckHealth(context.Background(), tc.inst(t), provider.NativeHost{Prober: tc.prober})
			if h.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", h.State, h.Reason, tc.want)
			}
			if !strings.Contains(h.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", h.Reason, tc.reason)
			}
		})
	}
}

// TestCheckHealthProbesTheInstancesOwnAccount makes sure the probe asks about
// the same configuration directory a run would use — otherwise a second
// subscription would be reported as the first one's login.
func TestCheckHealthProbesTheInstancesOwnAccount(t *testing.T) {
	dir := t.TempDir()
	p := loggedIn()
	newAdapter("").CheckHealth(context.Background(), provider.Instance{
		ID: "claude-secondary", BinaryPath: fakeBinary(t), ConfigDir: dir, Enabled: true,
	}, provider.NativeHost{Prober: p})
	for _, c := range p.cmds {
		if len(c.Env) != 1 || c.Env[0] != ConfigDirEnv+"="+dir {
			t.Fatalf("probe env = %v, want %s pointing at the instance's directory", c.Env, ConfigDirEnv)
		}
	}
}

// TestReadinessFallsBackToDetection covers the common setup: no path
// configured, so whatever detection found is what gets checked.
func TestReadinessFallsBackToDetection(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	inst := provider.Instance{ID: "claude", Enabled: true}
	if got := a.ResolveBinary(inst); got != bin {
		t.Fatalf("ResolveBinary = %q, want the detected binary", got)
	}
	if h := a.CheckHealth(context.Background(), inst, provider.NativeHost{Prober: loggedIn()}); h.State != provider.HealthReady {
		t.Fatalf("state = %q (%s), want ready", h.State, h.Reason)
	}
}

// TestResolveBinaryReportsNothingWhenItFindsNothing is what separates the
// readiness check from a run: Command falls back to the bare name so an
// exec-time lookup still gets a chance, but a health check must not.
func TestResolveBinaryReportsNothingWhenItFindsNothing(t *testing.T) {
	a := newAdapter("")
	if got := a.ResolveBinary(provider.Instance{ID: "claude"}); got != "" {
		t.Fatalf("ResolveBinary = %q, want an empty answer", got)
	}
	cmd, err := a.Command(provider.Instance{ID: "claude"}, provider.Request{Prompt: "p", SessionID: "s"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Path != BinaryName {
		t.Fatalf("command path = %q, want the bare %q for an exec lookup", cmd.Path, BinaryName)
	}
}

// TestCheckHealthToleratesNoiseAroundTheStatus: the CLI's combined output can
// carry an update notice before the document and a warning after it. Neither is
// a reason to declare the provider broken and stop the whole queue.
func TestCheckHealthToleratesNoiseAroundTheStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "a notice before", out: "Update available!\n" + `{"loggedIn":true,"authMethod":"claude.ai"}`},
		{name: "a warning after", out: `{"loggedIn":true,"authMethod":"claude.ai"}` + "\nwarning: something\n"},
		{name: "both", out: "Notice\n" + `{"loggedIn":true,"authMethod":"claude.ai"}` + "\nwarning\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProber{out: map[string][]byte{
				"--version": []byte("2.1.7"),
				"auth":      []byte(tc.out),
			}}
			h := newAdapter("").CheckHealth(context.Background(), provider.Instance{
				ID: "claude", BinaryPath: fakeBinary(t), Enabled: true,
			}, provider.NativeHost{Prober: p})
			if h.State != provider.HealthReady {
				t.Fatalf("state = %q (%s), want ready", h.State, h.Reason)
			}
		})
	}
}

// TestCheckHealthAppliesThePlatformExecutableRule drives the readiness check
// through both platforms' rules. Windows reports no execute bit on any file, so
// a real claude.exe used to be refused as "not executable" while the CLI ran
// fine from a terminal.
func TestCheckHealthAppliesThePlatformExecutableRule(t *testing.T) {
	file := func(name string, mode os.FileMode) func(t *testing.T) string {
		return func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(p, []byte("x"), mode); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
			return p
		}
	}
	tests := []struct {
		name   string
		goos   string
		path   func(t *testing.T) string
		want   provider.HealthState
		reason string
	}{
		{name: "windows exe without execute bits", goos: "windows", path: file("claude.exe", 0o600), want: provider.HealthReady},
		{name: "windows npm cmd shim", goos: "windows", path: file("claude.CMD", 0o600), want: provider.HealthReady},
		{name: "windows bat launcher", goos: "windows", path: file("claude.bat", 0o600), want: provider.HealthReady},
		{name: "windows text file", goos: "windows", path: file("claude.txt", 0o755), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "windows extensionless file", goos: "windows", path: file("claude", 0o755), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "windows directory", goos: "windows", path: func(t *testing.T) string { return t.TempDir() }, want: provider.HealthNotInstalled, reason: "directory"},
		{name: "windows missing file", goos: "windows", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "claude.exe") }, want: provider.HealthNotInstalled, reason: "cannot be run"},
		{name: "unix exe name without execute bits", goos: "linux", path: file("claude.exe", 0o644), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "unix file with execute bits", goos: "darwin", path: file("claude", 0o755), want: provider.HealthReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && tc.goos != "windows" && tc.want == provider.HealthReady {
				t.Skip("Windows file metadata cannot represent Unix execute bits")
			}
			bin := tc.path(t)
			t.Cleanup(binpath.UseOSRulesForTest(tc.goos))
			h := newAdapter("").CheckHealth(context.Background(), provider.Instance{ID: "claude", Kind: provider.KindClaudeCode, BinaryPath: bin, Enabled: true}, provider.NativeHost{Prober: loggedIn()})
			if h.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", h.State, h.Reason, tc.want)
			}
			if !strings.Contains(h.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", h.Reason, tc.reason)
			}
		})
	}
}
