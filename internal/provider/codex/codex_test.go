package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
)

func newAdapter(detected string) *Adapter {
	return &Adapter{detect: func() string { return detected }}
}

func instance(binary string) provider.Instance {
	return provider.Instance{ID: "codex", Kind: provider.KindCodex, Name: "Codex", BinaryPath: binary, Enabled: true}
}

// isolated is an instance pointed at an empty configuration directory, so a
// test that compares arguments sees only what dispatchq builds — never a
// developer_instructions value the machine running the test happens to have in
// its own ~/.codex/config.toml.
func isolated(t *testing.T, binary string) provider.Instance {
	t.Helper()
	inst := instance(binary)
	inst.ConfigDir = t.TempDir()
	return inst
}

// TestCommandGoldenArguments pins the invocation the spike verified, argument by
// argument. The prompt goes in on stdin with "-" as the positional argument,
// because passing it as an argument while stdin is not a terminal made the CLI
// announce that it was reading stdin as well.
func TestCommandGoldenArguments(t *testing.T) {
	tests := []struct {
		name     string
		req      provider.Request
		wantArgs []string
	}{
		{
			name: "a plain run",
			req:  provider.Request{Prompt: "do it", WorkingDir: "/repo", SessionID: "s-1"},
			wantArgs: []string{
				"exec", "--json", "--skip-git-repo-check", "--cd", "/repo", "-",
			},
		},
		{
			name: "with a model and reasoning effort",
			req: provider.Request{
				Prompt: "do it", WorkingDir: "/repo", SessionID: "s-1",
				Model: "gpt-5.6-sol", ReasoningEffort: "xhigh",
			},
			wantArgs: []string{
				"exec", "--json", "--skip-git-repo-check", "--cd", "/repo", "--model", "gpt-5.6-sol",
				"-c", `model_reasoning_effort="xhigh"`, "-",
			},
		},
		{
			name: "read-only",
			req: provider.Request{
				Prompt: "review it", WorkingDir: "/repo", SessionID: "s-1",
				AccessMode: provider.AccessReadOnly,
			},
			wantArgs: []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "read-only", "--cd", "/repo", "-"},
		},
		{
			name: "full access",
			req: provider.Request{
				Prompt: "fix it", WorkingDir: "/repo", SessionID: "s-1",
				AccessMode: provider.AccessFullAccess,
			},
			wantArgs: []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "danger-full-access", "--cd", "/repo", "-"},
		},
		{
			// `codex exec resume` accepts neither --cd nor --sandbox: it would
			// refuse the whole invocation. The folder comes from the process's
			// working directory, the sandbox from the configuration value.
			name: "a resumed thread",
			req: provider.Request{
				Prompt: "carry on", WorkingDir: "/repo", SessionID: "thread-42", Resume: true,
			},
			wantArgs: []string{"exec", "resume", "--json", "--skip-git-repo-check", "thread-42", "-"},
		},
		{
			name: "a resumed thread with an access mode",
			req: provider.Request{
				Prompt: "carry on", WorkingDir: "/repo", SessionID: "thread-42", Resume: true,
				AccessMode: provider.AccessReadOnly,
			},
			wantArgs: []string{
				"exec", "resume", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="read-only"`, "thread-42", "-",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newAdapter("").Command(isolated(t, "/opt/codex"), tc.req)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if got.Path != "/opt/codex" {
				t.Fatalf("path = %q, want the configured binary", got.Path)
			}
			if !reflect.DeepEqual(got.Args, tc.wantArgs) {
				t.Fatalf("args =\n  %q\nwant\n  %q", got.Args, tc.wantArgs)
			}
			if got.Stdin != tc.req.Prompt {
				t.Fatalf("stdin = %q, want the prompt", got.Stdin)
			}
		})
	}
}

// TestCommandAllowsNonRepositoryWorkingDirectories protects project collection
// folders: DispatchQ accepts them, so the Codex adapter must not reject them
// before the task starts.
func TestCommandAllowsNonRepositoryWorkingDirectories(t *testing.T) {
	got, err := newAdapter("").Command(isolated(t, "/opt/codex"), provider.Request{
		Prompt: "p", WorkingDir: "/projects", SessionID: "s-1",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if !slices.Contains(got.Args, "--skip-git-repo-check") {
		t.Fatalf("args = %q, want --skip-git-repo-check", got.Args)
	}
}

// TestProviderDefaultPassesNoSandbox: "provider default" means the instance's
// own configuration decides, and the only way to say that to Codex is to pass no
// --sandbox at all. Picking one here would quietly promise a restriction the
// operator never asked for — or remove one they did.
func TestProviderDefaultPassesNoSandbox(t *testing.T) {
	got, err := newAdapter("").Command(isolated(t, "/opt/codex"), provider.Request{
		Prompt: "p", SessionID: "s-1", AccessMode: provider.AccessProviderDefault,
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, a := range got.Args {
		if a == "--sandbox" {
			t.Fatalf("args = %q, want no sandbox flag for the provider default", got.Args)
		}
	}
}

func TestCommandNeedsASessionID(t *testing.T) {
	if _, err := newAdapter("").Command(instance("/opt/codex"), provider.Request{Prompt: "p"}); err == nil {
		t.Fatal("a run without a session id must be refused")
	}
}

// TestConfigDirTravelsWithEveryCommand: a second account is a second
// CODEX_HOME, so anything that touches the account has to carry it.
func TestConfigDirTravelsWithEveryCommand(t *testing.T) {
	inst := instance("/opt/codex")
	inst.ConfigDir = t.TempDir()
	a := newAdapter("")

	run, err := a.Command(inst, provider.Request{Prompt: "p", SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	resume, err := a.InteractiveResumeCommand(inst, provider.Request{SessionID: "s-1", WorkingDir: "/repo"})
	if err != nil {
		t.Fatalf("InteractiveResumeCommand: %v", err)
	}
	want := ConfigDirEnv + "=" + inst.ConfigDir
	for name, cmd := range map[string]provider.Command{"run": run, "resume": resume} {
		if len(cmd.Env) != 1 || cmd.Env[0] != want {
			t.Fatalf("%s env = %v, want %q", name, cmd.Env, want)
		}
	}
}

func TestInteractiveResumeCommand(t *testing.T) {
	got, err := newAdapter("").InteractiveResumeCommand(instance("/opt/codex"), provider.Request{
		SessionID: "thread-42", WorkingDir: "/repo", AccessMode: provider.AccessWorkspaceWrite,
	})
	if err != nil {
		t.Fatalf("InteractiveResumeCommand: %v", err)
	}
	want := []string{"resume", "--include-non-interactive", "--sandbox", "workspace-write", "--cd", "/repo", "thread-42"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args = %q, want %q", got.Args, want)
	}
	if _, err := newAdapter("").InteractiveResumeCommand(instance("/opt/codex"), provider.Request{}); err == nil {
		t.Fatal("continuing without a session id must be refused")
	}
}

// TestDeveloperInstructionsKeepTheInstancesOwn is the conflict the spike
// flagged: the `-c developer_instructions=` override *replaces* the configured
// value. dispatchq puts its run contract there, so the operator's own instructions
// have to be carried along rather than silently dropped.
func TestDeveloperInstructionsKeepTheInstancesOwn(t *testing.T) {
	dir := t.TempDir()
	const own = "Always answer in German."
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("developer_instructions = \"\"\"\n"+own+"\n\"\"\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	inst := instance("/opt/codex")
	inst.ConfigDir = dir

	got, err := newAdapter("").Command(inst, provider.Request{
		Prompt: "p", SessionID: "s-1", SystemPrompt: "You are running headless.",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	value := overrideValue(t, got.Args, "developer_instructions")
	if !strings.Contains(value, "You are running headless.") {
		t.Fatalf("override = %q, want dispatchq's run contract in it", value)
	}
	if !strings.Contains(value, own) {
		t.Fatalf("override = %q, want the instance's own instructions kept", value)
	}
	if strings.Index(value, "headless") > strings.Index(value, own) {
		t.Fatal("dispatchq's contract must come first; the operator's guidance adds to it")
	}
}

// TestDeveloperInstructionsWithoutAConfiguredValue: nothing to preserve, so the
// override is exactly dispatchq's contract.
func TestDeveloperInstructionsWithoutAConfiguredValue(t *testing.T) {
	inst := instance("/opt/codex")
	inst.ConfigDir = t.TempDir()
	got, err := newAdapter("").Command(inst, provider.Request{
		Prompt: "p", SessionID: "s-1", SystemPrompt: "contract",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if value := overrideValue(t, got.Args, "developer_instructions"); value != "contract" {
		t.Fatalf("override = %q, want exactly the contract", value)
	}
}

// TestOverridesAreQuotedAsTOML: a run contract is paragraphs with quotes,
// newlines and backslashes in it, and `-c key=value` is parsed as TOML.
func TestOverridesAreQuotedAsTOML(t *testing.T) {
	const prompt = "say \"hi\"\nand\\or not\there"
	got, err := newAdapter("").Command(isolated(t, "/opt/codex"), provider.Request{
		Prompt: "p", SessionID: "s-1", SystemPrompt: prompt,
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	raw := rawOverride(t, got.Args, "developer_instructions")
	if strings.Contains(raw, "\n") {
		t.Fatalf("override %q still contains a real newline", raw)
	}
	if value := overrideValue(t, got.Args, "developer_instructions"); value != prompt {
		t.Fatalf("round trip = %q, want %q", value, prompt)
	}
}

// rawOverride returns the `key="…"` argument that follows a -c.
func rawOverride(t *testing.T, args []string, key string) string {
	t.Helper()
	for i, a := range args {
		if a == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], key+"=") {
			return strings.TrimPrefix(args[i+1], key+"=")
		}
	}
	t.Fatalf("no -c %s= override in %q", key, args)
	return ""
}

// overrideValue unquotes what rawOverride found, which is how Codex will read it.
func overrideValue(t *testing.T, args []string, key string) string {
	t.Helper()
	raw := rawOverride(t, args, key)
	if !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		t.Fatalf("override %q is not a quoted TOML string", raw)
	}
	body := raw[1 : len(raw)-1]
	replacer := strings.NewReplacer(`\"`, `"`, `\n`, "\n", `\r`, "\r", `\t`, "\t", `\\`, `\`)
	return replacer.Replace(body)
}

func TestResolveBinary(t *testing.T) {
	if got := newAdapter("/detected/codex").ResolveBinary(provider.Instance{}); got != "/detected/codex" {
		t.Fatalf("ResolveBinary = %q, want the detected binary", got)
	}
	if got := newAdapter("/detected/codex").ResolveBinary(instance("/configured/codex")); got != "/configured/codex" {
		t.Fatalf("ResolveBinary = %q, want the configured path to win", got)
	}
	// Nothing found: the readiness check must see that, while a run still gets
	// the bare name for an exec-time lookup.
	a := newAdapter("")
	if got := a.ResolveBinary(provider.Instance{}); got != "" {
		t.Fatalf("ResolveBinary = %q, want an empty answer", got)
	}
	cmd, err := a.Command(provider.Instance{}, provider.Request{Prompt: "p", SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if cmd.Path != BinaryName {
		t.Fatalf("path = %q, want the bare %q", cmd.Path, BinaryName)
	}
}

func TestCapabilities(t *testing.T) {
	caps := newAdapter("").Capabilities()
	// Codex takes a sandbox mode on the command line, so unlike Claude Code it
	// can actually enforce the restricted modes.
	for _, mode := range []provider.AccessMode{
		provider.AccessProviderDefault, provider.AccessReadOnly,
		provider.AccessWorkspaceWrite, provider.AccessFullAccess,
	} {
		if !caps.SupportsAccess(mode) {
			t.Errorf("Codex should be able to enforce %q", mode)
		}
	}
	if !caps.ReasoningEffort {
		t.Error("Codex takes a reasoning effort")
	}
	if caps.CostMetrics {
		t.Error("Codex reports no monetary cost, so dispatchq must not claim it does")
	}
}

// fakeProber answers probes from a script keyed by the first argument.
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
		"--version": []byte("codex-cli 0.154.0\n"),
		"login":     []byte("Logged in using ChatGPT\n"),
	}}
}

func fakeBinary(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test stand-in that must be executable
		t.Fatalf("write fake binary: %v", err)
	}
	return p
}

func TestCheckHealthReady(t *testing.T) {
	bin := fakeBinary(t)
	p := loggedIn()
	h := newAdapter("").CheckHealth(context.Background(), instance(bin), p)
	if h.State != provider.HealthReady {
		t.Fatalf("state = %q (%s), want ready", h.State, h.Reason)
	}
	if !strings.Contains(h.Detail, "0.154.0") || !strings.Contains(h.Detail, "ChatGPT") {
		t.Fatalf("detail = %q, want the version and the login method", h.Detail)
	}
	// Never `codex exec`: without credentials that retries both transports five
	// times each, which is a readiness check that looks like an outage.
	if len(p.cmds) != 2 || p.cmds[0].Args[0] != "--version" ||
		strings.Join(p.cmds[1].Args, " ") != "login status" {
		t.Fatalf("probes = %+v, want a version and a login-status question", p.cmds)
	}
}

func TestCheckHealthUnreadyStates(t *testing.T) {
	bin := fakeBinary(t)
	tests := []struct {
		name   string
		inst   func(t *testing.T) provider.Instance
		prober *fakeProber
		want   provider.HealthState
		reason string
	}{
		{
			name:   "no binary anywhere",
			inst:   func(*testing.T) provider.Instance { return instance("") },
			prober: loggedIn(),
			want:   provider.HealthNotInstalled,
			reason: "not found",
		},
		{
			name:   "the configured path is not there",
			inst:   func(*testing.T) provider.Instance { return instance("/nowhere/codex") },
			prober: loggedIn(),
			want:   provider.HealthNotInstalled,
			reason: "cannot be run",
		},
		{
			name:   "the binary does not answer --version",
			inst:   func(*testing.T) provider.Instance { return instance(bin) },
			prober: &fakeProber{err: map[string]error{"--version": errors.New("exec format error")}},
			want:   provider.HealthCheckFailed,
			reason: "--version",
		},
		{
			// `codex login status` says it with the exit status: "Not logged in"
			// on stderr and exit 1.
			name: "nobody is logged in",
			inst: func(*testing.T) provider.Instance { return instance(bin) },
			prober: &fakeProber{
				out: map[string][]byte{"--version": []byte("codex-cli 0.154.0"), "login": []byte("Not logged in\n")},
				err: map[string]error{"login": errors.New("exit status 1")},
			},
			want:   provider.HealthNotAuthenticated,
			reason: "codex login",
		},
		{
			name: "the configuration directory is missing",
			inst: func(*testing.T) provider.Instance {
				i := instance(bin)
				i.ConfigDir = "/nowhere/at/all"
				return i
			},
			prober: loggedIn(),
			want:   provider.HealthInvalidConfiguration,
			reason: "not accessible",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdapter("").CheckHealth(context.Background(), tc.inst(t), tc.prober)
			if h.State != tc.want {
				t.Fatalf("state = %q (%s), want %q", h.State, h.Reason, tc.want)
			}
			if !strings.Contains(h.Reason, tc.reason) {
				t.Fatalf("reason = %q, want it to mention %q", h.Reason, tc.reason)
			}
		})
	}
}

func TestListModels(t *testing.T) {
	catalog := []byte(`{"models":[{"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol"},{"slug":"gpt-6-astra"}]}`)
	p := &fakeProber{out: map[string][]byte{"debug": catalog}}
	got := newAdapter("").ListModels(context.Background(), instance(fakeBinary(t)), p)
	want := []provider.Model{
		{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol"},
		{ID: "gpt-6-astra", Label: "gpt-6-astra"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %+v, want %+v", got, want)
	}
}

// TestListModelsFallsBackToTheBundledList: `codex debug models` is a debug
// command. A provider whose catalog cannot be read runs perfectly well, so its
// absence costs suggestions and nothing else.
func TestListModelsFallsBackToTheBundledList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prober *fakeProber
	}{
		{name: "the command failed", prober: &fakeProber{err: map[string]error{"debug": errors.New("unknown command")}}},
		{name: "unparseable output", prober: &fakeProber{out: map[string][]byte{"debug": []byte("not json")}}},
		{name: "an empty catalog", prober: &fakeProber{out: map[string][]byte{"debug": []byte(`{"models":[]}`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newAdapter("").ListModels(context.Background(), instance(fakeBinary(t)), tc.prober)
			if !reflect.DeepEqual(got, bundledModels) {
				t.Fatalf("models = %+v, want the bundled fallback %+v", got, bundledModels)
			}
		})
	}
}

// TestCatalogIsPerInstance: two instances of one kind can be two installations
// — a second account, or a path pointing at another build — so one answer for
// both would show the wrong models for the second.
func TestCatalogIsPerInstance(t *testing.T) {
	a := newAdapter("")
	first, second := instance(fakeBinary(t)), instance(fakeBinary(t))
	p := &fakeProber{out: map[string][]byte{
		"debug": []byte(`{"models":[{"slug":"only-here","display_name":"Only here"}]}`),
	}}
	if got := a.ListModels(context.Background(), first, p); len(got) != 1 || got[0].ID != "only-here" {
		t.Fatalf("models = %+v, want the discovered one", got)
	}
	p.out["debug"] = []byte(`{"models":[{"slug":"other-build"}]}`)
	if got := a.ListModels(context.Background(), second, p); len(got) != 1 || got[0].ID != "other-build" {
		t.Fatalf("models = %+v, want the second instance asked for itself", got)
	}
}

// TestAFallbackCatalogIsNotRemembered: a provider whose CLI was not installed
// yet must not be stuck on the fallback list for the daemon's lifetime.
func TestAFallbackCatalogIsNotRemembered(t *testing.T) {
	a := newAdapter("")
	inst := instance(fakeBinary(t))
	failing := &fakeProber{err: map[string]error{"debug": errors.New("unknown command")}}
	if got := a.ListModels(context.Background(), inst, failing); len(got) != len(bundledModels) {
		t.Fatalf("models = %+v, want the fallback", got)
	}
	working := &fakeProber{out: map[string][]byte{
		"debug": []byte(`{"models":[{"slug":"now-installed"}]}`),
	}}
	if got := a.ListModels(context.Background(), inst, working); len(got) != 1 || got[0].ID != "now-installed" {
		t.Fatalf("models = %+v, want the real catalog once it can be read", got)
	}
}

// TestLoginStatusThatCouldNotBeAskedIsNotALogout: "I could not ask" must not
// read as "nobody is signed in" — that verdict refuses new tasks.
func TestLoginStatusThatCouldNotBeAskedIsNotALogout(t *testing.T) {
	p := &fakeProber{
		out: map[string][]byte{"--version": []byte("codex-cli 0.154.0")},
		err: map[string]error{"login": errors.New("signal: killed")},
	}
	h := newAdapter("").CheckHealth(context.Background(), instance(fakeBinary(t)), p)
	if h.State != provider.HealthCheckFailed {
		t.Fatalf("state = %q (%s), want check_failed", h.State, h.Reason)
	}
	if h.KnownUnready() {
		t.Fatal("an unanswered probe must not refuse new tasks")
	}
}

// TestOperatorEnvironmentTravelsWithTheCommand: variables set on the instance
// reach the process, after the account directory.
func TestOperatorEnvironmentTravelsWithTheCommand(t *testing.T) {
	inst := instance("/opt/codex")
	inst.ConfigDir = t.TempDir()
	inst.Env = map[string]string{"OPENAI_API_KEY": "sk-secret"}
	cmd, err := newAdapter("").Command(inst, provider.Request{Prompt: "p", SessionID: "s-1"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := []string{ConfigDirEnv + "=" + inst.ConfigDir, "OPENAI_API_KEY=sk-secret"}
	if !reflect.DeepEqual(cmd.Env, want) {
		t.Fatalf("env = %q, want %q", cmd.Env, want)
	}
}
