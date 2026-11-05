package store

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/task"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func sampleTask(id string) task.Task {
	return task.Task{
		ID:          id,
		Name:        id,
		Prompt:      "do " + id,
		WorkingDir:  "/repo/" + id,
		Trigger:     task.TriggerASAP,
		Enabled:     true,
		Permissions: task.PermissionsDefault,
	}
}

func TestLoadConfigMissingReturnsEmpty(t *testing.T) {
	s := openTemp(t)
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Tasks) != 0 {
		t.Fatalf("expected no tasks, got %d", len(cfg.Tasks))
	}
}

func TestSaveLoadConfigRoundTripPreservesOrder(t *testing.T) {
	s := openTemp(t)
	in := Config{
		Settings: Settings{
			DefaultProvider: DefaultProviderID,
			Paused:          true,
			Pushover:        Pushover{Token: "tok", UserKey: "usr"},
			Ntfy:            Ntfy{Enabled: true, Server: "https://ntfy.example.com", Topic: "dispatchq", Token: "ntk"},
			Webhook:         Webhook{Enabled: true, URL: "https://hooks.example.com/x", Template: `{"text":"{{title}}: {{message}}"}`},
		},
		Providers: seedProviders(Settings{}),
		Tasks:     []task.Task{sampleTask("a"), sampleTask("b"), sampleTask("c")},
	}
	if err := s.SaveConfig(in); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	out, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if out.Settings != in.Settings {
		t.Fatalf("settings mismatch: %+v vs %+v", out.Settings, in.Settings)
	}
	if len(out.Tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(out.Tasks))
	}
	for i, want := range []string{"a", "b", "c"} {
		if out.Tasks[i].ID != want {
			t.Fatalf("task %d id = %q, want %q (order not preserved)", i, out.Tasks[i].ID, want)
		}
	}
}

func TestSaveConfigRejectsInvalidTask(t *testing.T) {
	s := openTemp(t)
	bad := sampleTask("x")
	bad.Prompt = ""
	if err := s.SaveConfig(Config{Tasks: []task.Task{bad}}); err == nil {
		t.Fatal("expected error saving invalid task")
	}
}

func TestSaveConfigRejectsDuplicateIDs(t *testing.T) {
	s := openTemp(t)
	err := s.SaveConfig(Config{Tasks: []task.Task{sampleTask("dup"), sampleTask("dup")}})
	if err == nil {
		t.Fatal("expected error on duplicate ids")
	}
}

func TestRunHistoryCollapsesToLatest(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fin := start.Add(time.Minute)

	if err := s.AppendRun(Run{RunID: "r1", TaskID: "a", StartedAt: start, Status: StatusRunning}); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}
	if err := s.AppendRun(Run{RunID: "r2", TaskID: "b", StartedAt: start, Status: StatusRunning}); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}
	if err := s.AppendRun(Run{RunID: "r1", TaskID: "a", StartedAt: start, FinishedAt: &fin, Status: StatusSuccess}); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}

	runs, err := s.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 collapsed runs, got %d", len(runs))
	}
	// First-seen order preserved: r1 then r2.
	if runs[0].RunID != "r1" || runs[1].RunID != "r2" {
		t.Fatalf("order = %q,%q; want r1,r2", runs[0].RunID, runs[1].RunID)
	}
	if runs[0].Status != StatusSuccess {
		t.Fatalf("r1 status = %q, want latest %q", runs[0].Status, StatusSuccess)
	}
}

func TestStateReadStatusPersists(t *testing.T) {
	s := openTemp(t)
	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.IsRead("r1") {
		t.Fatal("run should start unread")
	}
	st.MarkRead("r1")
	st.RecordStart("taskA", time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	st.MarkCompletedOnce("taskA")
	if err := s.SaveState(st); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	reloaded, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState (reload): %v", err)
	}
	if !reloaded.IsRead("r1") {
		t.Fatal("read status did not persist")
	}
	if !reloaded.IsCompletedOnce("taskA") {
		t.Fatal("completed-once did not persist")
	}
	if _, ok := reloaded.LastStart("taskA"); !ok {
		t.Fatal("last-start did not persist")
	}
}

func TestUpdateConfigConcurrentAddsNoLoss(t *testing.T) {
	s := openTemp(t)
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = s.UpdateConfig(func(cfg *Config) error {
				cfg.Tasks = append(cfg.Tasks, sampleTask(fmt.Sprintf("t%02d", i)))
				return nil
			})
		}(i)
	}
	wg.Wait()

	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Tasks) != n {
		t.Fatalf("lost updates under concurrency: got %d tasks, want %d", len(cfg.Tasks), n)
	}
}

func TestUpdateConfigCrossProcessNoLoss(t *testing.T) {
	// Two separate Store instances on the same home stand in for the daemon and a
	// `dispatchq queue` child updating config.toml at the same time. Each opens its
	// own lock fd, so only the cross-process flock (not the in-process writeMu)
	// keeps their read-modify-write updates from clobbering each other.
	home := t.TempDir()
	sA, err := Open(home)
	if err != nil {
		t.Fatalf("Open A: %v", err)
	}
	sB, err := Open(home)
	if err != nil {
		t.Fatalf("Open B: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(2)
		add := func(s *Store, id string) {
			defer wg.Done()
			_ = s.UpdateConfig(func(cfg *Config) error {
				cfg.Tasks = append(cfg.Tasks, sampleTask(id))
				return nil
			})
		}
		go add(sA, fmt.Sprintf("a%02d", i))
		go add(sB, fmt.Sprintf("b%02d", i))
	}
	wg.Wait()

	cfg, err := sA.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Tasks) != 2*n {
		t.Fatalf("lost updates across processes: got %d tasks, want %d", len(cfg.Tasks), 2*n)
	}
}

func TestUpdateStatePreservesOtherKeys(t *testing.T) {
	s := openTemp(t)
	// One writer sets read-status (as the API would)...
	if err := s.UpdateState(func(st *State) error { st.MarkRead("r1"); return nil }); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	// ...another touches only scheduling keys (as the engine would).
	if err := s.UpdateState(func(st *State) error { st.RecordStart("taskA", time.Now()); return nil }); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	st, _ := s.LoadState()
	if !st.IsRead("r1") {
		t.Fatal("read-status was clobbered by an unrelated state update")
	}
	if _, ok := st.LastStart("taskA"); !ok {
		t.Fatal("last-start not persisted")
	}
}

func TestDefaultHomeHonoursEnv(t *testing.T) {
	t.Setenv(EnvHome, "/tmp/dispatchq-test-home")
	home, err := DefaultHome()
	if err != nil {
		t.Fatalf("DefaultHome: %v", err)
	}
	if home != "/tmp/dispatchq-test-home" {
		t.Fatalf("DefaultHome = %q, want override", home)
	}
}

func TestReconcileRunningRuns(t *testing.T) {
	s := openTemp(t)
	now := time.Now()
	if err := s.AppendRun(Run{RunID: "r1", TaskID: "a", Status: StatusRunning, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRun(Run{RunID: "r2", TaskID: "b", Status: StatusSuccess, StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReconcileRunningRuns(now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reconciled %d, want 1", n)
	}
	runs, _ := s.Runs()
	got := map[string]Run{}
	for _, r := range runs {
		got[r.RunID] = r
	}
	if got["r1"].Status != StatusFailed || got["r1"].FinishedAt == nil {
		t.Fatalf("r1 = %+v, want failed with finish time", got["r1"])
	}
	if got["r2"].Status != StatusSuccess {
		t.Fatalf("r2 status = %q, want untouched success", got["r2"].Status)
	}
}

func TestPruneHistoryKeepsRecentAndDeletesLogs(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("r%d", i)
		if err := s.AppendRun(Run{RunID: id, TaskID: "a", Status: StatusSuccess, LogPath: s.LogPath(id)}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.LogPath(id), []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneHistory(2); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.Runs()
	if len(runs) != 2 || runs[0].RunID != "r3" || runs[1].RunID != "r4" {
		t.Fatalf("kept %v, want [r3 r4]", runs)
	}
	if _, err := os.Stat(s.LogPath("r0")); !os.IsNotExist(err) {
		t.Fatal("dropped run's log should be deleted")
	}
	if _, err := os.Stat(s.LogPath("r4")); err != nil {
		t.Fatal("kept run's log should remain")
	}
}

func TestIdleTimeoutAndHistoryDefaults(t *testing.T) {
	var s Settings
	if s.IdleTimeout() != 30*time.Minute {
		t.Fatalf("default idle = %v, want 30m", s.IdleTimeout())
	}
	s.IdleTimeoutMinutes = -1
	if s.IdleTimeout() != 0 {
		t.Fatal("negative idle should disable")
	}
	s.IdleTimeoutMinutes = 45
	if s.IdleTimeout() != 45*time.Minute {
		t.Fatalf("explicit idle = %v, want 45m", s.IdleTimeout())
	}

	var h Settings
	if h.RunHistoryLimit() != 500 {
		t.Fatalf("default history = %d, want 500", h.RunHistoryLimit())
	}
	h.MaxRunHistory = -1
	if h.RunHistoryLimit() != 0 {
		t.Fatal("negative history should mean unlimited (0)")
	}
	h.MaxRunHistory = 100
	if h.RunHistoryLimit() != 100 {
		t.Fatalf("explicit history = %d, want 100", h.RunHistoryLimit())
	}
}

func TestBackfillLastRunsFromHistory(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	old := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	for _, r := range []Run{
		{RunID: "r1", TaskID: "a", StartedAt: recent, Status: StatusSuccess},
		{RunID: "r2", TaskID: "a", StartedAt: old, Status: StatusSuccess},
		{RunID: "r3", TaskID: "b", StartedAt: old, Status: StatusFailed},
	} {
		if err := s.AppendRun(r); err != nil {
			t.Fatalf("AppendRun: %v", err)
		}
	}
	// An already recorded task keeps its entry, history or not.
	known := time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC)
	if err := s.UpdateState(func(cur *State) error { cur.RecordRun("b", known); return nil }); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	n, err := s.BackfillLastRuns()
	if err != nil {
		t.Fatalf("BackfillLastRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("backfilled %d, want 1", n)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if at, _ := st.LastRun("a"); !at.Equal(recent) {
		t.Fatalf("last run of a = %v, want the most recent %v", at, recent)
	}
	if at, _ := st.LastRun("b"); !at.Equal(known) {
		t.Fatalf("last run of b = %v, want the untouched %v", at, known)
	}
}

// TestLoadConfigMigratesGlobalSkipPermissions covers the removal of the global
// skip-permissions default: a task that relied on it must keep its authority,
// and the retired key must not survive the next save.
func TestLoadConfigMigratesGlobalSkipPermissions(t *testing.T) {
	s := openTemp(t)
	raw := `[settings]
skip_permissions_default = true

[[tasks]]
id = 'a'
name = 'a'
prompt = 'do a'
working_dir = '/repo/a'
trigger = 'asap'
enabled = true
permissions = 'default'

[[tasks]]
id = 'b'
name = 'b'
prompt = 'do b'
working_dir = '/repo/b'
trigger = 'asap'
enabled = true
permissions = 'skip'
`
	if err := os.WriteFile(filepath.Join(s.Home(), configFile), []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	for _, tk := range cfg.Tasks {
		if tk.Permissions != task.PermissionsSkip {
			t.Fatalf("task %q permissions = %q, want skip", tk.ID, tk.Permissions)
		}
	}
	if cfg.Settings.LegacySkipPermissions {
		t.Fatal("legacy setting still set after migration")
	}

	migrated, err := s.MigrateConfig()
	if err != nil {
		t.Fatalf("MigrateConfig: %v", err)
	}
	if !migrated {
		t.Fatal("MigrateConfig reported no change for a legacy config")
	}
	data, err := os.ReadFile(filepath.Join(s.Home(), configFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "skip_permissions_default") {
		t.Fatalf("retired key written back:\n%s", data)
	}
	if !strings.Contains(string(data), "permissions = 'skip'") {
		t.Fatalf("migrated permissions not persisted:\n%s", data)
	}

	// Idempotent: a current config is left alone.
	if migrated, err := s.MigrateConfig(); err != nil || migrated {
		t.Fatalf("second MigrateConfig = (%t, %v), want (false, nil)", migrated, err)
	}
}

// TestMigrateConfigNoFile guards the fresh-install path: nothing to migrate,
// and no config.toml conjured up.
func TestMigrateConfigNoFile(t *testing.T) {
	s := openTemp(t)
	if migrated, err := s.MigrateConfig(); err != nil || migrated {
		t.Fatalf("MigrateConfig = (%t, %v), want (false, nil)", migrated, err)
	}
	if _, err := os.Stat(filepath.Join(s.Home(), configFile)); !os.IsNotExist(err) {
		t.Fatalf("config.toml created by MigrateConfig (stat err = %v)", err)
	}
}

func TestPromptReviewIsOnForAnExistingConfig(t *testing.T) {
	// The setting is stored as "disabled" so a config.toml written before the
	// feature existed gains it switched on rather than silently off.
	st := openTemp(t)
	if err := st.SaveConfig(Config{}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings.PromptReviewDisabled {
		t.Error("a config that never mentioned the review must come back with it enabled")
	}
}

func TestPromptReviewSettingsRoundTrip(t *testing.T) {
	st := openTemp(t)
	want := Settings{DefaultProvider: DefaultProviderID, PromptReviewDisabled: true, PromptReviewModel: "haiku"}
	if err := st.SaveConfig(Config{Settings: want, Providers: seedProviders(Settings{})}); err != nil {
		t.Fatalf("save: %v", err)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Settings != want {
		t.Errorf("got %+v, want %+v", cfg.Settings, want)
	}
}

// TestMigrateSeedsTheClaudeProvider is the compatibility case for the provider
// configuration: a config.toml written before providers existed gains the
// `claude` instance carrying exactly the CLI path and model it used to run
// with, and the retired global keys are gone from the file afterwards.
func TestMigrateSeedsTheClaudeProvider(t *testing.T) {
	s := openTemp(t)
	raw := `[settings]
default_model = 'opus'
claude_path = '/opt/claude'
heartbeat_minutes = 30

[[tasks]]
id = 'a'
name = 'a'
prompt = 'do a'
working_dir = '/repo'
trigger = 'asap'
enabled = true
permissions = 'default'
`
	if err := os.WriteFile(filepath.Join(s.Home(), configFile), []byte(raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := Provider{
		ID: DefaultProviderID, Kind: DefaultProviderKind, Name: DefaultProviderName,
		BinaryPath: "/opt/claude", DefaultModel: "opus", Enabled: true,
	}
	if len(cfg.Providers) != 1 || !reflect.DeepEqual(cfg.Providers[0], want) {
		t.Fatalf("providers = %+v, want exactly %+v", cfg.Providers, want)
	}
	if cfg.Settings.DefaultProvider != DefaultProviderID {
		t.Fatalf("default provider = %q, want %q", cfg.Settings.DefaultProvider, DefaultProviderID)
	}
	if cfg.Settings.LegacyClaudePath != "" || cfg.Settings.LegacyDefaultModel != "" {
		t.Fatal("the retired global model/path are still set after migration")
	}
	if cfg.Settings.HeartbeatMinutes != 30 {
		t.Fatalf("unrelated settings changed: heartbeat = %d", cfg.Settings.HeartbeatMinutes)
	}
	if len(cfg.Tasks) != 1 || cfg.Tasks[0].Provider != "" {
		t.Fatalf("tasks = %+v, want the existing task untouched on the default provider", cfg.Tasks)
	}

	migrated, err := s.MigrateConfig()
	if err != nil {
		t.Fatalf("MigrateConfig: %v", err)
	}
	if !migrated {
		t.Fatal("MigrateConfig reported no change for a pre-provider config")
	}
	data, err := os.ReadFile(filepath.Join(s.Home(), configFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "claude_path") {
		t.Fatalf("retired claude_path written back:\n%s", data)
	}
	settingsSection, _, found := strings.Cut(string(data), "[[providers]]")
	if !found {
		t.Fatalf("the seeded provider was not persisted:\n%s", data)
	}
	if strings.Contains(settingsSection, "default_model") {
		t.Fatalf("retired global default_model written back:\n%s", data)
	}

	// Idempotent: running it again changes nothing.
	if migrated, err := s.MigrateConfig(); err != nil || migrated {
		t.Fatalf("second MigrateConfig = (%t, %v), want (false, nil)", migrated, err)
	}
}

// TestLoadConfigOnAFreshInstallHasTheClaudeProvider covers the other seeding
// path: with no config.toml at all the app still knows about Claude Code, so
// its Settings card can report what is actually wrong with it.
func TestLoadConfigOnAFreshInstallHasTheClaudeProvider(t *testing.T) {
	s := openTemp(t)
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != DefaultProviderID {
		t.Fatalf("providers = %+v, want the seeded claude instance", cfg.Providers)
	}
	if !cfg.Providers[0].Enabled || cfg.Providers[0].BinaryPath != "" {
		t.Fatalf("seeded instance = %+v, want it enabled with nothing configured", cfg.Providers[0])
	}
	if _, err := os.Stat(filepath.Join(s.Home(), configFile)); !os.IsNotExist(err) {
		t.Fatalf("config.toml conjured up by a read (stat err = %v)", err)
	}
}

// TestMigrateKeepsAConfiguredProviderList checks that migration never rewrites
// providers an operator has already configured.
func TestMigrateKeepsAConfiguredProviderList(t *testing.T) {
	cfg := Config{
		Settings:  Settings{DefaultProvider: "second"},
		Providers: []Provider{{ID: "first", Kind: DefaultProviderKind}, {ID: "second", Kind: DefaultProviderKind}},
	}
	if cfg.migrate() {
		t.Fatal("a current configuration must not be rewritten")
	}
	if len(cfg.Providers) != 2 || cfg.Settings.DefaultProvider != "second" {
		t.Fatalf("configuration changed: %+v", cfg)
	}
}

func TestSaveConfigRejectsBrokenProviderLists(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "empty id", cfg: Config{Providers: []Provider{{Kind: DefaultProviderKind}}}},
		{
			name: "duplicate id",
			cfg:  Config{Providers: []Provider{{ID: "claude"}, {ID: "claude"}}},
		},
		{
			name: "default names nothing",
			cfg:  Config{Settings: Settings{DefaultProvider: "gone"}, Providers: []Provider{{ID: "claude"}}},
		},
		{
			name: "fallback names nothing",
			cfg:  Config{Providers: []Provider{{ID: "claude", FallbackProvider: "gone"}}},
		},
		{
			name: "fallback points at itself",
			cfg:  Config{Providers: []Provider{{ID: "claude", FallbackProvider: "claude"}}},
		},
		{
			name: "fallback cycle",
			cfg: Config{Providers: []Provider{
				{ID: "a", FallbackProvider: "b"},
				{ID: "b", FallbackProvider: "c"},
				{ID: "c", FallbackProvider: "a"},
			}},
		},
	}
	s := openTemp(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.SaveConfig(tc.cfg); err == nil {
				t.Fatal("expected SaveConfig to refuse")
			}
		})
	}
}

// TestSaveConfigKeepsAValidFallbackChain: a chain that ends is exactly what
// the feature is, so the check must not refuse one.
func TestSaveConfigKeepsAValidFallbackChain(t *testing.T) {
	s := openTemp(t)
	cfg := Config{Providers: []Provider{
		{ID: "a", Kind: DefaultProviderKind, FallbackProvider: "b", Enabled: true},
		{ID: "b", Kind: DefaultProviderKind, FallbackProvider: "c", Enabled: true},
		{ID: "c", Kind: DefaultProviderKind, Enabled: true},
	}}
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Providers[0].FallbackProvider != "b" {
		t.Fatalf("fallback = %q, want it to survive the round trip", got.Providers[0].FallbackProvider)
	}
}

func TestNotifiedProviderStateSurvivesAReload(t *testing.T) {
	s := openTemp(t)
	if err := s.UpdateState(func(st *State) error {
		st.SetNotifiedProviderState("claude", "not_installed")
		return nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got := st.NotifiedProviderState("claude"); got != "not_installed" {
		t.Fatalf("state = %q, want the recorded one", got)
	}
	st.ForgetProvider("claude")
	if got := st.NotifiedProviderState("claude"); got != "" {
		t.Fatalf("state after ForgetProvider = %q, want none", got)
	}
}

// TestMigrateDropsTheBetaSwitch: every feature that sat behind the switch is
// public now, so both names it ever had leave the file.
func TestMigrateDropsTheBetaSwitch(t *testing.T) {
	for _, raw := range []string{
		"[settings]\nshow_codex_beta = true\n",
		"[settings]\nbeta_features = true\n",
		"[settings]\nbeta_features = false\n",
	} {
		s := openTemp(t)
		if err := os.WriteFile(filepath.Join(s.Home(), configFile), []byte(raw), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if migrated, err := s.MigrateConfig(); err != nil || !migrated {
			t.Fatalf("MigrateConfig(%q) = (%t, %v), want the file rewritten", raw, migrated, err)
		}
		data, err := os.ReadFile(filepath.Join(s.Home(), configFile))
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if strings.Contains(string(data), "beta") {
			t.Fatalf("retired switch written back:\n%s", data)
		}
		if migrated, err := s.MigrateConfig(); err != nil || migrated {
			t.Fatalf("second MigrateConfig = (%t, %v), want (false, nil)", migrated, err)
		}
	}
}

func TestMigrateMovesTheFeedbackSettingsToTheAssistant(t *testing.T) {
	for _, tc := range []struct {
		raw         string
		prov, model string
	}{
		// The model stays behind: it was picked for drafting issues, not the chat.
		{"[settings]\nfeedback_provider = \"cheap\"\nfeedback_model = \"haiku\"\n", "cheap", ""},
		{"[settings]\nfeedback_model = \"haiku\"\n", "", ""},
		// A value under the new name was chosen later and wins.
		{"[settings]\nassistant_provider = \"new\"\nfeedback_provider = \"old\"\n", "new", ""},
	} {
		s := openTemp(t)
		if err := os.WriteFile(filepath.Join(s.Home(), configFile), []byte(tc.raw), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		if migrated, err := s.MigrateConfig(); err != nil || !migrated {
			t.Fatalf("MigrateConfig(%q) = (%t, %v), want the file rewritten", tc.raw, migrated, err)
		}
		cfg, err := s.LoadConfig()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.Settings.AssistantProvider != tc.prov || cfg.Settings.AssistantModel != tc.model {
			t.Fatalf("%q: assistant = (%q, %q), want (%q, %q)", tc.raw,
				cfg.Settings.AssistantProvider, cfg.Settings.AssistantModel, tc.prov, tc.model)
		}
		data, err := os.ReadFile(filepath.Join(s.Home(), configFile))
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if strings.Contains(string(data), "feedback_") {
			t.Fatalf("old keys written back:\n%s", data)
		}
		if migrated, err := s.MigrateConfig(); err != nil || migrated {
			t.Fatalf("second MigrateConfig = (%t, %v), want (false, nil)", migrated, err)
		}
	}
}

func TestValidAppearance(t *testing.T) {
	for _, v := range []string{"", AppearanceLight, AppearanceDark} {
		if !ValidAppearance(v) {
			t.Errorf("ValidAppearance(%q) = false", v)
		}
	}
	for _, v := range []string{"system", "Dark", "sepia"} {
		if ValidAppearance(v) {
			t.Errorf("ValidAppearance(%q) = true", v)
		}
	}
}

func TestValidAccent(t *testing.T) {
	for _, v := range []string{"", "#0a84ff", "#FF9F0A"} {
		if !ValidAccent(v) {
			t.Errorf("ValidAccent(%q) = false", v)
		}
	}
	for _, v := range []string{"blue", "0a84ff", "#0a84f", "#0a84ffaa", "#0a84fg"} {
		if ValidAccent(v) {
			t.Errorf("ValidAccent(%q) = true", v)
		}
	}
}

// The key is written only once a scheme is pinned: an untouched config keeps
// following macOS without saying so.
func TestAppearanceIsWrittenOnlyWhenPinned(t *testing.T) {
	s := openTemp(t)
	if err := s.UpdateConfig(func(cfg *Config) error { cfg.Settings.HeartbeatMinutes = 30; return nil }); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(s.Home(), configFile))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(data), "appearance") {
		t.Fatalf("appearance written while following macOS:\n%s", data)
	}
	if err := s.UpdateConfig(func(cfg *Config) error { cfg.Settings.Appearance = AppearanceDark; return nil }); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if data, _ = os.ReadFile(filepath.Join(s.Home(), configFile)); !strings.Contains(string(data), `appearance = 'dark'`) {
		t.Fatalf("pinned appearance not written:\n%s", data)
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Settings.Appearance != AppearanceDark {
		t.Fatalf("appearance = %q after reload, want dark", cfg.Settings.Appearance)
	}
}

func TestBackfillUrgency(t *testing.T) {
	if got := (Settings{}).BackfillUrgencyOrDefault(); got != DefaultBackfillUrgency {
		t.Errorf("unset = %v, want the default", got)
	}
	if got := (Settings{BackfillUrgency: 3}).BackfillUrgencyOrDefault(); got != 3 {
		t.Errorf("set = %v, want 3", got)
	}
	for v, ok := range map[float64]bool{0: true, 0.5: true, 5: true, 0.4: false, 5.1: false, -1: false} {
		if err := CheckBackfillUrgency(v); (err == nil) != ok {
			t.Errorf("CheckBackfillUrgency(%v) = %v, want ok=%v", v, err, ok)
		}
	}
}
