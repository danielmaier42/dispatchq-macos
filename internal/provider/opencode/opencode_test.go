package opencode

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/binpath"
)

func newAdapter(detected string) *Adapter {
	return &Adapter{detect: func() string { return detected }}
}

func instance(binary string) provider.Instance {
	return provider.Instance{ID: "oc", Kind: provider.KindOpencode, Name: "opencode", BinaryPath: binary, Enabled: true}
}

// TestCommandGoldenArguments pins the invocation run directly against
// opencode 1.18.30: `run --format json`, the prompt as the final positional
// argument, `--session` only on a resume (a fresh run lets opencode assign its
// own session id, reported back on the stream's sessionID field), and
// `--auto` only for full access — no sandboxed mode was found in what
// `opencode run --help` advertises.
func TestCommandGoldenArguments(t *testing.T) {
	tests := []struct {
		name     string
		req      provider.Request
		wantArgs []string
	}{
		{
			name:     "a plain run",
			req:      provider.Request{Prompt: "do it", WorkingDir: "/repo", SessionID: "s-1"},
			wantArgs: []string{"run", "--format", "json", "do it"},
		},
		{
			name: "a run with a system prompt",
			req: provider.Request{
				Prompt: "do it", SessionID: "s-1", SystemPrompt: "follow the contract",
			},
			wantArgs: []string{"run", "--format", "json", "follow the contract\n\n---\n\ndo it"},
		},
		{
			name: "full access",
			req: provider.Request{
				Prompt: "fix it", SessionID: "s-1", AccessMode: provider.AccessFullAccess,
			},
			wantArgs: []string{"run", "--format", "json", "--auto", "fix it"},
		},
		{
			name: "a resumed session",
			req: provider.Request{
				Prompt: "continue", SessionID: "ses_abc", Resume: true,
			},
			wantArgs: []string{"run", "--format", "json", "--session", "ses_abc", "continue"},
		},
		{
			name: "a model and reasoning effort",
			req: provider.Request{
				Prompt: "do it", SessionID: "s-1", Model: "lmstudio/qwen/qwen3.6-35b-a3b", ReasoningEffort: "high",
			},
			wantArgs: []string{"run", "--format", "json", "--model", "lmstudio/qwen/qwen3.6-35b-a3b", "--variant", "high", "do it"},
		},
	}

	a := newAdapter("/usr/local/bin/opencode")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := a.Command(instance(""), tt.req)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, tt.wantArgs) {
				t.Errorf("Args = %v, want %v", cmd.Args, tt.wantArgs)
			}
			if cmd.Stdin != "" {
				t.Errorf("Stdin = %q, want empty — the prompt is a positional argument", cmd.Stdin)
			}
		})
	}
}

func TestCommandRejectsUnsupportedAccess(t *testing.T) {
	a := newAdapter("/usr/local/bin/opencode")
	_, err := a.Command(instance(""), provider.Request{
		Prompt: "x", SessionID: "s-1", AccessMode: provider.AccessReadOnly,
	})
	if err == nil {
		t.Fatal("Command: want an error, opencode cannot enforce read-only")
	}
}

func TestCommandRequiresSessionID(t *testing.T) {
	a := newAdapter("/usr/local/bin/opencode")
	if _, err := a.Command(instance(""), provider.Request{Prompt: "x"}); err == nil {
		t.Fatal("Command: want an error for a missing session id")
	}
}

func TestEnvIsolatesAllThreeXDGBases(t *testing.T) {
	a := newAdapter("")
	inst := instance("")
	inst.ConfigDir = "/tmp/an-instance"
	cmd, err := a.Command(inst, provider.Request{Prompt: "x", SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{
		"XDG_CONFIG_HOME=/tmp/an-instance",
		"XDG_DATA_HOME=/tmp/an-instance",
		"XDG_CACHE_HOME=/tmp/an-instance",
	}
	if !reflect.DeepEqual(cmd.Env, want) {
		t.Errorf("Env = %v, want %v", cmd.Env, want)
	}
}

func TestInteractiveResumeCommand(t *testing.T) {
	a := newAdapter("/usr/local/bin/opencode")
	cmd, err := a.InteractiveResumeCommand(instance(""), provider.Request{
		SessionID: "ses_abc", WorkingDir: "/repo", AccessMode: provider.AccessFullAccess,
	})
	if err != nil {
		t.Fatalf("InteractiveResumeCommand: %v", err)
	}
	want := []string{"--session", "ses_abc", "--dir", "/repo", "--auto"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("Args = %v, want %v", cmd.Args, want)
	}
}

// fakeProber answers probes from a script keyed by the first argument.
type fakeProber struct {
	out map[string][]byte
	err map[string]error
}

func (f *fakeProber) Probe(_ context.Context, c provider.Command) ([]byte, error) {
	key := ""
	if len(c.Args) > 0 {
		key = c.Args[0]
	}
	return f.out[key], f.err[key]
}

func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write fake binary: %v", err)
	}
	return p
}

func TestCheckHealthReady(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	h := a.CheckHealth(context.Background(), instance(bin), &fakeProber{
		out: map[string][]byte{"--version": []byte("1.18.30\n")},
	})
	if h.State != provider.HealthReady {
		t.Fatalf("State = %v, want ready (reason: %q)", h.State, h.Reason)
	}
	if h.Detail != "1.18.30" {
		t.Errorf("Detail = %q, want the version", h.Detail)
	}
}

func TestCheckHealthNotInstalled(t *testing.T) {
	a := newAdapter("")
	h := a.CheckHealth(context.Background(), instance(""), &fakeProber{})
	if h.State != provider.HealthNotInstalled {
		t.Fatalf("State = %v, want not_installed", h.State)
	}
}

func TestCheckHealthInvalidConfigDir(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	inst := instance(bin)
	inst.ConfigDir = filepath.Join(t.TempDir(), "does-not-exist")
	h := a.CheckHealth(context.Background(), inst, &fakeProber{})
	if h.State != provider.HealthInvalidConfiguration {
		t.Fatalf("State = %v, want invalid_configuration", h.State)
	}
}

func TestCheckHealthVersionProbeFails(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	h := a.CheckHealth(context.Background(), instance(bin), &fakeProber{
		err: map[string]error{"--version": os.ErrDeadlineExceeded},
	})
	if h.State != provider.HealthCheckFailed {
		t.Fatalf("State = %v, want check_failed when --version cannot be answered", h.State)
	}
}

func TestListModelsParsesPlainOutput(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	p := &fakeProber{out: map[string][]byte{
		"models": []byte("lmstudio/openai/gpt-oss-20b\nlmstudio/qwen/qwen3.6-35b-a3b\n"),
	}}
	got := a.ListModels(context.Background(), instance(bin), p)
	want := []provider.Model{
		{ID: "lmstudio/openai/gpt-oss-20b", Label: "lmstudio/openai/gpt-oss-20b"},
		{ID: "lmstudio/qwen/qwen3.6-35b-a3b", Label: "lmstudio/qwen/qwen3.6-35b-a3b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListModels = %v, want %v", got, want)
	}
}

// TestListModelsFiltersAnErrorLine covers the shape run directly:
// `opencode models <unknown-provider>` prints a one-line, space-containing
// error instead of any model — which must not be offered as a selectable id.
func TestListModelsFiltersAnErrorLine(t *testing.T) {
	bin := fakeBinary(t)
	a := newAdapter(bin)
	p := &fakeProber{out: map[string][]byte{
		"models": []byte("Error: Provider not found: opencode\n"),
	}}
	got := a.ListModels(context.Background(), instance(bin), p)
	if got != nil {
		t.Errorf("ListModels = %v, want nil for an error line", got)
	}
}

func TestListModelsNoBinaryYieldsNoSuggestions(t *testing.T) {
	a := newAdapter("")
	got := a.ListModels(context.Background(), instance(""), &fakeProber{})
	if got != nil {
		t.Errorf("ListModels = %v, want nil — opencode ships no models of its own", got)
	}
}

func TestAsideIsUnsupported(t *testing.T) {
	a := newAdapter("/usr/local/bin/opencode")
	if _, err := a.AsideCommand(instance(""), provider.AsideRequest{Text: "hi", SessionID: "s-1"}); err == nil {
		t.Fatal("AsideCommand: want an error, opencode cannot answer asides")
	}
	if _, err := a.ParseAside([]byte(`{}`)); err == nil {
		t.Fatal("ParseAside: want an error")
	}
}

func TestCapabilitiesDoNotClaimWhatWasNotVerified(t *testing.T) {
	c := (&Adapter{}).Capabilities()
	if c.RateLimitResume {
		t.Error("RateLimitResume: no observed run distinguished a rate limit from any other failure")
	}
	if c.Asides {
		t.Error("Asides: AsideCommand refuses")
	}
}

func TestDescribeAndKind(t *testing.T) {
	a := New()
	if a.Kind() != provider.KindOpencode {
		t.Errorf("Kind() = %q", a.Kind())
	}
	desc := a.Describe()
	if desc.Name == "" || desc.DefaultConfigDir == "" {
		t.Errorf("Describe() = %+v, want both fields set", desc)
	}
}

// TestOperatorEnvironmentTravelsWithTheCommand: variables set on the instance
// reach the process, with or without an isolated configuration directory.
func TestOperatorEnvironmentTravelsWithTheCommand(t *testing.T) {
	inst := instance("/opt/opencode")
	inst.Env = map[string]string{"OPENROUTER_API_KEY": "sk-secret"}
	cmd, err := New().Command(inst, provider.Request{Prompt: "p", SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if want := []string{"OPENROUTER_API_KEY=sk-secret"}; !reflect.DeepEqual(cmd.Env, want) {
		t.Fatalf("env = %q, want %q", cmd.Env, want)
	}
}

// TestCheckHealthAppliesThePlatformExecutableRule drives the readiness check
// through both platforms' rules. Windows reports no execute bit on any file, so
// a real opencode.exe used to be refused as "not executable" while the CLI ran
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
		{name: "windows exe without execute bits", goos: "windows", path: file("opencode.exe", 0o600), want: provider.HealthReady},
		{name: "windows npm cmd shim", goos: "windows", path: file("opencode.CMD", 0o600), want: provider.HealthReady},
		{name: "windows bat launcher", goos: "windows", path: file("opencode.bat", 0o600), want: provider.HealthReady},
		{name: "windows text file", goos: "windows", path: file("opencode.txt", 0o755), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "windows extensionless file", goos: "windows", path: file("opencode", 0o755), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "windows directory", goos: "windows", path: func(t *testing.T) string { return t.TempDir() }, want: provider.HealthNotInstalled, reason: "directory"},
		{name: "windows missing file", goos: "windows", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "opencode.exe") }, want: provider.HealthNotInstalled, reason: "cannot be run"},
		{name: "unix exe name without execute bits", goos: "linux", path: file("opencode.exe", 0o644), want: provider.HealthNotInstalled, reason: "not executable"},
		{name: "unix file with execute bits", goos: "darwin", path: file("opencode", 0o755), want: provider.HealthReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := tc.path(t)
			t.Cleanup(binpath.UseOSRulesForTest(tc.goos))
			h := newAdapter("").CheckHealth(context.Background(), instance(bin), &fakeProber{out: map[string][]byte{"--version": []byte("1.18.30\n")}})
			if h.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", h.State, h.Reason, tc.want)
			}
			if !strings.Contains(h.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", h.Reason, tc.reason)
			}
		})
	}
}
