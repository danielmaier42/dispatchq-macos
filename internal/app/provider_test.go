package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/adapters"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// The tests use the real adapter registry, because "is this kind implemented?"
// is exactly what the validation is for. Nothing here runs a CLI: readiness is
// asked of a checker built on a stub adapter (see readinessChecker).
func registry() *provider.Registry { return adapters.Default() }

func TestProvidersOnAFreshStore(t *testing.T) {
	s := openStore(t)
	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if set.DefaultID() != provider.DefaultInstanceID {
		t.Fatalf("default = %q, want the seeded claude instance", set.DefaultID())
	}
}

func TestAddEditEnableProvider(t *testing.T) {
	s := openStore(t)
	inst := provider.Instance{
		ID: "claude-secondary", Kind: provider.KindClaudeCode, Name: "Second account",
		ConfigDir: "/tmp/second", Enabled: true,
	}
	if err := AddProvider(s, registry(), inst); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := AddProvider(s, registry(), inst); err == nil {
		t.Fatal("a duplicate id must be refused")
	}

	if err := EditProvider(s, registry(), inst.ID, func(i *provider.Instance) error {
		i.DefaultModel = "haiku"
		return nil
	}); err != nil {
		t.Fatalf("EditProvider: %v", err)
	}
	if err := SetProviderEnabled(s, inst.ID, false); err != nil {
		t.Fatalf("SetProviderEnabled: %v", err)
	}

	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	got, ok := set.Lookup(inst.ID)
	if !ok {
		t.Fatal("the added provider is gone")
	}
	if got.DefaultModel != "haiku" || got.Enabled || got.ConfigDir != "/tmp/second" {
		t.Fatalf("instance = %+v, want the edited, switched-off one", got)
	}
}

func TestAddProviderRejectsAnInvalidConfiguration(t *testing.T) {
	s := openStore(t)
	err := AddProvider(s, registry(), provider.Instance{ID: "oc", Kind: "gremlin", Enabled: true})
	if !errors.Is(err, provider.ErrInvalidProvider) {
		t.Fatalf("err = %v, want ErrInvalidProvider", err)
	}
}

func TestEditProviderCannotChangeTheID(t *testing.T) {
	s := openStore(t)
	err := EditProvider(s, registry(), provider.DefaultInstanceID, func(i *provider.Instance) error {
		i.ID = "renamed"
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("err = %v, want the rename to be refused", err)
	}
}

func TestEditUnknownProvider(t *testing.T) {
	s := openStore(t)
	err := EditProvider(s, registry(), "nope", func(*provider.Instance) error { return nil })
	if !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
	if !strings.Contains(err.Error(), provider.DefaultInstanceID) {
		t.Fatalf("err = %q, want it to name the configured ids", err)
	}
}

// TestRemoveProviderRefusesWhileItIsUsed is the rule that keeps work from being
// silently re-pointed at another harness: every reference is named, and the
// operator changes them.
func TestRemoveProviderRefusesWhileItIsUsed(t *testing.T) {
	s := openStore(t)
	second := provider.Instance{ID: "second", Kind: provider.KindClaudeCode, Name: "second", Enabled: true}
	if err := AddProvider(s, registry(), second); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	tk := mk("a")
	tk.Provider = "second"
	if err := AddTask(s, tk); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	err := RemoveProvider(s, "second")
	if err == nil || !strings.Contains(err.Error(), "task a") {
		t.Fatalf("err = %v, want the referencing task to be named", err)
	}

	if err := SetDefaultProvider(s, "second"); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}
	if err := EditTask(s, "a", func(t *task.Task) error { t.Provider = ""; return nil }); err != nil {
		t.Fatalf("EditTask: %v", err)
	}
	if err := RemoveProvider(s, "second"); err == nil || !strings.Contains(err.Error(), "default provider") {
		t.Fatalf("err = %v, want the default-provider setting to be named", err)
	}

	if err := SetDefaultProvider(s, provider.DefaultInstanceID); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}
	if err := RemoveProvider(s, "second"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	set, _ := Providers(s)
	if _, ok := set.Lookup("second"); ok {
		t.Fatal("the provider is still configured")
	}
}

// TestRemoveProviderRefusesWhileItIsAFallback: the provider list must never
// hold a fallback that names nothing, so the reference is reported the same way
// a task's is.
func TestRemoveProviderRefusesWhileItIsAFallback(t *testing.T) {
	s := openStore(t)
	second := provider.Instance{ID: "second", Kind: provider.KindClaudeCode, Name: "second", Enabled: true}
	if err := AddProvider(s, registry(), second); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := EditProvider(s, registry(), provider.DefaultInstanceID, func(inst *provider.Instance) error {
		inst.FallbackProvider = "second"
		return nil
	}); err != nil {
		t.Fatalf("EditProvider: %v", err)
	}

	err := RemoveProvider(s, "second")
	if err == nil || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("err = %v, want the fallback reference to be named", err)
	}

	if err := EditProvider(s, registry(), provider.DefaultInstanceID, func(inst *provider.Instance) error {
		inst.FallbackProvider = ""
		return nil
	}); err != nil {
		t.Fatalf("EditProvider: %v", err)
	}
	if err := RemoveProvider(s, "second"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
}

// TestClearingTheFallbackDropsItsModel: a model chosen for a provider that no
// longer takes the work must not come back when a fallback is set again.
func TestClearingTheFallbackDropsItsModel(t *testing.T) {
	s := openStore(t)
	second := provider.Instance{ID: "second", Kind: provider.KindClaudeCode, Name: "second", Enabled: true}
	if err := AddProvider(s, registry(), second); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	set := func(id, model string) {
		t.Helper()
		if err := EditProvider(s, registry(), provider.DefaultInstanceID, func(inst *provider.Instance) error {
			inst.FallbackProvider, inst.FallbackModel = id, model
			return nil
		}); err != nil {
			t.Fatalf("EditProvider: %v", err)
		}
	}
	set("second", "opus")
	set("", "opus")

	providers, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	inst, _ := providers.Lookup(provider.DefaultInstanceID)
	if inst.FallbackModel != "" {
		t.Fatalf("fallback model = %q, want it gone with the fallback", inst.FallbackModel)
	}
}

// TestEditProviderRefusesABrokenFallback: a fallback that names nothing, or
// itself, would be a plan that cannot be carried out.
func TestEditProviderRefusesABrokenFallback(t *testing.T) {
	s := openStore(t)
	for _, id := range []string{"gone", provider.DefaultInstanceID} {
		err := EditProvider(s, registry(), provider.DefaultInstanceID, func(inst *provider.Instance) error {
			inst.FallbackProvider = id
			return nil
		})
		if err == nil {
			t.Fatalf("fallback %q: expected the edit to be refused", id)
		}
	}
}

func TestRemoveTheOnlyProviderIsRefused(t *testing.T) {
	s := openStore(t)
	if err := RemoveProvider(s, provider.DefaultInstanceID); err == nil {
		t.Fatal("removing the only provider must be refused")
	}
}

func TestSetDefaultProviderRejectsAnUnknownID(t *testing.T) {
	s := openStore(t)
	if err := SetDefaultProvider(s, "nope"); !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
}

// stubAdapter answers readiness from a scripted verdict, so EnsureRunnable is
// tested without an installed CLI.
type stubAdapter struct {
	kind   provider.Kind
	health provider.Health
}

func (s stubAdapter) Kind() provider.Kind                 { return s.kind }
func (s stubAdapter) Capabilities() provider.Capabilities { return provider.Capabilities{} }

func (s stubAdapter) Describe() provider.Description { return provider.Description{Name: "Stub"} }
func (s stubAdapter) DetectBinary() string           { return "" }
func (s stubAdapter) ResolveBinary(provider.Instance) string {
	return ""
}

func (s stubAdapter) CheckHealth(context.Context, provider.Instance, provider.Host) provider.Health {
	return s.health
}

func (s stubAdapter) ListModels(context.Context, provider.Instance, provider.Host) []provider.Model {
	return nil
}

func (s stubAdapter) InteractiveResumeCommand(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}

func (s stubAdapter) Command(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}
func (s stubAdapter) NewParser() provider.Parser { return nil }

func (s stubAdapter) AsideCommand(provider.Instance, provider.AsideRequest) (provider.Command, error) {
	return provider.Command{}, provider.ErrUnsupported
}

func (s stubAdapter) ParseAside([]byte) (provider.Aside, error) {
	return provider.Aside{}, provider.ErrUnsupported
}

func readinessChecker(h provider.Health) *provider.Checker {
	return provider.NewChecker(provider.NewRegistry(stubAdapter{kind: provider.KindClaudeCode, health: h}))
}

func TestEnsureRunnable(t *testing.T) {
	s := openStore(t)
	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	ctx := context.Background()

	ready := readinessChecker(provider.Health{State: provider.HealthReady})
	if err := EnsureRunnable(ctx, set, ready, ""); err != nil {
		t.Fatalf("a ready default provider must be accepted: %v", err)
	}
	if err := EnsureRunnable(ctx, set, ready, provider.DefaultInstanceID); err != nil {
		t.Fatalf("a ready named provider must be accepted: %v", err)
	}
	if err := EnsureRunnable(ctx, set, ready, "codex"); !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}

	missing := readinessChecker(provider.Health{
		State:  provider.HealthNotInstalled,
		Reason: "Claude Code was not found.",
	})
	err = EnsureRunnable(ctx, set, missing, "")
	if err == nil || !strings.Contains(err.Error(), "Claude Code was not found") {
		t.Fatalf("err = %v, want the readiness reason to be carried through", err)
	}
}

func TestEnsureRunnableRefusesADisabledProvider(t *testing.T) {
	s := openStore(t)
	if err := AddProvider(s, registry(), provider.Instance{
		ID: "second", Kind: provider.KindClaudeCode, Name: "second", Enabled: true,
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := SetDefaultProvider(s, "second"); err != nil {
		t.Fatalf("SetDefaultProvider: %v", err)
	}
	if err := SetProviderEnabled(s, provider.DefaultInstanceID, false); err != nil {
		t.Fatalf("SetProviderEnabled: %v", err)
	}
	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	ready := readinessChecker(provider.Health{State: provider.HealthReady})
	err = EnsureRunnable(context.Background(), set, ready, provider.DefaultInstanceID)
	if !errors.Is(err, provider.ErrProviderDisabled) {
		t.Fatalf("err = %v, want ErrProviderDisabled", err)
	}
}

// TestDefaultProviderStaysUsable: the instance every task without a provider
// runs on cannot be switched off or removed from under them.
func TestDefaultProviderStaysUsable(t *testing.T) {
	s := openStore(t)
	if err := SetProviderEnabled(s, provider.DefaultInstanceID, false); err == nil {
		t.Fatal("switching off the default provider must be refused")
	}
	if err := AddProvider(s, registry(), provider.Instance{
		ID: "off", Kind: provider.KindClaudeCode, Name: "off",
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := SetDefaultProvider(s, "off"); err == nil {
		t.Fatal("making a switched-off provider the default must be refused")
	}
}

// TestEnsureRunnableAcceptsAnUnconfirmedProvider: "I could not ask" is not "it
// does not work". A run that is demonstrably going must not lose the follow-up
// it just queued because one probe timed out.
func TestEnsureRunnableAcceptsAnUnconfirmedProvider(t *testing.T) {
	s := openStore(t)
	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	unknown := readinessChecker(provider.Health{
		State: provider.HealthCheckFailed, Reason: "the CLI did not answer in time",
	})
	if err := EnsureRunnable(context.Background(), set, unknown, ""); err != nil {
		t.Fatalf("an unconfirmed provider must not refuse the task: %v", err)
	}
}

// TestRemovedProviderForgetsItsNotificationMemo keeps a re-added id from
// inheriting the health state the operator was told about for a different
// instance.
func TestRemovedProviderForgetsItsNotificationMemo(t *testing.T) {
	s := openStore(t)
	if err := AddProvider(s, registry(), provider.Instance{
		ID: "second", Kind: provider.KindClaudeCode, Name: "second", Enabled: true,
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if err := s.UpdateState(func(st *store.State) error {
		st.SetNotifiedProviderState("second", "not_installed")
		return nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if err := RemoveProvider(s, "second"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := st.NotifiedProviderState("second"); got != "" {
		t.Fatalf("memo = %q, want it forgotten with the provider", got)
	}
}

// TestProviderPathsAcceptATilde: a path field that rejects "~/…" is a papercut,
// and the stored value is the resolved one, so the file says what is used.
func TestProviderPathsAcceptATilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory here: %v", err)
	}
	s := openStore(t)
	if err := EditProvider(s, registry(), provider.DefaultInstanceID, func(i *provider.Instance) error {
		i.BinaryPath = "~/.local/bin/claude"
		i.ConfigDir = "~/.claude_team"
		return nil
	}); err != nil {
		t.Fatalf("EditProvider: %v", err)
	}
	set, err := Providers(s)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	got, _ := set.Lookup(provider.DefaultInstanceID)
	if got.BinaryPath != filepath.Join(home, ".local/bin/claude") {
		t.Fatalf("binary path = %q, want it expanded", got.BinaryPath)
	}
	if got.ConfigDir != filepath.Join(home, ".claude_team") {
		t.Fatalf("config dir = %q, want it expanded", got.ConfigDir)
	}

	// A relative path is still refused — expansion is not a licence for one.
	err = EditProvider(s, registry(), provider.DefaultInstanceID, func(i *provider.Instance) error {
		i.ConfigDir = "relative/dir"
		return nil
	})
	if !errors.Is(err, provider.ErrInvalidProvider) {
		t.Fatalf("err = %v, want a relative path still refused", err)
	}
}
