package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/binpath"
)

// CheckHealth implements provider.Adapter. It costs no model usage: the binary,
// `codex --version`, then `codex login status`.
//
// It deliberately never runs `codex exec` to find out. The spike measured what
// that does without credentials: five WebSocket attempts, a fallback to HTTPS
// and five more — a readiness check that takes half a minute and looks like an
// outage.
func (a *Adapter) CheckHealth(ctx context.Context, inst provider.Instance, p provider.Prober) provider.Health {
	if h, ok := checkConfigDir(inst); !ok {
		return h
	}

	bin := a.ResolveBinary(inst)
	if bin == "" {
		return provider.Health{
			State: provider.HealthNotInstalled,
			Reason: "Codex was not found. Install the CLI, or set its path in " +
				"Settings → Providers.",
		}
	}
	if err := executableFile(bin); err != nil {
		return provider.Health{
			State:  provider.HealthNotInstalled,
			Binary: bin,
			Reason: fmt.Sprintf("Codex at %s cannot be run: %s.", bin, err),
		}
	}

	// The binary is there and executable, so a probe that does not come back is
	// a failed check, not a missing install: CLI start-up misses the deadline on
	// a busy machine. Reporting that as "not installed" would be a verdict —
	// KnownUnready, so follow-up tasks are not even filed for the provider —
	// where dispatchq in fact knows nothing yet.
	version, err := p.Probe(ctx, a.probeCommand(inst, bin, "--version"))
	if err != nil {
		return provider.Health{
			State:  provider.HealthCheckFailed,
			Binary: bin,
			Reason: fmt.Sprintf("Codex at %s did not answer --version (%s).", bin, redact(err.Error())),
		}
	}

	// `codex login status` says it in the exit status and writes the words to
	// stderr either way: "Logged in using ChatGPT" or "Not logged in".
	out, loginErr := p.Probe(ctx, a.probeCommand(inst, bin, "login", "status"))
	detail := firstLine(string(version))
	if loginErr != nil {
		// A non-zero exit means "not logged in" only when the CLI said so. A
		// probe that timed out or died says nothing about the account, and
		// reporting it as logged out would refuse new tasks for a provider that
		// may be perfectly fine.
		if !saysNotLoggedIn(string(out)) {
			return provider.Health{
				State:  provider.HealthCheckFailed,
				Binary: bin,
				Detail: detail,
				Reason: fmt.Sprintf("Codex at %s could not report its login status (%s).", bin, redact(loginErr.Error())),
			}
		}
		return provider.Health{
			State:  provider.HealthNotAuthenticated,
			Binary: bin,
			Detail: detail,
			Reason: "Codex is not logged in. Run `codex login` in a terminal.",
		}
	}
	if status := firstLine(string(out)); status != "" {
		detail = strings.TrimSpace(detail + " · " + status)
	}
	return provider.Health{State: provider.HealthReady, Binary: bin, Detail: detail}
}

// bundledModels is the fallback catalog: the two models the spike saw Codex
// bundle. It is a suggestion list, never validation — a model dispatchq has never
// heard of is passed to the harness unchanged.
var bundledModels = []provider.Model{
	{ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol"},
	{ID: "gpt-6-astra", Label: "GPT-6 Astra"},
}

// modelCatalog is what `codex debug models --bundled` answers with.
type modelCatalog struct {
	Models []struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"display_name"`
	} `json:"models"`
}

// ListModels implements provider.Adapter. The bundled catalog is a debug
// command, so its absence is not a problem worth reporting: whatever it cannot
// answer falls back to the small built-in list, and either way the result is
// only a suggestion for the UI.
func (a *Adapter) ListModels(ctx context.Context, inst provider.Instance, p provider.Prober) []provider.Model {
	return a.catalog.Lookup(inst, func() ([]provider.Model, bool) {
		bin := a.ResolveBinary(inst)
		if bin == "" {
			return bundledModels, false
		}
		out, err := p.Probe(ctx, a.probeCommand(inst, bin, "debug", "models", "--bundled"))
		if err != nil {
			return bundledModels, false
		}
		var cat modelCatalog
		if err := decodeJSON(out, &cat); err != nil || len(cat.Models) == 0 {
			return bundledModels, false
		}
		models := make([]provider.Model, 0, len(cat.Models))
		for _, m := range cat.Models {
			if m.Slug == "" {
				continue
			}
			label := m.DisplayName
			if label == "" {
				label = m.Slug
			}
			models = append(models, provider.Model{ID: m.Slug, Label: label})
		}
		if len(models) == 0 {
			return bundledModels, false
		}
		return models, true
	})
}

// saysNotLoggedIn reports whether the CLI itself said nobody is signed in, as
// opposed to the probe failing for reasons of its own.
func saysNotLoggedIn(out string) bool {
	return strings.Contains(strings.ToLower(out), "not logged in")
}

// checkConfigDir reports the instance's own settings as invalid when the account
// directory it names cannot be used.
func checkConfigDir(inst provider.Instance) (provider.Health, bool) {
	if inst.ConfigDir == "" {
		return provider.Health{}, true
	}
	fi, err := os.Stat(inst.ConfigDir)
	switch {
	case err != nil:
		return provider.Health{
			State:  provider.HealthInvalidConfiguration,
			Reason: fmt.Sprintf("The configuration directory %s is not accessible: %s.", inst.ConfigDir, redact(err.Error())),
		}, false
	case !fi.IsDir():
		return provider.Health{
			State:  provider.HealthInvalidConfiguration,
			Reason: fmt.Sprintf("The configuration directory %s is not a directory.", inst.ConfigDir),
		}, false
	}
	return provider.Health{}, true
}

// probeCommand builds a readiness probe for this instance — same binary, same
// account directory as a run, so the answer is about the account the jobs would
// actually use.
func (a *Adapter) probeCommand(inst provider.Instance, bin string, args ...string) provider.Command {
	return provider.Command{Path: bin, Args: args, Env: a.env(inst)}
}

// executableFile reports why a path cannot be run, or nil when it can. The
// platform rule lives in binpath; only the reason is redacted here.
func executableFile(p string) error {
	if err := binpath.CheckExecutable(p); err != nil {
		return fmt.Errorf("%s", redact(err.Error()))
	}
	return nil
}

// decodeJSON reads the first JSON value out of a CLI's combined output, which
// can be preceded by a notice and followed by a warning.
func decodeJSON(out []byte, into any) error {
	if i := strings.IndexByte(string(out), '{'); i > 0 {
		out = out[i:]
	}
	return json.NewDecoder(strings.NewReader(string(out))).Decode(into)
}

// firstLine is the first non-empty line of s, for the short detail shown on the
// provider card.
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
