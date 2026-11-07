package app

// Provider instances: reading, adding, editing and removing the configured
// harnesses, plus which of them is the default. The rules live here so the CLI
// and the HTTP API cannot disagree about what a valid change is.

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// Providers returns the configured provider instances and which one is the
// default. The store seeds them, so a fresh installation reports the Claude
// Code instance rather than an empty list.
func Providers(s *store.Store) (provider.Set, error) {
	cfg, err := s.LoadConfig()
	if err != nil {
		return provider.Set{}, err
	}
	return provider.FromConfig(cfg)
}

// EnsureRunnable refuses to file work for a provider that cannot run it: an id
// nothing is configured under, or a configured instance that is switched off,
// not installed or not logged in. Filing it anyway would put a job in the queue
// that is known in advance to fail at three in the morning, which is the one
// thing an unattended queue must not do. An empty id means the default provider.
//
// A check that could not reach a verdict at all does not refuse. "I could not
// ask" is not "it does not work", and throwing away a follow-up task a running
// job just asked for — on a provider that is demonstrably running it — would be
// the worse mistake. The scheduler still refuses to *start* such a task, and
// the queue says it is blocked.
func EnsureRunnable(ctx context.Context, set provider.Set, ch *provider.Checker, providerID string) error {
	resolved, err := set.Resolve(provider.Selection{ProviderID: providerID})
	if err != nil {
		return err
	}
	if h := ch.Check(ctx, resolved.Instance); h.KnownUnready() {
		return fmt.Errorf("provider %q is not ready: %s",
			resolved.Instance.ID, h.ReasonOr("it cannot run tasks right now"))
	}
	return nil
}

// AddProvider stores a new provider instance. The id must be free and the whole
// configuration must still be valid afterwards.
func AddProvider(s *store.Store, reg *provider.Registry, inst provider.Instance) error {
	if err := prepareExecution(reg, &inst); err != nil {
		return err
	}
	normalizeFallback(&inst)
	if err := provider.Validate(reg, inst); err != nil {
		return err
	}
	return s.UpdateConfig(func(cfg *store.Config) error {
		if providerIndex(cfg.Providers, inst.ID) >= 0 {
			return fmt.Errorf("provider %q already exists", inst.ID)
		}
		if poolIndex(cfg.Pools, inst.ID) >= 0 {
			return fmt.Errorf("a pool already has the id %q", inst.ID)
		}
		cfg.Providers = append(cfg.Providers, inst.Stored())
		return nil
	})
}

// EditProvider applies a change to one configured instance in place. The id is
// fixed: a task, a run's history and the pending resumes all name it, so apply
// must not change it.
func EditProvider(s *store.Store, reg *provider.Registry, id string, apply func(*provider.Instance) error) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		idx := providerIndex(cfg.Providers, id)
		if idx < 0 {
			return unknownProvider(cfg, id)
		}
		edited := provider.InstanceOf(cfg.Providers[idx])
		before := edited.Execution
		if err := apply(&edited); err != nil {
			return err
		}
		if edited.ID != id {
			return fmt.Errorf("provider id cannot be changed (%q -> %q)", id, edited.ID)
		}
		if edited.Execution != before {
			// Sessions, pending resumes and limit state belong to the
			// environment they were made in; moving the instance would point
			// them at an account that never saw them.
			return fmt.Errorf("provider %q runs in %s; that cannot be changed. To run in another environment, add a new provider",
				id, before.Label())
		}
		if err := prepareExecution(reg, &edited); err != nil {
			return err
		}
		normalizeFallback(&edited)
		if err := provider.Validate(reg, edited); err != nil {
			return err
		}
		if err := checkDefaultStaysUsable(*cfg, id, edited.Enabled); err != nil {
			return err
		}
		cfg.Providers[idx] = edited.Stored()
		return nil
	})
}

// SetProviderEnabled switches an instance on or off without removing it. A
// disabled instance keeps its configuration and its tasks; those tasks stay
// queued and report why they are blocked.
func SetProviderEnabled(s *store.Store, id string, enabled bool) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		idx := providerIndex(cfg.Providers, id)
		if idx < 0 {
			return unknownProvider(cfg, id)
		}
		if err := checkDefaultStaysUsable(*cfg, id, enabled); err != nil {
			return err
		}
		cfg.Providers[idx].Enabled = enabled
		return nil
	})
}

// RemoveProvider deletes a provider instance. It refuses while anything still
// names it and lists every reference, because silently re-pointing that work at
// another provider is exactly the substitution dispatchq never makes.
func RemoveProvider(s *store.Store, id string) error {
	err := s.UpdateConfig(func(cfg *store.Config) error {
		idx := providerIndex(cfg.Providers, id)
		if idx < 0 {
			return unknownProvider(cfg, id)
		}
		if len(cfg.Providers) == 1 {
			return fmt.Errorf("provider %q is the only one configured; add another before removing it", id)
		}
		if refs := providerRefs(*cfg, id); len(refs) > 0 {
			return fmt.Errorf("provider %q is still used by %s; change those first",
				id, strings.Join(refs, ", "))
		}
		cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
		return nil
	})
	if err != nil {
		return err
	}
	return s.UpdateState(func(st *store.State) error {
		st.ForgetProvider(id)
		return nil
	})
}

// SetDefaultProvider chooses the instance a task runs on when it names none. A
// switched-off instance is refused: it would block every task that names no
// provider, which is most of them.
func SetDefaultProvider(s *store.Store, id string) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		idx := providerIndex(cfg.Providers, id)
		if idx < 0 {
			return unknownProvider(cfg, id)
		}
		if !cfg.Providers[idx].Enabled {
			return fmt.Errorf("provider %q is switched off; switch it on before making it the default", id)
		}
		cfg.Settings.DefaultProvider = id
		return nil
	})
}

// wslSaveTimeout bounds the questions saving a WSL provider asks WSL.
const wslSaveTimeout = 30 * time.Second

// prepareExecution resolves a leading "~" in the instance's two paths before
// anything is checked or stored. A native instance expands it with this
// computer's home, without asking anything else. A WSL instance expands it
// with the Linux user's home, and its distribution must be installed: that is
// the one moment saving a provider starts wsl.exe, and only because the
// operator chose Linux.
func prepareExecution(reg *provider.Registry, inst *provider.Instance) error {
	if inst.Execution.IsNative() {
		expandPaths(inst)
		return nil
	}
	if err := provider.CheckExecution(reg, inst.Execution); err != nil {
		return fmt.Errorf("%w %q: %w", provider.ErrInvalidProvider, inst.ID, err)
	}
	svc := reg.WSL()
	ctx, cancel := context.WithTimeout(context.Background(), wslSaveTimeout)
	defer cancel()
	distros, err := svc.Distros(ctx)
	if err != nil {
		return fmt.Errorf("listing the WSL distributions: %w", err)
	}
	known := false
	for _, d := range distros {
		known = known || d.Name == inst.Execution.Distro
	}
	if !known {
		return fmt.Errorf("%w %q: WSL has no distribution called %q", provider.ErrInvalidProvider, inst.ID, inst.Execution.Distro)
	}
	if !hasTilde(inst.BinaryPath) && !hasTilde(inst.ConfigDir) {
		return nil
	}
	found, err := svc.Detect(ctx, inst.Execution, inst.Kind)
	if err != nil {
		return fmt.Errorf("finding the Linux home directory: %w", err)
	}
	inst.BinaryPath = linuxHome(inst.BinaryPath, found.Home)
	inst.ConfigDir = linuxHome(inst.ConfigDir, found.Home)
	return nil
}

func hasTilde(p string) bool { return p == "~" || strings.HasPrefix(p, "~/") }

// linuxHome expands "~" and "~/…" against a Linux home directory.
func linuxHome(p, home string) string {
	if !hasTilde(p) || home == "" {
		return p
	}
	return path.Join(home, strings.TrimPrefix(p, "~"))
}

// expandPaths resolves a leading "~" in the instance's two paths before anything
// is checked or stored, so the file holds the path that is actually used rather
// than one the daemon would have to expand again on every run.
func expandPaths(inst *provider.Instance) {
	inst.BinaryPath = provider.ExpandHome(inst.BinaryPath)
	inst.ConfigDir = provider.ExpandHome(inst.ConfigDir)
}

// normalizeFallback drops a fallback model that has no fallback to run on, so
// the file never holds a setting that does nothing — and so switching the
// fallback off and on again does not silently bring back a model chosen for a
// provider that is no longer the one taking the work.
func normalizeFallback(inst *provider.Instance) {
	if inst.FallbackProvider == "" {
		inst.FallbackModel = ""
	}
}

// checkDefaultStaysUsable refuses to switch off the instance that tasks naming
// no provider run on. Allowing it would block most of the queue behind a switch
// whose effect is not obvious from where it sits.
func checkDefaultStaysUsable(cfg store.Config, id string, enabled bool) error {
	if enabled || cfg.Settings.DefaultProvider != id {
		return nil
	}
	return fmt.Errorf("provider %q is the default; make another one the default before switching it off", id)
}

// providerRefs lists, in words the operator can act on, everything that names a
// provider id.
func providerRefs(cfg store.Config, id string) []string {
	var refs []string
	if cfg.Settings.DefaultProvider == id {
		refs = append(refs, "the default provider setting")
	}
	var fallbacks []string
	for _, p := range cfg.Providers {
		if p.FallbackProvider == id {
			fallbacks = append(fallbacks, p.ID)
		}
	}
	if len(fallbacks) > 0 {
		refs = append(refs, "the rate-limit fallback of provider "+strings.Join(fallbacks, ", "))
	}
	var pools []string
	for _, p := range cfg.Pools {
		for _, m := range p.Members {
			if m.Provider == id {
				pools = append(pools, p.ID)
			}
		}
	}
	if len(pools) > 0 {
		refs = append(refs, "pool "+strings.Join(pools, ", "))
	}
	var tasks []string
	for _, t := range cfg.Tasks {
		if t.Provider == id {
			tasks = append(tasks, t.ID)
		}
	}
	if len(tasks) > 0 {
		refs = append(refs, "task "+strings.Join(tasks, ", "))
	}
	return refs
}

func providerIndex(ps []store.Provider, id string) int {
	for i, p := range ps {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// unknownProvider names the ids that do exist, so a typo is answered with the
// answer rather than with another lookup.
func unknownProvider(cfg *store.Config, id string) error {
	known := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		known = append(known, p.ID)
	}
	if len(known) == 0 {
		return fmt.Errorf("%w %q", provider.ErrUnknownProvider, id)
	}
	return fmt.Errorf("%w %q (configured: %s)", provider.ErrUnknownProvider, id, strings.Join(known, ", "))
}
