package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/limit"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// fallbackConfig is the two-account setup the rate-limit fallback exists for:
// the default instance names the second one as the provider that takes its work
// while its allowance is used up.
func fallbackConfig(fallback string, tasks ...task.Task) store.Config {
	first := claudeProvider("", "sonnet")
	first.FallbackProvider = fallback
	second := claudeProvider("", "haiku")
	second.ID, second.Name = "second", "Second account"
	return store.Config{
		Settings:  store.Settings{DefaultProvider: store.DefaultProviderID},
		Providers: []store.Provider{first, second},
		Tasks:     tasks,
	}
}

// TestALimitedProviderHandsItsTasksToItsFallback is the feature: the queue does
// not stop for the night when one account's allowance runs out, it carries on
// through the provider that account names.
func TestALimitedProviderHandsItsTasksToItsFallback(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(fallbackConfig("second", asapTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected the fallback to run the task, got %d runs", len(reqs))
	}
	if got := reqs[0].Provider.ID; got != "second" {
		t.Fatalf("ran on %q, want the fallback %q", got, "second")
	}
	// The substitute's own default model, not the blocked provider's: a task
	// that named no model gets the answer of the account that actually runs it.
	if got := reqs[0].Model; got != "haiku" {
		t.Fatalf("model = %q, want the fallback's own default", got)
	}
	// The run itself says where it went, so the operator reading it is not left
	// wondering why a Claude task ran on another account.
	log, err := os.ReadFile(reqs[0].Log.(*os.File).Name())
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if !strings.Contains(string(log), "Second account") {
		t.Fatalf("the run log should name the fallback, got %q", log)
	}
}

// TestAFallbackCarriesTheModelToTheSameHarness: a second subscription of the
// same CLI still understands the model the task asked for, so it keeps it.
func TestAFallbackCarriesTheModelToTheSameHarness(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	tk := asapTask("a", false)
	tk.Model = "opus"
	if err := st.SaveConfig(fallbackConfig("second", tk)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one run, got %d", len(reqs))
	}
	if got := reqs[0].Model; got != "opus" {
		t.Fatalf("model = %q, want the task's own", got)
	}
}

// TestWithoutAFallbackALimitedTaskStillWaits: the fallback is opt-in, and
// nothing about the old behaviour changes for a provider that has none.
func TestWithoutAFallbackALimitedTaskStillWaits(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(fallbackConfig("", asapTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	if got := r.requests(); len(got) != 0 {
		t.Fatalf("a limited provider without a fallback must start nothing, got %+v", got)
	}
}

// TestAFallbackThatIsItselfLimitedStartsNothing: substituting is only worth
// doing while the substitute can actually take the work.
func TestAFallbackThatIsItselfLimitedStartsNothing(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(fallbackConfig("second", asapTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)
	e.gates.For("second").BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	if got := r.requests(); len(got) != 0 {
		t.Fatalf("both accounts are out of allowance, so nothing may start, got %+v", got)
	}
}

// TestAFallbackStartsAFreshSession: a session id belongs to the account that
// issued it. The interrupted conversation stays with the blocked provider and
// is picked up there when its window reopens.
func TestAFallbackStartsAFreshSession(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{result: func(req executor.Request, n int) provider.Result {
		if n == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	cron := asapTask("a", false)
	cron.Trigger, cron.Cron = task.TriggerCron, "* * * * *"
	if err := st.SaveConfig(fallbackConfig("second", cron)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	// The first tick only anchors the cron task; the second runs it into the
	// limit, leaving a session waiting on the default provider; by the third its
	// gate is shut and the fallback takes the next occurrence.
	for i := range 3 {
		fc.Advance(time.Minute)
		if err := e.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		e.WaitIdle()
	}

	reqs := r.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected the fallback to pick the task up, got %d runs", len(reqs))
	}
	if reqs[1].Provider.ID != "second" {
		t.Fatalf("second run went to %q, want the fallback", reqs[1].Provider.ID)
	}
	if reqs[1].Resume {
		t.Fatal("a session from another account must never be resumed")
	}
	if reqs[1].SessionID == reqs[0].SessionID {
		t.Fatal("the fallback must start its own session")
	}
}

// TestRunNowUsesTheFallback: pressing "run now" while the account is out of
// allowance is exactly the moment the substitute is wanted.
func TestRunNowUsesTheFallback(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(fallbackConfig("second", asapTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.RunTaskNow(context.Background(), "a"); err != nil {
		t.Fatalf("RunTaskNow: %v", err)
	}
	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one run, got %d", len(reqs))
	}
	if reqs[0].Provider.ID != "second" {
		t.Fatalf("ran on %q, want the fallback", reqs[0].Provider.ID)
	}
}

// TestAFinishedFallbackDropsThePausedSession: the substitute did the work from
// the start, so the session waiting on the blocked account is retired with the
// task rather than resumed once its window reopens — the job must not run twice.
func TestAFinishedFallbackDropsThePausedSession(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{result: func(req executor.Request, n int) provider.Result {
		if n == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	cron := asapTask("a", false)
	cron.Trigger, cron.Cron = task.TriggerCron, "* * * * *"
	if err := st.SaveConfig(fallbackConfig("second", cron)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for i := range 3 {
		fc.Advance(time.Minute)
		if err := e.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		e.WaitIdle()
	}

	state, err := st.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if pending, ok := state.PendingResume("a"); ok {
		t.Fatalf("the paused session should have been dropped, still pending: %+v", pending)
	}
}

// TestAFallbackDoesNotPinFollowUpsToTheSubstitute: a job a substituted run
// queues inherits the account the task was scheduled onto, not the stand-in a
// rate limit sent that one run to.
func TestAFallbackDoesNotPinFollowUpsToTheSubstitute(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	if err := st.SaveConfig(fallbackConfig("second", asapTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one run, got %d", len(reqs))
	}
	if got := reqs[0].InheritProvider; got != store.DefaultProviderID {
		t.Fatalf("follow-ups would inherit %q, want the provider the task was scheduled onto", got)
	}
}

// TestAFallbackRunsTheModelItWasGiven: the provider handing the work over may
// name the model that should answer on the substitute, and that choice beats
// both the task's model and the substitute's default.
func TestAFallbackRunsTheModelItWasGiven(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st, ad := newTestEngineWithProvider(t, r, fc)
	ad.set(provider.Health{State: provider.HealthReady})
	tk := asapTask("a", false)
	tk.Model = "opus"
	cfg := fallbackConfig("second", tk)
	cfg.Providers[0].FallbackModel = "haiku-fast"
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one run, got %d", len(reqs))
	}
	if got := reqs[0].Model; got != "haiku-fast" {
		t.Fatalf("model = %q, want the one configured for the fallback", got)
	}
}

// TestTheChainStepsOverAProviderThatCannotRun: a hop is not the provider the
// task named, so one that cannot take the work is stepped over instead of
// ending the chain — otherwise a logged-out stand-in would stop the queue that
// the fallback behind it could have kept moving.
func TestTheChainStepsOverAProviderThatCannotRun(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ad := &perProviderHealth{healthy: provider.Health{State: provider.HealthReady}, broken: map[string]provider.Health{
		"second": {State: provider.HealthNotAuthenticated, Reason: "not logged in"},
	}}
	e := New(st, limit.NewGates(fc), r, fc, &provider.Checker{Registry: provider.NewRegistry(ad), TTL: time.Nanosecond})

	cfg := fallbackConfig("second", asapTask("a", false))
	cfg.Providers[1].FallbackProvider = "third"
	third := claudeProvider("", "")
	third.ID, third.Name = "third", "Third account"
	cfg.Providers = append(cfg.Providers, third)
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	e.gates.For(store.DefaultProviderID).BlockFor(time.Hour)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected the chain to find a working account, got %d runs", len(reqs))
	}
	if reqs[0].Provider.ID != "third" {
		t.Fatalf("ran on %q, want the first account behind the broken one", reqs[0].Provider.ID)
	}
}

// perProviderHealth answers differently per instance, which is what a chain
// with one broken account in it needs.
type perProviderHealth struct {
	healthAdapter
	healthy provider.Health
	broken  map[string]provider.Health
}

func (a *perProviderHealth) CheckHealth(_ context.Context, inst provider.Instance, _ provider.Host) provider.Health {
	if h, ok := a.broken[inst.ID]; ok {
		return h
	}
	return a.healthy
}
