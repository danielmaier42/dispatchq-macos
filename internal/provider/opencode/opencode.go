// Package opencode is the provider adapter for the opencode CLI. It owns
// everything dispatchq knows about that harness: how `opencode run` is invoked,
// how its JSONL event stream maps onto dispatchq's normalized events, how a
// session is resumed, and how an instance's own account/config directory is
// selected. Nothing outside this package needs to know any of it.
//
// What the CLI actually does was established by running opencode 1.18.30
// locally against LM Studio — nothing here is guessed from documentation.
// opencode has no dedicated system-prompt flag and no sandboxed-access mode
// beyond bypassing permission prompts entirely, and its error stream never
// distinguished authentication, rate-limit or model-rejection failures in
// what was actually observed; every one of those gaps is called out at the
// point it matters below rather than papered over with an invented mapping.
package opencode

import (
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// BinaryName is the CLI's command name, used when no path is configured and
// detection found nothing — the exec lookup may still succeed at run time.
const BinaryName = "opencode"

// Adapter runs jobs through the opencode CLI.
type Adapter struct {
	// detect locates the CLI; swapped in tests.
	detect func() string

	binary  cachedProbe[string]
	catalog provider.Catalog
}

// New returns the opencode adapter.
func New() *Adapter { return &Adapter{detect: DetectBinary} }

// Kind implements provider.Adapter.
func (a *Adapter) Kind() provider.Kind { return provider.KindOpencode }

// Describe implements provider.Adapter. opencode actually splits its state
// across $XDG_CONFIG_HOME/opencode (settings) and $XDG_DATA_HOME/opencode
// (credentials, sessions); an instance's ConfigDir points both there at once
// (see env), so the config directory is what is shown.
func (a *Adapter) Describe() provider.Description {
	return provider.Description{Name: "opencode", DefaultConfigDir: "~/.config/opencode"}
}

// Capabilities implements provider.Adapter.
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SessionResume:     true,
		InteractiveResume: true,
		StructuredOutput:  true,
		ModelDiscovery:    true,
		ReasoningEffort:   true,
		UsageMetrics:      true,
		CostMetrics:       true,
		// Not claimed: nothing observed running against a real provider ever
		// distinguished a rate limit from any other failure — every error the
		// CLI reported came back as the same generic {"type":"error"} shape,
		// so the parser cannot tell a resumable rejection from a dead run.
		RateLimitResume: false,
		// Not claimed: opencode coordinates subagents through its own `agent`
		// command, but a single run's event stream was never observed
		// spawning one, so dispatchq does not promise it counts correctly.
		Subagents: false,
		// Not claimed: opencode has no tool-free, schema-constrained turn like
		// Claude Code's --safe-mode aside; see AsideCommand.
		Asides: false,
		// Known gap: every failure comes back in one generic shape (see parse.go),
		// so a limit or a lost login cannot be told from any other error.
		AccessModes: []provider.AccessMode{
			provider.AccessProviderDefault,
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

// Command implements provider.Adapter. The prompt is a positional argument —
// `opencode run [message..]` — never stdin, the same shape Claude Code takes.
//
// opencode has no flag that delivers a system prompt alongside a model's own
// instructions, so dispatchq's run contract is prefixed onto the message itself,
// clearly delimited from the task's own prompt. It is sent on every turn,
// including a resume, because unlike Claude Code and Codex nothing here
// persists it across turns — there is no session-level place to put it.
func (a *Adapter) Command(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("opencode: a run needs a session id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}

	args := []string{"run", "--format", "json"}
	if req.Resume {
		args = append(args, "--session", req.SessionID)
	}
	if access == provider.AccessFullAccess {
		args = append(args, "--auto")
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.ReasoningEffort != "" {
		args = append(args, "--variant", req.ReasoningEffort)
	}
	args = append(args, withSystemPrompt(req.SystemPrompt, req.Prompt))

	return provider.Command{Path: a.binaryOr(inst), Args: args, Env: a.env(inst)}, nil
}

// InteractiveResumeCommand implements provider.Adapter: opencode's own
// top-level command takes --session to reopen a session in its interactive
// TUI — the same flag `run` takes to continue one headlessly.
func (a *Adapter) InteractiveResumeCommand(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("opencode: continuing a session needs its id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}
	args := []string{"--session", req.SessionID}
	if req.WorkingDir != "" {
		args = append(args, "--dir", req.WorkingDir)
	}
	if access == provider.AccessFullAccess {
		args = append(args, "--auto")
	}
	return provider.Command{Path: a.binaryOr(inst), Args: args, Env: a.env(inst)}, nil
}

// env points the instance at its own account and configuration: opencode
// keeps settings under $XDG_CONFIG_HOME/opencode and credentials, sessions and
// its log under $XDG_DATA_HOME/opencode, so isolating a second instance means
// overriding every XDG base it reads, not one dedicated variable the way
// Claude Code and Codex have. Pointing all three at the same directory is
// safe: each nests its own "opencode" subdirectory underneath, verified by
// running with all three overridden and inspecting what landed where.
//
// The variables the operator set for the instance follow, so they win.
func (a *Adapter) env(inst provider.Instance) []string {
	var base []string
	if inst.ConfigDir != "" {
		base = []string{
			"XDG_CONFIG_HOME=" + inst.ConfigDir,
			"XDG_DATA_HOME=" + inst.ConfigDir,
			"XDG_CACHE_HOME=" + inst.ConfigDir,
		}
	}
	return inst.Environ(base...)
}

// withSystemPrompt prefixes dispatchq's run contract onto the task's prompt,
// clearly delimited so the model can tell dispatchq's standing guidance from the
// task at hand.
func withSystemPrompt(system, prompt string) string {
	system = strings.TrimSpace(system)
	if system == "" {
		return prompt
	}
	return system + "\n\n---\n\n" + prompt
}
