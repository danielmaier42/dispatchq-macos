package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// ResolveBinary implements provider.Adapter: the instance's configured path
// first, then detection, then "" when the CLI cannot be located at all.
// Command falls back to the bare name so an exec-time lookup still gets a
// chance; a readiness check must not, or a missing CLI would read as ready.
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

// authStatus is the part of `claude auth status --json` dispatchq reads. The
// command also reports the account's email and organisation; those are
// deliberately not parsed, so they cannot end up in a log or an API response.
type authStatus struct {
	LoggedIn   bool   `json:"loggedIn"`
	AuthMethod string `json:"authMethod"`
}

// CheckHealth implements provider.Adapter. It costs no model usage: it looks at
// the filesystem, asks the CLI for its version, and asks whether anyone is
// logged in.
func (a *Adapter) CheckHealth(ctx context.Context, inst provider.Instance, h provider.Host) provider.Health {
	if bad, ok := provider.CheckConfigDir(ctx, h, inst); !ok {
		return bad
	}

	bin := a.ResolveBinary(inst)
	if bin == "" {
		return provider.Health{
			State: provider.HealthNotInstalled,
			Reason: "Claude Code was not found. Install the CLI, or set its path in " +
				"Settings → Providers.",
		}
	}
	if err := executableFile(ctx, h, bin); err != nil {
		return provider.Health{
			State:  provider.HealthNotInstalled,
			Binary: bin,
			Reason: fmt.Sprintf("Claude Code at %s cannot be run: %s.", bin, err),
		}
	}

	// The binary is there and executable, so a probe that does not come back is
	// a failed check, not a missing install: CLI start-up misses the deadline on
	// a busy machine. Reporting that as "not installed" would be a verdict —
	// KnownUnready, so follow-up tasks are not even filed for the provider —
	// where dispatchq in fact knows nothing yet.
	version, err := h.Probe(ctx, a.probeCommand(inst, bin, "--version"))
	if err != nil {
		return provider.Health{
			State:  provider.HealthCheckFailed,
			Binary: bin,
			Reason: fmt.Sprintf("Claude Code at %s did not answer --version (%s).", bin, redact(err.Error())),
		}
	}

	// The answer is in the output, not in the exit status: `claude auth status`
	// prints a perfectly good document and *still* exits non-zero when nobody is
	// logged in. Reading the exit code first would report a logged-out account as
	// "could not be checked" — vaguer than the truth, and weak enough that new
	// tasks would be filed for a provider that cannot run them.
	out, probeErr := h.Probe(ctx, a.probeCommand(inst, bin, "auth", "status", "--json"))
	var st authStatus
	if err := decodeStatus(out, &st); err != nil {
		reason := "Claude Code reported its login status in a format dispatchq does not understand."
		if probeErr != nil {
			reason = fmt.Sprintf("Claude Code at %s could not report its login status (%s).", bin, redact(probeErr.Error()))
		}
		return provider.Health{
			State:  provider.HealthCheckFailed,
			Binary: bin,
			Detail: firstLine(string(version)),
			Reason: reason,
		}
	}
	if !st.LoggedIn {
		return provider.Health{
			State:  provider.HealthNotAuthenticated,
			Binary: bin,
			Detail: firstLine(string(version)),
			Reason: "Claude Code is not logged in. Run `claude auth login` in a terminal.",
		}
	}
	return provider.Health{
		State:  provider.HealthReady,
		Binary: bin,
		Detail: strings.TrimSpace(firstLine(string(version)) + " · " + st.AuthMethod),
	}
}

// probeCommand builds a readiness probe for this instance — same binary, same
// configuration directory as a run, so the answer is about the account the jobs
// would actually use.
func (a *Adapter) probeCommand(inst provider.Instance, bin string, args ...string) provider.Command {
	return provider.Command{Path: bin, Args: args, Env: a.env(inst)}
}

// env is the instance's own environment: its account directory, when it has
// one, then the variables the operator set for it (an ANTHROPIC_API_KEY, say).
func (a *Adapter) env(inst provider.Instance) []string {
	var base []string
	if inst.ConfigDir != "" {
		base = append(base, ConfigDirEnv+"="+inst.ConfigDir)
	}
	return inst.Environ(base...)
}

// executableFile reports why a path cannot be run, or nil when it can. The
// instance's host applies the rule of its platform; only the reason is
// redacted here.
func executableFile(ctx context.Context, h provider.Host, p string) error {
	if err := h.Executable(ctx, p); err != nil {
		return fmt.Errorf("%s", redact(err.Error()))
	}
	return nil
}

// decodeStatus reads the first JSON object out of a CLI's combined output. The
// document can be surrounded by noise on either side — an update notice before
// it, a warning written to stderr after it — and neither is a reason to declare
// the whole queue's provider broken, so only the document itself is parsed.
func decodeStatus(out []byte, into any) error {
	if i := bytes.IndexByte(out, '{'); i > 0 {
		out = out[i:]
	}
	return json.NewDecoder(bytes.NewReader(out)).Decode(into)
}

// firstLine is the first non-empty line of s, for the short version detail
// shown on the provider card.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// redact keeps a probe's own words out of the reason when they could carry
// account data: only the first line is kept, and it is cut short. The CLI's
// login status names an email address and an organisation, and a reason string
// is written to the daemon log and served over the API.
func redact(s string) string {
	line := firstLine(s)
	const limit = 160
	if r := []rune(line); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return line
}

// aliasPref are the model tiers dispatchq knows Claude Code accepts, in the order
// it offers them. Every one is always selectable: the CLI's `--model` help text
// names only a couple as examples, so it cannot be read as the full set.
var aliasPref = []string{"opus", "sonnet", "haiku", "fable"}

// quotedRe finds the aliases the help text quotes.
var quotedRe = regexp.MustCompile(`'([a-zA-Z0-9-]+)'`)

// ListModels implements provider.Adapter: the known tiers, plus any further
// alias this binary's own `--help` advertises for --model. A binary that cannot
// be asked still yields the known tiers, because a model list is a suggestion
// and a provider whose catalog cannot be read runs perfectly well.
func (a *Adapter) ListModels(ctx context.Context, inst provider.Instance, h provider.Host) []provider.Model {
	return a.catalog.Lookup(inst, func() ([]provider.Model, bool) {
		bin := a.ResolveBinary(inst)
		if bin == "" {
			return orderAliases(nil), false
		}
		out, err := h.Probe(ctx, a.probeCommand(inst, bin, "--help"))
		if err != nil {
			return orderAliases(nil), false
		}
		return orderAliases(aliasesFromHelp(string(out))), true
	})
}

// aliasesFromHelp extracts the model aliases advertised in the --model help text.
func aliasesFromHelp(help string) []string {
	i := strings.Index(help, "--model <model>")
	if i < 0 {
		return nil
	}
	end := min(i+500, len(help))
	seen := map[string]bool{}
	var aliases []string
	for _, m := range quotedRe.FindAllStringSubmatch(help[i:end], -1) {
		tok := m[1]
		// Skip full model names (claude-fable-5 and the like); the aliases are
		// what a task should store, because they follow the latest release.
		if strings.HasPrefix(tok, "claude-") || seen[tok] {
			continue
		}
		seen[tok] = true
		aliases = append(aliases, tok)
	}
	return aliases
}

// orderAliases turns the advertised aliases into the selectable list: every
// known tier first, in aliasPref order, then anything else the help mentioned,
// in the order it appeared.
func orderAliases(aliases []string) []provider.Model {
	extra := map[string]bool{}
	for _, a := range aliases {
		extra[a] = true
	}
	out := make([]provider.Model, 0, len(aliasPref)+len(aliases))
	for _, pref := range aliasPref {
		out = append(out, provider.Model{ID: pref, Label: title(pref) + " (latest)"})
		delete(extra, pref)
	}
	for _, a := range aliases {
		if extra[a] {
			out = append(out, provider.Model{ID: a, Label: title(a)})
			delete(extra, a)
		}
	}
	return out
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// InteractiveResumeCommand implements provider.Adapter: `claude --resume` in the
// task's directory, with the same authority the run had — a task that skipped
// permission prompts resumes the same way, so the continued session behaves like
// the one it continues.
func (a *Adapter) InteractiveResumeCommand(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("claude-code: continuing a session needs its id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}
	args := []string{"--resume", req.SessionID}
	if access == provider.AccessFullAccess {
		args = append(args, "--dangerously-skip-permissions")
	}
	return provider.Command{Path: a.binary(inst), Args: args, Env: a.env(inst)}, nil
}
