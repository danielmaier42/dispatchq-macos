package provider

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

func testSet(t *testing.T) Set {
	t.Helper()
	s, err := NewSet("claude", []Instance{
		{ID: "claude", Kind: KindClaudeCode, Name: "Claude Code", DefaultModel: "sonnet", Enabled: true},
		{ID: "fake", Kind: "fake", Name: "Fake", DefaultModel: "fake-default", Enabled: true},
		{ID: "off", Kind: "fake", Name: "Off", DefaultModel: "irrelevant", Enabled: false},
		{ID: "nodefault", Kind: "fake", Name: "No default", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	return s
}

// TestResolveFollowsTheInheritanceTable walks the documented resolution table
// row by row. The crucial row is the third: naming only a provider must take
// that provider's own default model, never carry one over from the old one.
func TestResolveFollowsTheInheritanceTable(t *testing.T) {
	tests := []struct {
		name      string
		sel       Selection
		wantID    string
		wantModel string
	}{
		{
			name:      "neither specified: default provider and its default model",
			sel:       Selection{},
			wantID:    "claude",
			wantModel: "sonnet",
		},
		{
			name:      "only a model: default provider, explicit model",
			sel:       Selection{Model: "opus"},
			wantID:    "claude",
			wantModel: "opus",
		},
		{
			name:      "only a provider: that provider's default model",
			sel:       Selection{ProviderID: "fake"},
			wantID:    "fake",
			wantModel: "fake-default",
		},
		{
			name:      "both: exactly what was asked for",
			sel:       Selection{ProviderID: "fake", Model: "fake-pro"},
			wantID:    "fake",
			wantModel: "fake-pro",
		},
		{
			name:      "a provider without a default model leaves the harness to choose",
			sel:       Selection{ProviderID: "nodefault"},
			wantID:    "nodefault",
			wantModel: "",
		},
	}

	set := testSet(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := set.Resolve(tc.sel)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Instance.ID != tc.wantID {
				t.Fatalf("provider = %q, want %q", got.Instance.ID, tc.wantID)
			}
			if got.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", got.Model, tc.wantModel)
			}
		})
	}
}

func TestResolveNeverCarriesAModelIntoAnotherProvider(t *testing.T) {
	set := testSet(t)
	// What the parent ran with.
	parent, err := set.Resolve(Selection{Model: "opus"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if parent.Model != "opus" {
		t.Fatalf("parent model = %q, want opus", parent.Model)
	}
	// Switching provider without naming a model must land on the new
	// provider's default, not on "opus", which the new provider knows nothing
	// about.
	child, err := set.Resolve(Selection{ProviderID: "fake"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if child.Model != "fake-default" {
		t.Fatalf("model = %q, want the new provider's default", child.Model)
	}
}

func TestResolveRefusesInsteadOfSubstituting(t *testing.T) {
	set := testSet(t)
	tests := []struct {
		name    string
		sel     Selection
		wantErr error
	}{
		{name: "unknown provider", sel: Selection{ProviderID: "codex-work"}, wantErr: ErrUnknownProvider},
		{name: "disabled provider", sel: Selection{ProviderID: "off"}, wantErr: ErrProviderDisabled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := set.Resolve(tc.sel)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got.Instance.ID != "" {
				t.Fatalf("a failed resolution must not name a provider, got %q", got.Instance.ID)
			}
		})
	}
}

func TestResolveWithoutAnyProviderConfigured(t *testing.T) {
	empty, err := NewSet("", nil)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	if _, err := empty.Resolve(Selection{}); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("err = %v, want ErrNoProviders", err)
	}
}

func TestNewSetRejectsAnUnusableInstanceList(t *testing.T) {
	tests := []struct {
		name      string
		defaultID string
		instances []Instance
	}{
		{name: "empty id", instances: []Instance{{Kind: KindClaudeCode}}},
		{
			name:      "duplicate id",
			instances: []Instance{{ID: "claude", Kind: KindClaudeCode}, {ID: "claude", Kind: KindClaudeCode}},
		},
		{
			name:      "default names nothing",
			defaultID: "missing",
			instances: []Instance{{ID: "claude", Kind: KindClaudeCode}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSet(tc.defaultID, tc.instances); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestFromConfigReadsTheStoredInstances is the compatibility case: the store
// migrates an existing dispatchq configuration into the `claude` instance, and a
// task that names no provider keeps running exactly as before.
func TestFromConfigReadsTheStoredInstances(t *testing.T) {
	cfg := store.Config{
		Settings: store.Settings{DefaultProvider: DefaultInstanceID},
		Providers: []store.Provider{{
			ID: DefaultInstanceID, Kind: store.DefaultProviderKind, Name: DefaultInstanceName,
			BinaryPath: "/opt/claude", DefaultModel: "opus", Enabled: true,
		}},
	}
	set, err := FromConfig(cfg)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	all := set.All()
	if len(all) != 1 {
		t.Fatalf("got %d instances, want the one that is configured", len(all))
	}
	want := Instance{
		ID: DefaultInstanceID, Kind: KindClaudeCode, Name: DefaultInstanceName,
		BinaryPath: "/opt/claude", DefaultModel: "opus", Enabled: true,
	}
	if !reflect.DeepEqual(all[0], want) {
		t.Fatalf("instance = %+v, want %+v", all[0], want)
	}

	res, err := set.Resolve(Selection{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Instance.ID != DefaultInstanceID || res.Model != "opus" {
		t.Fatalf("resolved %+v, want the stored instance on its default model", res)
	}
}

// TestFromConfigWithoutADefaultUsesTheFirstInstance covers a configuration whose
// default_provider is empty: a task that names none must still run somewhere
// obvious rather than failing to resolve.
func TestFromConfigWithoutADefaultUsesTheFirstInstance(t *testing.T) {
	set, err := FromConfig(store.Config{Providers: []store.Provider{
		{ID: "claude", Kind: store.DefaultProviderKind, Enabled: true},
		{ID: "second", Kind: store.DefaultProviderKind, Enabled: true},
	}})
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if set.DefaultID() != "claude" {
		t.Fatalf("default = %q, want the first configured instance", set.DefaultID())
	}
}

// TestFromConfigRejectsAConfigurationWithoutProviders guards the seam with the
// store: nothing invents a provider behind the operator's back, so a Config
// that was never migrated is an error rather than a guess.
func TestFromConfigRejectsAConfigurationWithoutProviders(t *testing.T) {
	if _, err := FromConfig(store.Config{}); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("err = %v, want ErrNoProviders", err)
	}
}

// TestStoredRoundTripsAnInstance pins the mapping between the domain type and
// the shape config.toml holds, which is what keeps a saved provider identical
// after a reload.
func TestStoredRoundTripsAnInstance(t *testing.T) {
	inst := Instance{
		ID: "claude-secondary", Kind: KindClaudeCode, Name: "Second account",
		BinaryPath: "/opt/claude", ConfigDir: "/tmp/other", DefaultModel: "haiku", Enabled: true,
	}
	if got := InstanceOf(inst.Stored()); !reflect.DeepEqual(got, inst) {
		t.Fatalf("round trip = %+v, want %+v", got, inst)
	}
}

func TestLookupAndAllDoNotAliasTheSet(t *testing.T) {
	set := testSet(t)
	all := set.All()
	all[0].DefaultModel = "tampered"
	again, ok := set.Lookup("claude")
	if !ok {
		t.Fatal("claude should be configured")
	}
	if again.DefaultModel != "sonnet" {
		t.Fatalf("the set was mutated through All(): %q", again.DefaultModel)
	}
}

// fallbackSet is a chain of three instances: claude falls back to spare, spare
// to codex. The kinds differ on the last hop, which is where a model name stops
// travelling.
func fallbackSet(t *testing.T) Set {
	t.Helper()
	s, err := NewSet("claude", []Instance{
		{ID: "claude", Kind: KindClaudeCode, Name: "Claude", DefaultModel: "sonnet", FallbackProvider: "spare", Enabled: true},
		{ID: "spare", Kind: KindClaudeCode, Name: "Spare", DefaultModel: "haiku", FallbackProvider: "codex", FallbackModel: "gpt-high", Enabled: true},
		{ID: "codex", Kind: KindCodex, Name: "Codex", DefaultModel: "gpt", Enabled: true},
		{ID: "off", Kind: KindClaudeCode, Name: "Off", FallbackProvider: "claude", Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	return s
}

// limitedIDs is the availability of a set where the named instances are out of
// allowance and everything else can take work.
func limitedIDs(blocked map[string]bool) Availability {
	return Availability{OutOfAllowance: func(id string) bool { return blocked[id] }}
}

// everythingLimited is the availability of a set where no account has anything
// left at all.
var everythingLimited = Availability{OutOfAllowance: func(string) bool { return true }}

// TestResolveAvailableFollowsTheFallbackChain is the rate-limit substitution:
// the only one dispatchq makes, and only as far as the chain actually reaches.
func TestResolveAvailableFollowsTheFallbackChain(t *testing.T) {
	tests := []struct {
		name         string
		sel          Selection
		blocked      []string
		wantProvider string
		wantModel    string
		wantFrom     string
	}{
		{
			name: "an open provider is never substituted",
			sel:  Selection{ProviderID: "claude"}, wantProvider: "claude", wantModel: "sonnet",
		},
		{
			name: "a blocked provider hands over to its fallback",
			sel:  Selection{ProviderID: "claude"}, blocked: []string{"claude"},
			wantProvider: "spare", wantModel: "haiku", wantFrom: "claude",
		},
		{
			name: "the model travels to another account of the same harness",
			sel:  Selection{ProviderID: "claude", Model: "opus"}, blocked: []string{"claude"},
			wantProvider: "spare", wantModel: "opus", wantFrom: "claude",
		},
		{
			name: "the model the handing-over provider named wins",
			sel:  Selection{ProviderID: "claude", Model: "opus"}, blocked: []string{"claude", "spare"},
			wantProvider: "codex", wantModel: "gpt-high", wantFrom: "claude",
		},
		{
			name: "a chain that is blocked end to end stays where it was",
			sel:  Selection{ProviderID: "claude"}, blocked: []string{"claude", "spare", "codex"},
			wantProvider: "claude", wantModel: "sonnet",
		},
		{
			name: "a provider without a fallback stays where it was",
			sel:  Selection{ProviderID: "codex"}, blocked: []string{"codex"},
			wantProvider: "codex", wantModel: "gpt",
		},
	}

	set := fallbackSet(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocked := map[string]bool{}
			for _, id := range tc.blocked {
				blocked[id] = true
			}
			got, err := set.ResolveAvailable(tc.sel, limitedIDs(blocked))
			if err != nil {
				t.Fatalf("ResolveAvailable: %v", err)
			}
			if got.Instance.ID != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", got.Instance.ID, tc.wantProvider)
			}
			if got.Model != tc.wantModel {
				t.Fatalf("model = %q, want %q", got.Model, tc.wantModel)
			}
			if got.FallbackFrom.ID != tc.wantFrom {
				t.Fatalf("fallback from %q, want %q", got.FallbackFrom.ID, tc.wantFrom)
			}
			if got.Substituted() != (tc.wantFrom != "") {
				t.Fatalf("Substituted() = %t", got.Substituted())
			}
		})
	}
}

// TestResolveAvailableStepsOverAHopThatCannotWork: a hop is not the provider
// the task named, so one that is switched off or not ready is stepped over
// rather than ending the chain — nothing is substituted by skipping it.
func TestResolveAvailableStepsOverAHopThatCannotWork(t *testing.T) {
	set, err := NewSet("claude", []Instance{
		{ID: "claude", Kind: KindClaudeCode, FallbackProvider: "off", Enabled: true},
		{ID: "off", Kind: KindClaudeCode, FallbackProvider: "loggedout", Enabled: false},
		{ID: "loggedout", Kind: KindClaudeCode, FallbackProvider: "spare", Enabled: true},
		{ID: "spare", Kind: KindClaudeCode, Name: "Spare", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	av := limitedIDs(map[string]bool{"claude": true})
	av.CanTakeWork = func(inst Instance) bool { return inst.ID != "loggedout" }
	got, err := set.ResolveAvailable(Selection{ProviderID: "claude"}, av)
	if err != nil {
		t.Fatalf("ResolveAvailable: %v", err)
	}
	if got.Instance.ID != "spare" {
		t.Fatalf("provider = %q, want the first hop that can actually work", got.Instance.ID)
	}
}

// TestResolveAvailableStepsOverAHopReadAsUsedUp: a hop whose last reading shows
// a window used up until a reset still ahead is stepped over like a blocked
// one. A reading whose reset has passed, or that names none, does not count.
func TestResolveAvailableStepsOverAHopReadAsUsedUp(t *testing.T) {
	now := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	ahead, past := now.Add(24*time.Hour), now.Add(-time.Minute)
	for _, tc := range []struct {
		name  string
		spare Limits
		want  string
	}{
		{"used up until a reset ahead", Limits{Windows: []LimitWindow{{ID: "week", UsedPercent: 100, ResetsAt: &ahead}}}, "codex"},
		{"five hours used up", Limits{Windows: []LimitWindow{{ID: "five_hour", UsedPercent: 100, ResetsAt: &ahead}, {ID: "week", UsedPercent: 20, ResetsAt: &ahead}}}, "codex"},
		{"reset already passed", Limits{Windows: []LimitWindow{{ID: "week", UsedPercent: 100, ResetsAt: &past}}}, "spare"},
		{"no reset known", Limits{Windows: []LimitWindow{{ID: "week", UsedPercent: 100}}}, "spare"},
		{"room left", Limits{Windows: []LimitWindow{{ID: "week", UsedPercent: 90, ResetsAt: &ahead}}}, "spare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			av := limitedIDs(map[string]bool{"claude": true})
			av.Now = func() time.Time { return now }
			av.Limits = func(inst Instance) (Limits, bool) {
				if inst.ID == "spare" {
					return tc.spare, true
				}
				return Limits{}, false
			}
			got, err := fallbackSet(t).ResolveAvailable(Selection{ProviderID: "claude"}, av)
			if err != nil {
				t.Fatalf("ResolveAvailable: %v", err)
			}
			if got.Instance.ID != tc.want {
				t.Fatalf("provider = %q, want %q", got.Instance.ID, tc.want)
			}
		})
	}
}

// TestResolveAvailableWaitsWhenNoHopCanWork: stepping over hops is not licence
// to run somewhere nobody named — when the chain ends without a usable account,
// the work stays with the provider whose allowance ran out.
func TestResolveAvailableWaitsWhenNoHopCanWork(t *testing.T) {
	set, err := NewSet("claude", []Instance{
		{ID: "claude", Kind: KindClaudeCode, FallbackProvider: "off", Enabled: true},
		{ID: "off", Kind: KindClaudeCode, Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	got, err := set.ResolveAvailable(Selection{ProviderID: "claude"}, limitedIDs(map[string]bool{"claude": true}))
	if err != nil {
		t.Fatalf("ResolveAvailable: %v", err)
	}
	if got.Substituted() {
		t.Fatalf("provider = %q, want the original", got.Instance.ID)
	}
}

// TestResolveAvailableSurvivesACycle: config.toml refuses to hold one, but a
// hand-edited file might, and a scheduler tick must not spin on it.
func TestResolveAvailableSurvivesACycle(t *testing.T) {
	set, err := NewSet("a", []Instance{
		{ID: "a", Kind: KindClaudeCode, FallbackProvider: "b", Enabled: true},
		{ID: "b", Kind: KindClaudeCode, FallbackProvider: "a", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	got, err := set.ResolveAvailable(Selection{ProviderID: "a"}, everythingLimited)
	if err != nil {
		t.Fatalf("ResolveAvailable: %v", err)
	}
	if got.Instance.ID != "a" {
		t.Fatalf("provider = %q, want the original", got.Instance.ID)
	}
}

// TestResolveAvailableStillRefusesTheUnresolvable: a rate limit is the only
// thing a fallback answers. An unknown or switched-off provider is still an
// error, not a reason to run somewhere else.
func TestResolveAvailableStillRefusesTheUnresolvable(t *testing.T) {
	set := fallbackSet(t)
	for _, id := range []string{"gone", "off"} {
		if _, err := set.ResolveAvailable(Selection{ProviderID: id}, everythingLimited); err == nil {
			t.Fatalf("provider %q: expected an error, got a substitute", id)
		}
	}
}

// TestResolveAvailableFallsBackToTheSubstitutesDefaultModel: without a model
// named for the fallback, a different harness runs its own default rather than
// a name it would not understand.
func TestResolveAvailableFallsBackToTheSubstitutesDefaultModel(t *testing.T) {
	set, err := NewSet("claude", []Instance{
		{ID: "claude", Kind: KindClaudeCode, DefaultModel: "sonnet", FallbackProvider: "codex", Enabled: true},
		{ID: "codex", Kind: KindCodex, DefaultModel: "gpt", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	got, err := set.ResolveAvailable(Selection{ProviderID: "claude", Model: "opus"},
		limitedIDs(map[string]bool{"claude": true}))
	if err != nil {
		t.Fatalf("ResolveAvailable: %v", err)
	}
	if got.Model != "gpt" {
		t.Fatalf("model = %q, want the substitute's own default", got.Model)
	}
}
