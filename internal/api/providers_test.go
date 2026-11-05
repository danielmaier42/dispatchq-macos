package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// newProviderServer wires a server whose one adapter reports health.
func newProviderServer(t *testing.T, health provider.Health) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg := provider.NewRegistry(stubAdapter{health: health})
	srv := httptest.NewServer(Handler(Deps{
		Store: st, Registry: reg, Providers: provider.NewChecker(reg),
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestListProvidersOnAFreshInstall(t *testing.T) {
	srv, _ := newProviderServer(t, provider.Health{
		State: provider.HealthNotInstalled, Reason: "Claude Code was not found.",
	})
	var got []providerView
	do(t, srv, http.MethodGet, "/api/providers", nil).into(t, &got)

	if len(got) != 1 {
		t.Fatalf("got %d providers, want the seeded claude instance", len(got))
	}
	p := got[0]
	if p.ID != provider.DefaultInstanceID || !p.Default {
		t.Fatalf("provider = %+v, want the default claude instance", p)
	}
	// A fresh installation must not imply Claude Code is there just because it
	// is the default provider.
	if p.Health.State != provider.HealthNotInstalled || p.Health.Reason == "" {
		t.Fatalf("health = %+v, want the real state with a reason", p.Health)
	}
}

func TestProviderEndpointsEditAndCheck(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{State: provider.HealthReady})

	body := map[string]any{
		"name": "Claude Code", "binary_path": "/opt/claude",
		"config_dir": "/tmp/cfg", "default_model": "sonnet", "enabled": true,
	}
	var updated providerView
	r := do(t, srv, http.MethodPut, "/api/providers/claude", body)
	if r.Status != http.StatusOK {
		t.Fatalf("update = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &updated)
	if updated.BinaryPath != "/opt/claude" || updated.DefaultModel != "sonnet" || updated.ConfigDir != "/tmp/cfg" {
		t.Fatalf("provider = %+v, want the edit applied", updated.Instance)
	}
	cfg, _ := st.LoadConfig()
	if cfg.Providers[0].BinaryPath != "/opt/claude" {
		t.Fatalf("stored = %+v, want the edit persisted", cfg.Providers[0])
	}

	var checked providerView
	r = do(t, srv, http.MethodPost, "/api/providers/claude/check", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("check = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &checked)
	if !checked.Health.Ready() || checked.Health.CheckedAt.IsZero() {
		t.Fatalf("health = %+v, want a fresh ready verdict", checked.Health)
	}
}

func TestProviderEndpointsRejectBadChanges(t *testing.T) {
	srv, _ := newProviderServer(t, provider.Health{State: provider.HealthReady})

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{
			name: "a relative binary path", method: http.MethodPut, path: "/api/providers/claude",
			body: map[string]any{"binary_path": "bin/claude"}, want: http.StatusBadRequest,
		},
		{
			name: "changing the kind", method: http.MethodPut, path: "/api/providers/claude",
			body: map[string]any{"kind": "codex"}, want: http.StatusBadRequest,
		},
		{
			name: "editing an id nothing is configured under", method: http.MethodPut, path: "/api/providers/nope",
			body: map[string]any{"name": "x"}, want: http.StatusNotFound,
		},
		{
			name: "checking an unknown id", method: http.MethodPost, path: "/api/providers/nope/check",
			want: http.StatusNotFound,
		},
		{
			name: "adding a kind no adapter implements", method: http.MethodPost, path: "/api/providers",
			body: map[string]any{"id": "oc", "kind": "gremlin"}, want: http.StatusBadRequest,
		},
		{
			name: "removing the only provider", method: http.MethodDelete, path: "/api/providers/claude",
			want: http.StatusBadRequest,
		},
		{
			name: "making an unknown provider the default", method: http.MethodPost, path: "/api/providers/nope/default",
			want: http.StatusNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if r := do(t, srv, tc.method, tc.path, tc.body); r.Status != tc.want {
				t.Fatalf("status = %d (%s), want %d", r.Status, r.Body, tc.want)
			}
		})
	}
}

func TestAddAndDefaultProvider(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{State: provider.HealthReady})

	body := map[string]any{"id": "second", "kind": string(provider.KindClaudeCode), "name": "Second account"}
	if r := do(t, srv, http.MethodPost, "/api/providers", body); r.Status != http.StatusCreated {
		t.Fatalf("add = %d (%s)", r.Status, r.Body)
	}
	if r := do(t, srv, http.MethodPost, "/api/providers/second/default", nil); r.Status != http.StatusNoContent {
		t.Fatalf("default = %d (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if cfg.Settings.DefaultProvider != "second" {
		t.Fatalf("default provider = %q, want the new one", cfg.Settings.DefaultProvider)
	}

	// Saving the settings form must not take the choice back with it.
	if r := do(t, srv, http.MethodPut, "/api/settings", store.Settings{HeartbeatMinutes: 30}); r.Status != http.StatusOK {
		t.Fatalf("put settings = %d (%s)", r.Status, r.Body)
	}
	cfg, _ = st.LoadConfig()
	if cfg.Settings.DefaultProvider != "second" {
		t.Fatalf("default provider = %q, want it untouched by a settings save", cfg.Settings.DefaultProvider)
	}

	// It is the default now, so it cannot be switched off from under every task
	// that names no provider.
	if r := do(t, srv, http.MethodPost, "/api/providers/second/disable", nil); r.Status != http.StatusBadRequest {
		t.Fatalf("disable = %d (%s), want it refused while it is the default", r.Status, r.Body)
	}
	if r := do(t, srv, http.MethodPost, "/api/providers/claude/default", nil); r.Status != http.StatusNoContent {
		t.Fatalf("default = %d (%s)", r.Status, r.Body)
	}
	if r := do(t, srv, http.MethodPost, "/api/providers/second/disable", nil); r.Status != http.StatusNoContent {
		t.Fatalf("disable = %d (%s)", r.Status, r.Body)
	}
	cfg, _ = st.LoadConfig()
	if cfg.Providers[1].Enabled {
		t.Fatalf("provider = %+v, want it switched off", cfg.Providers[1])
	}
	// The card's switch goes through the edit endpoint, which holds the same line.
	if r := do(t, srv, http.MethodPut, "/api/providers/claude",
		map[string]any{"name": "Claude Code", "enabled": false}); r.Status != http.StatusBadRequest {
		t.Fatalf("edit = %d (%s), want the default provider's switch refused too", r.Status, r.Body)
	}
}

// TestQueueSaysWhyATaskIsBlocked is the queue's half of the readiness rule: the
// task keeps its place and the row says what is wrong, instead of looking like
// a job that simply never runs.
func TestQueueSaysWhyATaskIsBlocked(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{
		State: provider.HealthNotAuthenticated, Reason: "Claude Code is not logged in.",
	})
	if err := st.SaveConfig(store.Config{Tasks: []task.Task{sampleTask("a")}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	var got []taskView
	do(t, srv, http.MethodGet, "/api/tasks", nil).into(t, &got)
	if len(got) != 1 {
		t.Fatalf("got %d tasks, want the blocked one still queued", len(got))
	}
	if got[0].BlockedReason != "Claude Code is not logged in." {
		t.Fatalf("blocked reason = %q, want the readiness reason", got[0].BlockedReason)
	}
}

func TestQueueSaysNothingWhenTheProviderIsReady(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(store.Config{Tasks: []task.Task{sampleTask("a")}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	var got []taskView
	do(t, srv, http.MethodGet, "/api/tasks", nil).into(t, &got)
	if len(got) != 1 || got[0].BlockedReason != "" {
		t.Fatalf("tasks = %+v, want no blocked marker", got)
	}
}

// TestAddTaskRefusesAnUnreadyProvider keeps a job that is known in advance to
// fail from being filed at all.
func TestAddTaskRefusesAnUnreadyProvider(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{
		State: provider.HealthNotInstalled, Reason: "Claude Code was not found.",
	})
	r := do(t, srv, http.MethodPost, "/api/tasks", sampleTask("a"))
	if r.Status != http.StatusBadRequest {
		t.Fatalf("add = %d (%s), want it refused", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 0 {
		t.Fatalf("a refused task must not be stored, got %+v", cfg.Tasks)
	}
}

// TestListProviderKinds: the registry answers what can be added, so a later
// adapter appears in the app by being registered and nothing here changes.
func TestListProviderKinds(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg := provider.NewRegistry(
		stubAdapter{health: provider.Health{State: provider.HealthReady}},
		codexAdapter{},
	)
	srv := httptest.NewServer(Handler(Deps{Store: st, Registry: reg, Providers: provider.NewChecker(reg)}))
	t.Cleanup(srv.Close)

	var got []providerKind
	do(t, srv, http.MethodGet, "/api/providers/kinds", nil).into(t, &got)
	want := map[string]bool{string(provider.KindClaudeCode): true, string(provider.KindCodex): true}
	if len(got) != len(want) {
		t.Fatalf("kinds = %+v, want one per registered adapter", got)
	}
	for _, k := range got {
		if !want[k.Kind] {
			t.Fatalf("unexpected kind %q", k.Kind)
		}
	}
}

// TestProviderViewReportsWhatTheAdapterCan: the app decides what to offer and
// what to label from capabilities, never from a provider's name — which is what
// keeps a third adapter from needing changes on this side.
func TestProviderViewReportsWhatTheAdapterCan(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg := provider.NewRegistry(codexAdapter{})
	srv := httptest.NewServer(Handler(Deps{Store: st, Registry: reg, Providers: provider.NewChecker(reg)}))
	t.Cleanup(srv.Close)
	if err := st.UpdateConfig(func(cfg *store.Config) error {
		cfg.Providers[0].Kind = string(provider.KindCodex)
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	var got []providerView
	do(t, srv, http.MethodGet, "/api/providers", nil).into(t, &got)
	if len(got) != 1 {
		t.Fatalf("got %d providers, want one", len(got))
	}
	if !got[0].ReasoningEffort {
		t.Fatalf("view = %+v, want the adapter's reasoning-effort capability reported", got[0])
	}
}

// codexAdapter stands in for a second harness with capabilities of its own.
type codexAdapter struct{ stubAdapter }

func (codexAdapter) Kind() provider.Kind { return provider.KindCodex }

func (codexAdapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{ReasoningEffort: true}
}

// TestProviderSurfaceNamesTheHarness: the app says "Claude", not "claude-code",
// and shows where that CLI keeps its configuration by default. Both come from
// the adapter, so a later harness names itself.
func TestProviderSurfaceNamesTheHarness(t *testing.T) {
	srv, _ := newProviderServer(t, provider.Health{State: provider.HealthReady})

	var views []providerView
	do(t, srv, http.MethodGet, "/api/providers", nil).into(t, &views)
	if len(views) != 1 || views[0].TypeName != "Claude" {
		t.Fatalf("view = %+v, want the harness named", views)
	}
	if views[0].DefaultConfigDir != "~/.claude" {
		t.Fatalf("default config dir = %q, want the CLI's own", views[0].DefaultConfigDir)
	}

	var kinds []providerKind
	do(t, srv, http.MethodGet, "/api/providers/kinds", nil).into(t, &kinds)
	if len(kinds) != 1 || kinds[0].Name != "Claude" || kinds[0].DefaultConfigDir != "~/.claude" {
		t.Fatalf("kinds = %+v, want the harness named and its default directory", kinds)
	}
	// The stored value stays the adapter kind; only what is shown is the name.
	if kinds[0].Kind != string(provider.KindClaudeCode) {
		t.Fatalf("kind = %q, want the stored key unchanged", kinds[0].Kind)
	}
}

// TestProviderFallbackRoundTrip: the card writes the fallback with the rest of
// the provider's settings, and a fallback that names nothing is refused rather
// than stored.
func TestProviderFallbackRoundTrip(t *testing.T) {
	srv, _ := newProviderServer(t, provider.Health{State: provider.HealthReady})

	r := do(t, srv, http.MethodPost, "/api/providers", map[string]any{
		"id": "second", "kind": string(provider.KindClaudeCode), "name": "Second account",
	})
	if r.Status != http.StatusCreated {
		t.Fatalf("add = %d (%s)", r.Status, r.Body)
	}

	var updated providerView
	r = do(t, srv, http.MethodPut, "/api/providers/claude", map[string]any{
		"name": "Claude Code", "fallback_provider": "second", "enabled": true,
	})
	if r.Status != http.StatusOK {
		t.Fatalf("update = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &updated)
	if updated.FallbackProvider != "second" {
		t.Fatalf("fallback = %q, want %q", updated.FallbackProvider, "second")
	}

	r = do(t, srv, http.MethodPut, "/api/providers/claude", map[string]any{
		"name": "Claude Code", "fallback_provider": "nobody", "enabled": true,
	})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("a fallback nothing answers to must be refused, got %d (%s)", r.Status, r.Body)
	}
}

// TestFallbackLabelNamesWhoIsActuallyWorking: the banner must say the queue is
// moving only while some account in the chain really can take the work.
func TestFallbackLabelNamesWhoIsActuallyWorking(t *testing.T) {
	reg := provider.NewRegistry(stubAdapter{health: provider.Health{State: provider.HealthReady}})
	srv := &server{d: Deps{Registry: reg, Providers: provider.NewChecker(reg)}}
	set, err := provider.NewSet("claude", []provider.Instance{
		{ID: "claude", Kind: provider.KindClaudeCode, Name: "Claude", FallbackProvider: "spare", Enabled: true},
		{ID: "spare", Kind: provider.KindClaudeCode, Name: "Spare", FallbackProvider: "third", Enabled: true},
		{ID: "third", Kind: provider.KindClaudeCode, Name: "Third", Enabled: true},
		{ID: "lonely", Kind: provider.KindClaudeCode, Name: "Lonely", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	now := time.Now()
	tests := []struct {
		name    string
		id      string
		blocked map[string]time.Time
		want    string
	}{
		{name: "the first free one in the chain", id: "claude",
			blocked: map[string]time.Time{"claude": now}, want: "Spare"},
		{name: "past a fallback that is blocked too", id: "claude",
			blocked: map[string]time.Time{"claude": now, "spare": now}, want: "Third"},
		{name: "nothing when the whole chain waits", id: "claude",
			blocked: map[string]time.Time{"claude": now, "spare": now, "third": now}, want: ""},
		{name: "nothing when there is no fallback", id: "lonely",
			blocked: map[string]time.Time{"lonely": now}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, _ := set.Lookup(tc.id)
			if got := srv.fallbackLabel(context.Background(), set, inst, tc.blocked); got != tc.want {
				t.Fatalf("fallbackLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFallbackLabelSkipsAProviderThatCannotRun: naming a substitute that is
// logged out would tell the operator the queue keeps moving while nothing
// starts.
func TestFallbackLabelSkipsAProviderThatCannotRun(t *testing.T) {
	reg := provider.NewRegistry(stubAdapter{health: provider.Health{
		State: provider.HealthNotAuthenticated, Reason: "not logged in",
	}})
	srv := &server{d: Deps{Registry: reg, Providers: provider.NewChecker(reg)}}
	set, err := provider.NewSet("claude", []provider.Instance{
		{ID: "claude", Kind: provider.KindClaudeCode, Name: "Claude", FallbackProvider: "spare", Enabled: true},
		{ID: "spare", Kind: provider.KindClaudeCode, Name: "Spare", Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	inst, _ := set.Lookup("claude")
	blocked := map[string]time.Time{"claude": time.Now()}
	if got := srv.fallbackLabel(context.Background(), set, inst, blocked); got != "" {
		t.Fatalf("fallbackLabel = %q, want nothing for a fallback that cannot run", got)
	}
}

// countingStub reports readiness like stubAdapter and remembers how often it
// was asked, which is how a test sees a harness being probed twice.
type countingStub struct {
	stubAdapter
	mu     sync.Mutex
	checks int
}

func (c *countingStub) CheckHealth(ctx context.Context, inst provider.Instance, p provider.Prober) provider.Health {
	c.mu.Lock()
	c.checks++
	c.mu.Unlock()
	return c.stubAdapter.CheckHealth(ctx, inst, p)
}

func (c *countingStub) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checks
}

// The queue's blocked markers ask one question per harness, not one per way a
// task names it: a task that leaves the provider field empty and a task that
// names the default by its id mean the same CLI, and probing it twice is two
// processes for one answer — and they now run at the same time, so the cache
// cannot absorb the second one.
func TestBlockedReasonsAsksEachHarnessOnce(t *testing.T) {
	ad := &countingStub{stubAdapter: stubAdapter{health: provider.Health{
		State: provider.HealthNotAuthenticated, Reason: "not logged in",
	}}}
	reg := provider.NewRegistry(ad)
	srv := &server{d: Deps{Registry: reg, Providers: provider.NewChecker(reg)}}
	cfg := store.Config{
		Providers: []store.Provider{{ID: "claude", Kind: "claude-code", Name: "Claude", Enabled: true}},
		Tasks: []task.Task{
			{ID: "a", Name: "a", Provider: ""},       // whatever the default is
			{ID: "b", Name: "b", Provider: "claude"}, // the same one, by name
		},
	}
	cfg.Settings.DefaultProvider = "claude"

	got := srv.blockedReasons(context.Background(), cfg)
	if ad.count() != 1 {
		t.Fatalf("the harness was probed %d times, want once for both tasks", ad.count())
	}
	if got[""] != "not logged in" || got["claude"] != "not logged in" {
		t.Fatalf("blocked = %+v, want both ways of naming the provider marked", got)
	}
}

// TestProviderEnvironmentIsWriteOnly: the app can set a variable and see that
// it exists, but never reads its value back, and an edit that leaves a variable
// without a value keeps the stored one.
func TestProviderEnvironmentIsWriteOnly(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{State: provider.HealthReady})

	put := func(env []map[string]any) resp {
		return do(t, srv, http.MethodPut, "/api/providers/claude", map[string]any{
			"name": "Claude Code", "enabled": true, "env": env,
		})
	}
	r := put([]map[string]any{{"name": "ANTHROPIC_API_KEY", "value": "sk-ant-secret"}, {"name": "ANTHROPIC_BASE_URL", "value": "https://gw.example"}})
	if r.Status != http.StatusOK {
		t.Fatalf("update = %d (%s)", r.Status, r.Body)
	}
	if strings.Contains(string(r.Body), "sk-ant-secret") || strings.Contains(string(r.Body), "gw.example") {
		t.Fatalf("response %s carries a value", r.Body)
	}
	var v providerView
	r.into(t, &v)
	if want := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}; !reflect.DeepEqual(v.Env, want) {
		t.Fatalf("env names = %v, want %v", v.Env, want)
	}
	list := do(t, srv, http.MethodGet, "/api/providers", nil)
	if strings.Contains(string(list.Body), "sk-ant-secret") {
		t.Fatalf("list %s carries a value", list.Body)
	}

	// Keep the key (no value), replace the URL, drop nothing.
	r = put([]map[string]any{{"name": "ANTHROPIC_API_KEY"}, {"name": "ANTHROPIC_BASE_URL", "value": "https://other.example"}})
	if r.Status != http.StatusOK {
		t.Fatalf("keeping update = %d (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	want := map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret", "ANTHROPIC_BASE_URL": "https://other.example"}
	if !reflect.DeepEqual(cfg.Providers[0].Env, want) {
		t.Fatalf("stored env = %v, want %v", cfg.Providers[0].Env, want)
	}

	// An update that does not mention env leaves it alone.
	if r := do(t, srv, http.MethodPut, "/api/providers/claude", map[string]any{"name": "Renamed", "enabled": true}); r.Status != http.StatusOK {
		t.Fatalf("plain update = %d (%s)", r.Status, r.Body)
	}
	if cfg, _ = st.LoadConfig(); !reflect.DeepEqual(cfg.Providers[0].Env, want) {
		t.Fatalf("env after a plain update = %v, want it untouched", cfg.Providers[0].Env)
	}

	// An empty list removes every variable; a name left out is removed.
	if r := put([]map[string]any{}); r.Status != http.StatusOK {
		t.Fatalf("clearing update = %d (%s)", r.Status, r.Body)
	}
	if cfg, _ = st.LoadConfig(); len(cfg.Providers[0].Env) != 0 {
		t.Fatalf("env = %v, want it cleared", cfg.Providers[0].Env)
	}
}

func TestProviderEnvironmentIsValidated(t *testing.T) {
	srv, _ := newProviderServer(t, provider.Health{State: provider.HealthReady})
	for name, env := range map[string][]map[string]any{
		"bad name":         {{"name": "NOT-VALID", "value": "x"}},
		"owned by a field": {{"name": "CLAUDE_CONFIG_DIR", "value": "/x"}},
		"no value yet":     {{"name": "ANTHROPIC_API_KEY"}},
		"duplicate":        {{"name": "A", "value": "1"}, {"name": "A", "value": "2"}},
	} {
		r := do(t, srv, http.MethodPut, "/api/providers/claude", map[string]any{"name": "C", "enabled": true, "env": env})
		if r.Status != http.StatusBadRequest {
			t.Errorf("%s: status = %d (%s), want 400", name, r.Status, r.Body)
		}
	}
}

func TestAddProviderWithEnvironment(t *testing.T) {
	srv, st := newProviderServer(t, provider.Health{State: provider.HealthReady})
	r := do(t, srv, http.MethodPost, "/api/providers", map[string]any{
		"id": "api", "kind": "claude-code", "name": "API",
		"env": []map[string]any{{"name": "ANTHROPIC_API_KEY", "value": "sk-ant-x"}},
	})
	if r.Status != http.StatusCreated {
		t.Fatalf("add = %d (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if got := cfg.Providers[len(cfg.Providers)-1].Env["ANTHROPIC_API_KEY"]; got != "sk-ant-x" {
		t.Fatalf("stored key = %q", got)
	}
}
