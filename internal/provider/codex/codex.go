// Package codex is the provider adapter for the Codex CLI. It owns everything
// dispatchq knows about that harness: how `codex exec` is invoked, how its JSONL
// stream maps onto dispatchq's normalized events, how a thread is resumed, and how
// its account directory is selected. Nothing outside this package needs to know
// any of it.
//
// What the CLI actually does was established by a spike against codex-cli
// 0.154.0 — nothing here is guessed from documentation. The sanitized streams
// it captured live in testdata/ and drive this package's parser tests; the
// spike's own write-up is in the repository history.
package codex

import (
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// BinaryName is the CLI's command name, used when no path is configured and
// detection found nothing — the exec lookup may still succeed at run time.
const BinaryName = "codex"

// ConfigDirEnv is the environment variable Codex reads its configuration,
// credentials and stored threads from. An instance with its own directory is
// how a second Codex account stays separate.
const ConfigDirEnv = "CODEX_HOME"

// developerInstructionsKey is the config override dispatchq delivers its run
// contract through. Codex *replaces* this value rather than appending to it, so
// the adapter must not use it blindly — see Command.
const developerInstructionsKey = "developer_instructions"

// Adapter runs jobs through the Codex CLI.
type Adapter struct {
	// detect locates the CLI; swapped in tests.
	detect func() string

	binary  cachedProbe[string]
	catalog provider.Catalog
}

// New returns the Codex adapter.
func New() *Adapter { return &Adapter{detect: DetectBinary} }

// Kind implements provider.Adapter.
func (a *Adapter) Kind() provider.Kind { return provider.KindCodex }

// Describe implements provider.Adapter.
func (a *Adapter) Describe() provider.Description {
	return provider.Description{Name: "Codex", DefaultConfigDir: "~/.codex"}
}

// Capabilities implements provider.Adapter. Codex takes a sandbox mode on the
// command line, so unlike Claude Code it can actually enforce the restricted
// access modes, and it reports token usage but no monetary cost.
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SessionResume:     true,
		InteractiveResume: true,
		StructuredOutput:  true,
		ModelDiscovery:    true,
		ReasoningEffort:   true,
		UsageMetrics:      true,
		CostMetrics:       false,
		RateLimitResume:   true,
		Subagents:         true,
		Asides:            true,
		// Known gap: a real ChatGPT allowance exhaustion has not been observed in
		// the JSONL stream yet, so a rate-limited run resumes on a fixed delay.
		AccessModes: []provider.AccessMode{
			provider.AccessProviderDefault,
			provider.AccessReadOnly,
			provider.AccessWorkspaceWrite,
			provider.AccessFullAccess,
		},
	}
}

// DetectBinary locates the CLI, probing at most once per process — the
// login-shell fallback is far too expensive to repeat before every job.
func (a *Adapter) DetectBinary() string {
	return a.binary.do(func() string { return a.detect() })
}

// ResolveBinary implements provider.Adapter: the instance's configured path
// first, then detection, then "" when the CLI cannot be located at all.
func (a *Adapter) ResolveBinary(inst provider.Instance) string {
	if inst.BinaryPath != "" {
		return inst.BinaryPath
	}
	if !inst.Execution.IsNative() {
		// Detection looks at this computer. An instance that runs in Linux is
		// found on its own login PATH there, when it is started.
		return BinaryName
	}
	return a.DetectBinary()
}

// binaryOr resolves the executable for a run, falling back to the bare name so
// an exec-time PATH lookup still gets a chance.
func (a *Adapter) binaryOr(inst provider.Instance) string {
	if p := a.ResolveBinary(inst); p != "" {
		return p
	}
	return BinaryName
}

// sandboxModeKey is the configuration key behind Codex's --sandbox flag, used
// where the flag itself is not accepted (see Command).
const sandboxModeKey = "sandbox_mode"

// sandboxFor maps dispatchq's access intent onto Codex's --sandbox values. The
// provider default is expressed by passing no flag at all, so the instance's own
// configuration decides — which is exactly what "provider-default" promises.
func sandboxFor(mode provider.AccessMode) (string, bool) {
	switch mode {
	case provider.AccessReadOnly:
		return "read-only", true
	case provider.AccessWorkspaceWrite:
		return "workspace-write", true
	case provider.AccessFullAccess:
		return "danger-full-access", true
	default:
		return "", false
	}
}

// Command implements provider.Adapter. The shape is the one the spike verified:
// the prompt goes in through stdin with "-" as the positional argument, because
// passing it as an argument while stdin is not a terminal made Codex announce
// that it was reading stdin as well.
func (a *Adapter) Command(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("codex: a run needs a session id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}

	args := []string{"exec"}
	if req.Resume {
		// `codex exec resume <thread>` continues the thread the run started.
		args = append(args, "resume")
	}
	// A DispatchQ task may intentionally run from a project collection or another
	// directory that is not itself a Git checkout. Codex rejects those folders
	// by default, even though --cd accepts them. This only disables that preflight
	// check; it does not alter the task's access mode or sandbox arguments.
	args = append(args, "--json", "--skip-git-repo-check")
	// `codex exec resume` takes neither --sandbox nor --cd: both are flags of
	// `codex exec` itself, and passing them to the subcommand fails the run
	// before it starts ("unexpected argument"). The sandbox goes in as the
	// configuration value the flag would set, and the directory needs nothing —
	// the executor already starts the process in the task's folder.
	if sandbox, ok := sandboxFor(access); ok {
		if req.Resume {
			args = append(args, "-c", sandboxModeKey+"="+tomlString(sandbox))
		} else {
			args = append(args, "--sandbox", sandbox)
		}
	}
	if req.WorkingDir != "" && !req.Resume {
		args = append(args, "--cd", req.WorkingDir)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(req.ReasoningEffort))
	}
	// The override replaces the configured value, so the instance's own
	// developer instructions are read and carried along rather than dropped.
	if prompt := developerInstructions(req.SystemPrompt, ownInstructions(req.Host, inst)); prompt != "" {
		args = append(args, "-c", developerInstructionsKey+"="+tomlString(prompt))
	}
	if req.Resume {
		args = append(args, req.SessionID)
	}
	args = append(args, "-")

	return provider.Command{
		Path:  a.binaryOr(inst),
		Args:  args,
		Env:   a.env(inst),
		Stdin: req.Prompt,
	}, nil
}

// InteractiveResumeCommand implements provider.Adapter: the command that reopens
// a finished run's thread in a terminal. --include-non-interactive is what makes
// a thread created by `codex exec` resolvable here.
func (a *Adapter) InteractiveResumeCommand(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("codex: continuing a session needs its id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}
	args := []string{"resume", "--include-non-interactive"}
	if sandbox, ok := sandboxFor(access); ok {
		args = append(args, "--sandbox", sandbox)
	}
	if req.WorkingDir != "" {
		args = append(args, "--cd", req.WorkingDir)
	}
	args = append(args, req.SessionID)
	return provider.Command{Path: a.binaryOr(inst), Args: args, Env: a.env(inst)}, nil
}

// env is the instance's own environment: its account directory, when it has
// one, then the variables the operator set for it.
func (a *Adapter) env(inst provider.Instance) []string {
	var base []string
	if inst.ConfigDir != "" {
		base = append(base, ConfigDirEnv+"="+inst.ConfigDir)
	}
	return inst.Environ(base...)
}

// tomlString quotes a value for a `-c key=value` override, which Codex parses as
// TOML. A run contract is several paragraphs with quotes and newlines in it, so
// this is not optional.
func tomlString(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
