package opencode

import (
	"context"
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// CheckHealth implements provider.Adapter. It costs no model usage: the
// binary, then `opencode --version`.
//
// opencode has no single logged-in/logged-out state the way Claude Code and
// Codex do — it is a pass-through to whichever providers an operator has
// configured (a local LM Studio server needs no login at all), and running
// directly against it turned up no command that reports a yes/no verdict
// covering all of them. So unlike the other two adapters, this one never
// reports HealthNotAuthenticated: a binary that answers --version is read as
// ready, and whether a given run's chosen provider is actually usable is left
// for the run itself to discover.
func (a *Adapter) CheckHealth(ctx context.Context, inst provider.Instance, h provider.Host) provider.Health {
	if bad, ok := provider.CheckConfigDir(ctx, h, inst); !ok {
		return bad
	}

	bin := a.ResolveBinary(inst)
	if bin == "" {
		return provider.Health{
			State: provider.HealthNotInstalled,
			Reason: "opencode was not found. Install the CLI, or set its path in " +
				"Settings → Providers.",
		}
	}
	if err := executableFile(ctx, h, bin); err != nil {
		return provider.Health{
			State:  provider.HealthNotInstalled,
			Binary: bin,
			Reason: fmt.Sprintf("opencode at %s cannot be run: %s.", bin, err),
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
			Reason: fmt.Sprintf("opencode at %s did not answer --version (%s).", bin, redact(err.Error())),
		}
	}
	return provider.Health{State: provider.HealthReady, Binary: bin, Detail: firstLine(string(version))}
}

// modelLine is a model advertised by `opencode models`, in the plain
// "provider/model[/variant]" form it prints one per line. A model id is
// exactly what `-m`/`--model` takes, and an error message ("Provider not
// found: …") is filtered out by requiring both a "/" and no whitespace —
// every id observed running the CLI directly had one and never the other.
func modelLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.ContainsAny(line, " \t") || !strings.Contains(line, "/") {
		return "", false
	}
	return line, true
}

// ListModels implements provider.Adapter. `opencode models` is a cheap local
// lookup — it lists whatever the configured providers advertise, not a
// catalog opencode itself ships — so a binary that cannot be asked yields no
// suggestions rather than an invented list: unlike Codex or Claude Code,
// opencode has no models of its own to fall back to.
func (a *Adapter) ListModels(ctx context.Context, inst provider.Instance, h provider.Host) []provider.Model {
	return a.catalog.Lookup(inst, func() ([]provider.Model, bool) {
		bin := a.ResolveBinary(inst)
		if bin == "" {
			return nil, false
		}
		out, err := h.Probe(ctx, a.probeCommand(inst, bin, "models"))
		if err != nil {
			return nil, false
		}
		var models []provider.Model
		for _, line := range strings.Split(string(out), "\n") {
			if id, ok := modelLine(line); ok {
				models = append(models, provider.Model{ID: id, Label: id})
			}
		}
		if len(models) == 0 {
			return nil, false
		}
		return models, true
	})
}

// probeCommand builds a readiness probe for this instance — same binary, same
// account directory as a run, so the answer is about the account the jobs
// would actually use.
func (a *Adapter) probeCommand(inst provider.Instance, bin string, args ...string) provider.Command {
	return provider.Command{Path: bin, Args: args, Env: a.env(inst)}
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

// firstLine is the first non-empty line of s, for the short detail shown on
// the provider card.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// redact keeps a probe's own words out of a reason that is logged and served
// over the API: the first line only, cut short.
func redact(s string) string {
	line := firstLine(s)
	const limit = 160
	if r := []rune(line); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return line
}
