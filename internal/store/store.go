// Package store persists dispatchq configuration, run history, and read-state
// on disk in human-readable formats (PLAN.md D5/D10):
//
//   - config.toml    global settings + the ordered task list (TOML)
//   - history.jsonl  append-only run event log (JSON Lines)
//   - state.json     read-status and scheduling bookkeeping (JSON)
//   - runs/<id>.log  per-run output log
//
// The data directory is ~/Library/Application Support/dispatchq by default and
// can be overridden with the DISPATCHQ_HOME environment variable. Data written
// while the app was called ClaudeQ is moved there on first use (see rename.go).
package store

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/danielmaier42/claudeq/internal/task"
)

// EnvHome is the environment variable that overrides the data directory.
const EnvHome = "DISPATCHQ_HOME"

const (
	configFile  = "config.toml"
	historyFile = "history.jsonl"
	stateFile   = "state.json"
	runsDir     = "runs"
)

// Store provides serialized access to the on-disk data directory.
type Store struct {
	home    string
	mu      sync.Mutex // guards individual file reads/writes
	writeMu sync.Mutex // serializes read-modify-write updates
}

// DefaultHome resolves the data directory: the [HomeOverride] if set, otherwise
// ~/Library/Application Support/dispatchq. A data directory left behind by
// ClaudeQ is moved there first, so the first DispatchQ start finds every task.
func DefaultHome() (string, error) {
	if h := HomeOverride(); h != "" {
		return h, nil
	}
	base, err := os.UserConfigDir() // ~/Library/Application Support on macOS
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	home := filepath.Join(base, homeDirName)
	if err := migrateLegacyHome(filepath.Join(base, legacyHomeDirName), home); err != nil {
		return "", err
	}
	return home, nil
}

// HomeOverride is the data directory the environment asks for: $DISPATCHQ_HOME,
// or $CLAUDEQ_HOME from a shell profile written before the rename. Empty means
// the default location.
func HomeOverride() string {
	if h := os.Getenv(EnvHome); h != "" {
		return h
	}
	return os.Getenv(LegacyEnvHome)
}

// Open opens (creating if necessary) the data directory at home.
func Open(home string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(home, runsDir), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return &Store{home: home}, nil
}

// Home returns the data directory path.
func (s *Store) Home() string { return s.home }

// LogPath returns the log file path for a run id.
func (s *Store) LogPath(runID string) string {
	return filepath.Join(s.home, runsDir, runID+".log")
}

func (s *Store) path(name string) string { return filepath.Join(s.home, name) }

// LoadConfig reads config.toml. A missing file yields a default, empty Config.
func (s *Store) LoadConfig() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, _, err := s.readConfig()
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// readConfig parses config.toml and applies the migrations, reporting whether
// they changed anything. The caller holds the appropriate lock.
func (s *Store) readConfig() (Config, bool, error) {
	data, err := os.ReadFile(s.path(configFile))
	if errors.Is(err, os.ErrNotExist) {
		// A fresh installation still has the Claude Code provider — its Settings
		// card reports what is actually wrong (not installed, not logged in)
		// rather than the app pretending no harness exists. Nothing is written:
		// this is the default a first save will persist.
		fresh := Config{}
		fresh.migrate()
		return fresh, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse config: %w", err)
	}
	return cfg, cfg.migrate(), nil
}

// migrate rewrites configs written by older versions and reports whether it
// changed anything. Every load applies it in memory, so an old config behaves
// correctly even before it is rewritten; MigrateConfig writes the result back.
// It must stay idempotent.
func (c *Config) migrate() bool {
	changed := false

	// The global skip-permissions default is gone: a task that relied on it
	// keeps its authority by carrying the setting itself.
	if c.Settings.LegacySkipPermissions {
		for i := range c.Tasks {
			if c.Tasks[i].Permissions == task.PermissionsDefault {
				c.Tasks[i].Permissions = task.PermissionsSkip
			}
		}
		c.Settings.LegacySkipPermissions = false
		changed = true
	}

	// Runs go through a configured provider instance now. A configuration
	// written before that has none, so the Claude Code binary and the global
	// default model become the `claude` instance every existing task then runs
	// on — same CLI, same model, same schedule.
	if len(c.Providers) == 0 {
		c.Providers = seedProviders(c.Settings)
		changed = true
	}
	if c.Settings.DefaultProvider == "" {
		c.Settings.DefaultProvider = c.Providers[0].ID
		changed = true
	}
	// Every feature that sat behind the beta switch is public now, so the
	// switch (and the name it had before) means nothing any more.
	if c.Settings.LegacyBetaFeatures != nil || c.Settings.LegacyShowCodexBeta != nil {
		c.Settings.LegacyBetaFeatures, c.Settings.LegacyShowCodexBeta = nil, nil
		changed = true
	}
	// The instances own these two values now; leaving copies behind would give
	// the file two answers to the same question.
	if c.Settings.LegacyClaudePath != "" || c.Settings.LegacyDefaultModel != "" {
		c.Settings.LegacyClaudePath, c.Settings.LegacyDefaultModel = "", ""
		changed = true
	}
	// The feedback assistant became the assistant, and its provider moved with
	// it; a value set under the new name wins, since it can only have been
	// chosen later. The model does not move: one picked for drafting an issue
	// (the old Settings advised a small one) would quietly take the chat off
	// its own, larger default, and the feedback page's default is that small
	// model anyway.
	if c.Settings.LegacyFeedbackProvider != "" || c.Settings.LegacyFeedbackModel != "" {
		if c.Settings.AssistantProvider == "" {
			c.Settings.AssistantProvider = c.Settings.LegacyFeedbackProvider
		}
		c.Settings.LegacyFeedbackProvider, c.Settings.LegacyFeedbackModel = "", ""
		changed = true
	}
	return changed
}

// MigrateConfig persists the migrations LoadConfig applies in memory, so the
// file on disk says what the daemon actually does. It reports whether the file
// was rewritten and is a no-op for an already-current config.
func (s *Store) MigrateConfig() (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	changed := false
	err := s.withWriteLock(func() error {
		cfg, migrated, err := s.readConfig()
		if err != nil || !migrated {
			return err
		}
		changed = true
		return s.SaveConfig(cfg)
	})
	return changed, err
}

// SaveConfig atomically writes config.toml after validating every task.
func (s *Store) SaveConfig(cfg Config) error {
	for i, t := range cfg.Tasks {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("task %d (%q): %w", i, t.ID, err)
		}
	}
	if err := cfg.checkUniqueIDs(); err != nil {
		return err
	}
	if err := cfg.checkProviders(); err != nil {
		return err
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.path(configFile), data)
}

// UpdateConfig atomically applies fn to the config: it serializes with other
// updates, loads the current config, applies fn, and saves the result. This
// prevents lost updates when several callers (e.g. concurrent API requests)
// modify tasks at once.
func (s *Store) UpdateConfig(fn func(*Config) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteLock(func() error {
		cfg, err := s.LoadConfig()
		if err != nil {
			return err
		}
		if err := fn(&cfg); err != nil {
			return err
		}
		return s.SaveConfig(cfg)
	})
}

// UpdateState atomically applies fn to the state, serialized with other
// updates. fn should mutate only the fields it owns so concurrent writers do
// not clobber each other's keys (e.g. the daemon must not overwrite read-status
// set via the API).
func (s *Store) UpdateState(fn func(*State) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.withWriteLock(func() error {
		st, err := s.LoadState()
		if err != nil {
			return err
		}
		if err := fn(st); err != nil {
			return err
		}
		return s.SaveState(st)
	})
}

// AppendRun appends a run event to history.jsonl. Later events for the same
// run id supersede earlier ones (see Runs).
func (s *Store) AppendRun(r Run) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode run: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path(historyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write history: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close history: %w", err)
	}
	return nil
}

// AppendRunLog appends a line to a run's log file (used to record the final
// status/error into the log so it shows in both raw and chat views).
func (s *Store) AppendRunLog(runID string, data []byte) error {
	f, err := os.OpenFile(s.LogPath(runID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open run log: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write run log: %w", err)
	}
	return f.Close()
}

// Runs returns the run history collapsed so the latest event per run id wins,
// preserving first-seen order.
func (s *Store) Runs() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path(historyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	defer func() { _ = f.Close() }()

	latest := map[string]Run{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Run
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("parse history line: %w", err)
		}
		if _, seen := latest[r.RunID]; !seen {
			order = append(order, r.RunID)
		}
		latest[r.RunID] = r
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}

	out := make([]Run, 0, len(order))
	for _, id := range order {
		out = append(out, latest[id])
	}
	return out, nil
}

// ReconcileRunningRuns marks any run still recorded as "running" as failed. It
// is meant to run at startup: a fresh process means nothing is actually running,
// so such entries are leftovers from a hard stop (crash, power loss, SIGKILL)
// that never got to record an outcome. Returns how many it reconciled.
func (s *Store) ReconcileRunningRuns(now time.Time) (int, error) {
	runs, err := s.Runs()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		if r.Status != StatusRunning {
			continue
		}
		r.Status = StatusFailed
		fin := now
		r.FinishedAt = &fin
		if r.Error == "" {
			r.Error = "interrupted before completing (daemon stopped unexpectedly)"
		}
		if err := s.AppendRun(r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// BackfillLastRuns fills in State.LastRunAt from run history for tasks that
// have no entry yet. It exists for installs that predate the field: without it
// a recurring task would show no last execution until it next runs. Tasks
// already recorded are left untouched. Returns how many entries it added.
func (s *Store) BackfillLastRuns() (int, error) {
	runs, err := s.Runs()
	if err != nil {
		return 0, err
	}
	latest := map[string]time.Time{}
	for _, r := range runs {
		if r.TaskID == "" {
			continue
		}
		if prev, ok := latest[r.TaskID]; !ok || r.StartedAt.After(prev) {
			latest[r.TaskID] = r.StartedAt
		}
	}
	n := 0
	if err := s.UpdateState(func(cur *State) error {
		n = 0
		for id, at := range latest {
			if _, ok := cur.LastRun(id); ok {
				continue
			}
			cur.RecordRun(id, at)
			n++
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return n, nil
}

// PruneHistory keeps only the most recent `limit` runs: it compacts
// history.jsonl to one latest event per kept run and deletes the log files of
// the dropped runs. limit <= 0 keeps everything.
func (s *Store) PruneHistory(limit int) error {
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path(historyFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	latest := map[string]Run{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Run
		if err := json.Unmarshal(line, &r); err != nil {
			_ = f.Close()
			return fmt.Errorf("parse history line: %w", err)
		}
		if _, seen := latest[r.RunID]; !seen {
			order = append(order, r.RunID)
		}
		latest[r.RunID] = r
	}
	scErr := sc.Err()
	_ = f.Close()
	if scErr != nil {
		return fmt.Errorf("read history: %w", scErr)
	}

	if len(order) <= limit {
		return nil
	}
	drop := order[:len(order)-limit]
	keep := order[len(order)-limit:]

	var buf bytes.Buffer
	for _, id := range keep {
		b, err := json.Marshal(latest[id])
		if err != nil {
			return fmt.Errorf("marshal run: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := writeAtomic(s.path(historyFile), buf.Bytes()); err != nil {
		return fmt.Errorf("rewrite history: %w", err)
	}
	for _, id := range drop {
		p := latest[id].LogPath
		if p == "" {
			p = s.LogPath(id)
		}
		_ = os.Remove(p)
	}
	return nil
}

// LoadState reads state.json. A missing file yields a ready-to-use zero State.
func (s *Store) LoadState() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path(stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	st := newState()
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	st.ensureMaps()
	return st, nil
}

// SaveState atomically writes state.json.
func (s *Store) SaveState(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(s.path(stateFile), data)
}

// writeAtomic writes data to a temp file in the same directory and renames it
// into place, so a crash never leaves a half-written file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// Config is the persisted configuration: global settings, the configured
// provider instances, plus the ordered task list. List order defines priority —
// index 0 is highest (PLAN.md FA-11).
type Config struct {
	Settings Settings `toml:"settings"`
	// Providers are the configured provider instances, in the order they were
	// added. The domain type built from them lives in internal/provider; this is
	// only their on-disk shape.
	Providers []Provider `toml:"providers,omitempty"`
	// Pools are the configured provider pools: sets of providers a task can be
	// given as a whole, with each run going to the member that suits it best.
	Pools []Pool      `toml:"pools,omitempty"`
	Tasks []task.Task `toml:"tasks"`
}

// Pool is one provider pool as config.toml holds it.
type Pool struct {
	// ID is the stable identifier a task selects ("claude-pool").
	ID string `toml:"id" json:"id"`
	// Name is the label shown in the app and in run messages.
	Name string `toml:"name" json:"name"`
	// Members are the providers the pool spreads its tasks over, in order.
	Members []PoolMember `toml:"members" json:"members"`
}

// PoolMember is one provider of a pool.
type PoolMember struct {
	// Provider is the member's provider id.
	Provider string `toml:"provider" json:"provider"`
	// Weight is how large the member's allowance is compared with the others'
	// (a Max 20x plan is 20, a Pro plan 1). Zero takes what the provider
	// reports about its plan, or 1 when it reports nothing.
	Weight float64 `toml:"weight,omitempty" json:"weight,omitempty"`
}

// Identity of the provider instance every dispatchq configuration has: the Claude
// Code CLI, seeded on migration and on a fresh installation (see seedProviders).
const (
	// DefaultProviderID is the stable id of that instance — what a task names.
	DefaultProviderID = "claude"
	// DefaultProviderKind is the adapter it runs on.
	DefaultProviderKind = "claude-code"
	// DefaultProviderName is its display name.
	DefaultProviderName = "Claude Code"
)

// Provider is one configured provider instance as config.toml holds it: a
// stable id, the adapter kind that runs it, and the instance's own settings.
// Two subscriptions of the same harness are two entries of the same kind with
// separate configuration directories, so nothing here assumes one instance per
// kind.
//
// The store keeps the file shape only. What an instance can do, and how it is
// resolved, validated and probed, lives in internal/provider.
type Provider struct {
	// ID is the stable identifier a task selects ("claude").
	ID string `toml:"id" json:"id"`
	// Kind names the adapter implementation that runs this instance.
	Kind string `toml:"kind" json:"kind"`
	// Name is the human-readable label shown in the app and in run messages.
	Name string `toml:"name" json:"name"`
	// BinaryPath is an absolute path to the harness CLI. Empty lets the adapter
	// detect it.
	BinaryPath string `toml:"binary_path" json:"binary_path"`
	// ConfigDir selects the harness's configuration (and therefore account)
	// directory. Empty uses the CLI's own default.
	ConfigDir string `toml:"config_dir" json:"config_dir"`
	// Env is the environment the harness is started with, on top of the
	// daemon's own: an ANTHROPIC_API_KEY here runs the instance on API billing,
	// say. It holds secrets, so it never travels as JSON; only the provider's
	// settings dialog and CLI write it, and neither reads the values back.
	Env map[string]string `toml:"env,omitempty" json:"-"`
	// DefaultModel is used when neither the task nor the caller names a model.
	DefaultModel string `toml:"default_model" json:"default_model"`
	// FallbackProvider is the id of the instance that takes over this one's
	// tasks while its allowance is used up. Empty means the tasks wait instead.
	FallbackProvider string `toml:"fallback_provider,omitempty" json:"fallback_provider"`
	// FallbackModel is the model those substituted runs use. Empty lets dispatchq
	// decide (see provider.ResolveAvailable) and is meaningless without a
	// FallbackProvider.
	FallbackModel string `toml:"fallback_model,omitempty" json:"fallback_model"`
	// Enabled turns the instance off without removing it.
	Enabled bool `toml:"enabled" json:"enabled"`
	// Execution names the environment the harness runs in. Absent means the
	// daemon's own operating system, which is what every configuration written
	// before execution environments existed means; nothing migrates.
	Execution *ProviderExecution `toml:"execution,omitempty" json:"execution,omitempty"`
}

// ProviderExecution is the [providers.execution] table: where a provider
// instance runs when that is not the daemon's own operating system.
type ProviderExecution struct {
	// Type is the environment kind. "wsl" runs the instance inside a Linux
	// distribution of the Windows Subsystem for Linux.
	Type string `toml:"type" json:"type"`
	// Distro is the WSL distribution name, as `wsl.exe --list` reports it.
	Distro string `toml:"distro,omitempty" json:"distro,omitempty"`
	// User is an existing Linux user of that distribution. Empty runs as the
	// distribution's default user.
	User string `toml:"user,omitempty" json:"user,omitempty"`
}

// seedProviders builds the provider list for a configuration that has none: the
// Claude Code instance, carrying over the pre-provider global settings so a
// migrated installation invokes exactly the same CLI with exactly the same
// model as before.
func seedProviders(s Settings) []Provider {
	return []Provider{{
		ID:           DefaultProviderID,
		Kind:         DefaultProviderKind,
		Name:         DefaultProviderName,
		BinaryPath:   s.LegacyClaudePath,
		DefaultModel: s.LegacyDefaultModel,
		Enabled:      true,
	}}
}

// checkProviders guards what the file format itself has to guarantee: every
// instance is addressable by a unique id, and the default names one of them.
// Whether a kind exists and a path is usable is the provider layer's business
// (internal/provider), which the store must not depend on.
func (c Config) checkProviders() error {
	seen := map[string]struct{}{}
	for _, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider with empty id")
		}
		if _, dup := seen[p.ID]; dup {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = struct{}{}
	}
	if id := c.Settings.DefaultProvider; id != "" {
		if _, ok := seen[id]; !ok {
			return fmt.Errorf("default provider %q is not configured", id)
		}
	}
	return c.checkFallbacks()
}

// checkFallbacks guards the rate-limit fallback chains: every fallback names
// another configured instance, and following them always ends. A cycle would
// be a queue that hands the same work round and round while every account in
// it is blocked, so the file is not allowed to hold one.
func (c Config) checkFallbacks() error {
	next := make(map[string]string, len(c.Providers))
	for _, p := range c.Providers {
		next[p.ID] = p.FallbackProvider
	}
	for _, p := range c.Providers {
		if p.FallbackProvider == "" {
			continue
		}
		if p.FallbackProvider == p.ID {
			return fmt.Errorf("provider %q cannot be its own fallback", p.ID)
		}
		if _, ok := next[p.FallbackProvider]; !ok {
			return fmt.Errorf("fallback %q of provider %q is not configured", p.FallbackProvider, p.ID)
		}
		seen := map[string]struct{}{p.ID: {}}
		for id := next[p.ID]; id != ""; id = next[id] {
			if _, loop := seen[id]; loop {
				return fmt.Errorf("provider %q has a fallback cycle", p.ID)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func (c Config) checkUniqueIDs() error {
	seen := map[string]struct{}{}
	for _, t := range c.Tasks {
		if _, dup := seen[t.ID]; dup {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		seen[t.ID] = struct{}{}
	}
	return nil
}

// Settings holds global configuration.
type Settings struct {
	// DefaultProvider is the id of the provider instance a task runs on when it
	// names none. Empty falls back to the first configured instance.
	DefaultProvider string `toml:"default_provider" json:"default_provider"`
	// LegacyDefaultModel is the retired global default model. Providers carry
	// their own default model now; this is only read to migrate old configs
	// (see migrate) and never written back or exposed over the API.
	LegacyDefaultModel string `toml:"default_model,omitempty" json:"-"`
	// LegacySkipPermissions is the removed global "may do anything" default.
	// It is only read to migrate old configs (see migrate) and never written
	// back or exposed over the API; permissions live on the task now.
	LegacySkipPermissions bool `toml:"skip_permissions_default,omitempty" json:"-"`
	// HeartbeatMinutes is the safety-net wake interval in minutes (PLAN.md D8).
	// Zero means use the default (see HeartbeatOrDefault).
	HeartbeatMinutes int `toml:"heartbeat_minutes" json:"heartbeat_minutes"`
	// Pushover holds mobile-notification credentials (FA-41). Used from Phase 4.
	Pushover Pushover `toml:"pushover" json:"pushover"`
	// Ntfy holds the ntfy push channel: a topic on ntfy.sh or a self-hosted
	// instance.
	Ntfy Ntfy `toml:"ntfy" json:"ntfy"`
	// Webhook holds the generic JSON webhook channel — the one that covers a
	// service dispatchq does not know about (Slack, Discord, Home Assistant, n8n).
	Webhook Webhook `toml:"webhook" json:"webhook"`
	// LegacyClaudePath is the retired global path to the Claude Code binary. The
	// `claude` provider instance carries it now; this is only read to migrate
	// old configs (see migrate) and never written back or exposed over the API.
	LegacyClaudePath string `toml:"claude_path,omitempty" json:"-"`
	// IdleTimeoutMinutes kills a run that produces no output for this many
	// minutes — a hung/deadlocked process. A working run keeps streaming events,
	// so it is not affected. 0 = use the default; negative = never kill.
	IdleTimeoutMinutes int `toml:"idle_timeout_minutes" json:"idle_timeout_minutes"`
	// MaxRunHistory caps how many runs are kept; older runs (and their log files)
	// are pruned. 0 = use the default; negative = keep everything.
	MaxRunHistory int `toml:"max_run_history" json:"max_run_history"`
	// SystemPrompt is the operator's custom system prompt, appended to dispatchq's
	// built-in one on every run (built-in first, this last). Empty means none.
	SystemPrompt string `toml:"system_prompt" json:"system_prompt"`
	// BackfillUrgency is the provider urgency above which backfill tasks run
	// (see task.Task.Backfill). Zero means the default (see
	// BackfillUrgencyOrDefault).
	BackfillUrgency float64 `toml:"backfill_urgency,omitempty" json:"backfill_urgency"`
	// Paused is the global stop switch: while it is true no run starts at all —
	// neither a due task nor a manual "run now" — and the machine is no longer
	// woken for scheduled work. Runs already in flight are left alone.
	Paused bool `toml:"paused" json:"paused"`
	// PromptReviewDisabled turns off the prompt review that checks a task's
	// prompt against this machine before it is queued. The zero value keeps the
	// review on, so an existing config gains the feature without being edited.
	PromptReviewDisabled bool `toml:"prompt_review_disabled" json:"prompt_review_disabled"`
	// ArtifactNotifyDisabled silences the "new artifact" notification on every
	// channel. The artifact is still published and listed; it just arrives
	// without a ping. The zero value keeps the notification on, so an existing
	// config behaves as before.
	ArtifactNotifyDisabled bool `toml:"artifact_notify_disabled,omitempty" json:"artifact_notify_disabled"`
	// PromptReviewProvider is the provider instance that answers the review.
	// Empty means the default provider. It is chosen separately from the one
	// tasks run on: reviewing a prompt is cheap, frequent work that has no
	// reason to consume the allowance the real jobs need.
	PromptReviewProvider string `toml:"prompt_review_provider,omitempty" json:"prompt_review_provider"`
	// PromptReviewModel is the model used for that review. Empty means "the same
	// model as everything else", i.e. the reviewing provider's default model.
	PromptReviewModel string `toml:"prompt_review_model,omitempty" json:"prompt_review_model"`
	// AssistantProvider is the provider instance the assistant answers on: the
	// chat that operates dispatchq, and the feedback page that drafts a GitHub
	// issue. Empty means the default provider.
	AssistantProvider string `toml:"assistant_provider,omitempty" json:"assistant_provider"`
	// AssistantModel is the model it answers with. Empty means dispatchq's own
	// choice per job (see assistant.DefaultModel and feedback.DefaultModel), so
	// neither silently lands on an expensive model picked for real tasks.
	AssistantModel string `toml:"assistant_model,omitempty" json:"assistant_model"`
	// LegacyFeedbackProvider and LegacyFeedbackModel are what the two settings
	// above were called while only the feedback page used them. Read only to
	// migrate old configs (see migrate); never written back or exposed.
	LegacyFeedbackProvider string `toml:"feedback_provider,omitempty" json:"-"`
	LegacyFeedbackModel    string `toml:"feedback_model,omitempty" json:"-"`
	// LegacyBetaFeatures is the retired switch that revealed the features
	// dispatchq did not consider finished; they are all public now. A pointer, so
	// the key is noticed whatever it says, and dropped from old configs (see
	// migrate); never written back or exposed.
	LegacyBetaFeatures *bool `toml:"beta_features,omitempty" json:"-"`
	// LegacyShowCodexBeta is what that switch was called when it governed only
	// Codex. Read only to drop it from old configs; never written back.
	LegacyShowCodexBeta *bool `toml:"show_codex_beta,omitempty" json:"-"`
	// DefaultWorkingDir prefills a new task's working directory. Empty means no
	// prefill — the field starts blank, as before this setting existed.
	DefaultWorkingDir string `toml:"default_working_dir,omitempty" json:"default_working_dir"`
	// Appearance pins the app window and the dashboard to AppearanceLight or
	// AppearanceDark; empty follows macOS. Presentation state only: the daemon
	// and the CLI never look at it.
	Appearance string `toml:"appearance,omitempty" json:"appearance"`
	// Accent pins the app's accent colour as "#rrggbb"; empty follows the macOS
	// accent. Presentation state only, like Appearance.
	Accent string `toml:"accent,omitempty" json:"accent"`
}

// The appearances Settings.Appearance can pin. Empty means "follow macOS" and
// is what an untouched config holds, so the key only appears once chosen.
const (
	AppearanceLight = "light"
	AppearanceDark  = "dark"
)

// ValidAppearance reports whether v is a value Settings.Appearance may hold.
func ValidAppearance(v string) bool {
	switch v {
	case "", AppearanceLight, AppearanceDark:
		return true
	}
	return false
}

// ValidAccent reports whether v is a value Settings.Accent may hold: empty, or
// a colour written as "#" and six hex digits.
func ValidAccent(v string) bool {
	if v == "" {
		return true
	}
	if len(v) != 7 || v[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(v[1:])
	return err == nil
}

// ErrPaused is what a refused run carries while Settings.Paused is on. A pause
// a manual "run now" could step around would not be a pause, so such a request
// is refused with this instead of quietly starting a run.
var ErrPaused = errors.New("all runs are paused (Settings → Pause all runs)")

// DefaultHeartbeatMinutes is the wake safety-net interval when unset.
const DefaultHeartbeatMinutes = 60

// HeartbeatOrDefault returns the configured heartbeat, or the default if unset.
func (s Settings) HeartbeatOrDefault() time.Duration {
	m := s.HeartbeatMinutes
	if m <= 0 {
		m = DefaultHeartbeatMinutes
	}
	return time.Duration(m) * time.Minute
}

// DefaultBackfillUrgency is the backfill threshold when unset: a provider
// that would need half again its even pace to use up its week.
const DefaultBackfillUrgency = 1.5

// Backfill thresholds the settings accept: below the lower one a fresh week
// would already count as at risk, above the upper one nothing ever is.
const (
	MinBackfillUrgency = 0.5
	MaxBackfillUrgency = 5.0
)

// BackfillUrgencyOrDefault returns the configured backfill threshold, or the
// default if unset.
func (s Settings) BackfillUrgencyOrDefault() float64 {
	if s.BackfillUrgency <= 0 {
		return DefaultBackfillUrgency
	}
	return s.BackfillUrgency
}

// ErrInvalidBackfillUrgency is returned for a threshold outside the range.
var ErrInvalidBackfillUrgency = fmt.Errorf("backfill urgency must be between %.1f and %.1f", MinBackfillUrgency, MaxBackfillUrgency)

// CheckBackfillUrgency reports whether v is a threshold the settings accept;
// zero (the default) always is.
func CheckBackfillUrgency(v float64) error {
	if v == 0 || v >= MinBackfillUrgency && v <= MaxBackfillUrgency {
		return nil
	}
	return ErrInvalidBackfillUrgency
}

// DefaultIdleTimeoutMinutes is the no-output kill threshold when unset.
const DefaultIdleTimeoutMinutes = 30

// IdleTimeout returns the no-output duration after which a run is killed, or 0
// when disabled. Unset (0) uses the default; a negative setting disables it.
func (s Settings) IdleTimeout() time.Duration {
	m := s.IdleTimeoutMinutes
	if m == 0 {
		m = DefaultIdleTimeoutMinutes
	}
	if m < 0 {
		return 0
	}
	return time.Duration(m) * time.Minute
}

// DefaultMaxRunHistory is the retained-run count when unset.
const DefaultMaxRunHistory = 500

// RunHistoryLimit returns how many runs to keep, or 0 for unlimited. Unset (0)
// uses the default; a negative setting keeps everything.
func (s Settings) RunHistoryLimit() int {
	n := s.MaxRunHistory
	if n == 0 {
		n = DefaultMaxRunHistory
	}
	if n < 0 {
		return 0
	}
	return n
}

// Pushover holds Pushover API credentials and whether the channel is enabled.
type Pushover struct {
	Enabled bool   `toml:"enabled" json:"enabled"`
	Token   string `toml:"token" json:"token"`
	UserKey string `toml:"user_key" json:"user_key"`
}

// Ntfy holds the ntfy channel's configuration and whether it is enabled. Server
// empty means the public ntfy.sh; Token is only needed for a protected topic.
type Ntfy struct {
	Enabled bool   `toml:"enabled" json:"enabled"`
	Server  string `toml:"server" json:"server"`
	Topic   string `toml:"topic" json:"topic"`
	Token   string `toml:"token" json:"token"`
}

// Webhook holds the generic webhook channel's configuration and whether it is
// enabled. Template empty means dispatchq's own JSON body (see
// notify.DefaultWebhookTemplate).
type Webhook struct {
	Enabled  bool   `toml:"enabled" json:"enabled"`
	URL      string `toml:"url" json:"url"`
	Template string `toml:"template" json:"template"`
}
