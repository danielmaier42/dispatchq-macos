package engine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/limit"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// stub is a controllable Runner.
type stub struct {
	mu        sync.Mutex
	reqs      []executor.Request
	active    int
	maxActive int
	block     chan struct{} // if non-nil, Run waits on it before returning
	result    func(executor.Request, int) provider.Result
	calls     int
}

func (s *stub) Run(_ context.Context, req executor.Request) (provider.Result, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.reqs = append(s.reqs, req)
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()

	if s.block != nil {
		<-s.block
	}

	s.mu.Lock()
	s.active--
	s.mu.Unlock()

	if s.result != nil {
		return s.result(req, n), nil
	}
	return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}, nil
}

func (s *stub) requests() []executor.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]executor.Request(nil), s.reqs...)
}

// healthAdapter stands in for the Claude Code adapter so a test can say what
// the harness's readiness is without an installed CLI.
type healthAdapter struct {
	mu     sync.Mutex
	health provider.Health
	// during runs inside every probe, e.g. to cancel the context mid-check.
	during func()
}

func (a *healthAdapter) onHealth(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.during = f
}

func (a *healthAdapter) set(h provider.Health) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.health = h
}

func (a *healthAdapter) Kind() provider.Kind { return provider.KindClaudeCode }

func (a *healthAdapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{AccessModes: []provider.AccessMode{
		provider.AccessProviderDefault, provider.AccessFullAccess,
	}}
}

func (a *healthAdapter) Describe() provider.Description { return provider.Description{Name: "Claude"} }

func (a *healthAdapter) DetectBinary() string { return "" }

func (a *healthAdapter) ResolveBinary(inst provider.Instance) string { return inst.BinaryPath }

func (a *healthAdapter) CheckHealth(context.Context, provider.Instance, provider.Prober) provider.Health {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.during != nil {
		a.during()
	}
	return a.health
}

func (a *healthAdapter) ListModels(context.Context, provider.Instance, provider.Prober) []provider.Model {
	return nil
}

func (a *healthAdapter) InteractiveResumeCommand(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}

func (a *healthAdapter) Command(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}

func (a *healthAdapter) NewParser() provider.Parser { return nil }

func (a *healthAdapter) AsideCommand(provider.Instance, provider.AsideRequest) (provider.Command, error) {
	return provider.Command{}, provider.ErrUnsupported
}

func (a *healthAdapter) ParseAside([]byte) (provider.Aside, error) {
	return provider.Aside{}, provider.ErrUnsupported
}

func newTestEngine(t *testing.T, r Runner, fc clock.Clock) (*Engine, *store.Store) {
	e, st, _ := newTestEngineWithProvider(t, r, fc)
	return e, st
}

// newTestEngineWithProvider also hands back the adapter whose readiness the
// test controls. Its checker keeps no verdict at all, so a health change takes
// effect on the very next tick.
func newTestEngineWithProvider(t *testing.T, r Runner, fc clock.Clock) (*Engine, *store.Store, *healthAdapter) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ad := &healthAdapter{health: provider.Health{State: provider.HealthReady}}
	checker := &provider.Checker{Registry: provider.NewRegistry(ad), TTL: time.Nanosecond}
	e := New(st, limit.NewGates(fc), r, fc, checker)

	var runN, sessN int
	e.newRunID = func() string { runN++; return fmt.Sprintf("run-%d", runN) }
	e.newSessionID = func() string { sessN++; return fmt.Sprintf("sess-%d", sessN) }
	return e, st, ad
}

func asapTask(id string, parallel bool) task.Task {
	return task.Task{
		ID: id, Name: id, Prompt: "do " + id, WorkingDir: "/repo",
		Trigger: task.TriggerASAP, Enabled: true,
		Permissions: task.PermissionsDefault, Parallel: parallel,
	}
}

func saveTasks(t *testing.T, st *store.Store, tasks ...task.Task) {
	t.Helper()
	if err := st.SaveConfig(store.Config{Tasks: tasks}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestTickRunsAsapTaskOnce(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(reqs))
	}
	if reqs[0].Resume {
		t.Fatal("first run should not be a resume")
	}

	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Status != store.StatusSuccess {
		t.Fatalf("expected 1 successful run, got %+v", runs)
	}

	state, _ := st.LoadState()
	if !state.IsCompletedOnce("a") {
		t.Fatal("asap task should be marked completed after running")
	}

	// A second tick must not re-run a completed one-shot task.
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	e.WaitIdle()
	if got := len(r.requests()); got != 1 {
		t.Fatalf("completed task re-ran: %d total runs", got)
	}

	// A finished one-shot task leaves the queue but stays in history with a
	// replayable snapshot.
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 0 {
		t.Fatalf("finished one-shot task should be removed from the queue, still have %d", len(cfg.Tasks))
	}
	hist, _ := st.Runs()
	if len(hist) != 1 || hist[0].Task == nil || hist[0].Task.Prompt == "" {
		t.Fatalf("run should carry a replayable task snapshot: %+v", hist)
	}
}

// TestRunRecordsTheReportedModel: the model the harness says answered lands on
// the run beside the one dispatchq asked for.
func TestRunRecordsTheReportedModel(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC))
	r := &stub{result: func(req executor.Request, _ int) provider.Result {
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID, Model: "claude-opus-5-5"}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Provider.ReportedModel != "claude-opus-5-5" {
		t.Fatalf("runs = %+v, want the reported model recorded", runs)
	}
}

func TestRateLimitBlocksUntilReportedReset(t *testing.T) {
	// A five-hour session-limit hit reports an absolute reset time but no retry
	// delay. The gate must block until that reset (plus the safety buffer), not
	// just the 15-minute default backoff.
	start := time.Date(2026, 7, 17, 14, 40, 0, 0, time.UTC)
	reset := start.Add(5 * time.Hour)
	fc := clock.NewFake(start)
	r := &stub{result: func(req executor.Request, call int) provider.Result {
		if call == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, ResetAt: reset}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	e.WaitIdle()

	want := reset.Add(RateLimitResetBuffer)
	if got := e.gates.BlockedUntil(); !got.Equal(want) {
		t.Fatalf("gate blocked until %v, want reset + buffer = %v", got, want)
	}

	// Well past the default backoff but before the reset: still blocked.
	fc.Advance(time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	e.WaitIdle()
	if len(r.requests()) != 1 {
		t.Fatalf("task restarted before the reported reset: %d runs", len(r.requests()))
	}

	// Past reset + buffer: the session resumes.
	fc.Advance(4*time.Hour + RateLimitResetBuffer)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	e.WaitIdle()
	reqs := r.requests()
	if len(reqs) != 2 || !reqs[1].Resume {
		t.Fatalf("expected a resume after the reset, got %d runs (resume=%v)", len(reqs), len(reqs) == 2 && reqs[1].Resume)
	}
}

func TestRateLimitStaleResetFallsBackToBackoff(t *testing.T) {
	// A reset time already in the past (stale event, clock skew) must not open
	// the gate immediately — fall back to the default backoff.
	start := time.Date(2026, 7, 17, 14, 40, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	r := &stub{result: func(req executor.Request, _ int) provider.Result {
		return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, ResetAt: start.Add(-time.Minute)}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	if got, want := e.gates.BlockedUntil(), start.Add(DefaultRateLimitBackoff); !got.Equal(want) {
		t.Fatalf("gate blocked until %v, want default backoff %v", got, want)
	}
}

func TestCreditBlockIsNamedWhileTheProviderWaits(t *testing.T) {
	// A used-up credit balance pauses the provider for the fixed hour and is
	// named as such, so the dashboard does not read it as a limit that resets.
	start := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	r := &stub{result: func(req executor.Request, _ int) provider.Result {
		return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, RetryAfter: time.Hour, Block: provider.BlockCreditExhausted}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	if got := e.BlockReasons(); got[store.DefaultProviderID] != string(provider.BlockCreditExhausted) {
		t.Fatalf("block reasons = %v, want the credit block on the default provider", got)
	}
	if got, want := e.BlockedProviders()[store.DefaultProviderID], start.Add(time.Hour); !got.Equal(want) {
		t.Fatalf("blocked until %v, want %v", got, want)
	}
	fc.Advance(time.Hour)
	if got := e.BlockReasons(); len(got) != 0 {
		t.Fatalf("block reasons = %v after the hour, want none", got)
	}
}

func TestRateLimitThenResumeAfterReset(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	r := &stub{result: func(req executor.Request, call int) provider.Result {
		if call == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, RetryAfter: time.Hour}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	// Tick 1: hits the rate limit -> gate closes, resume pending.
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	e.WaitIdle()

	if e.gates.For(store.DefaultProviderID).Open() {
		t.Fatal("the provider's gate should be closed after a rate limit")
	}
	state, _ := st.LoadState()
	if _, ok := state.PendingResume("a"); !ok {
		t.Fatal("expected a pending resume session")
	}
	if state.IsCompletedOnce("a") {
		t.Fatal("rate-limited task must not be marked completed")
	}

	// While the gate is closed, nothing starts.
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	e.WaitIdle()
	if len(r.requests()) != 1 {
		t.Fatalf("task started again while gate closed: %d runs", len(r.requests()))
	}

	// After the reset, the task resumes its session.
	fc.Advance(time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 3: %v", err)
	}
	e.WaitIdle()

	reqs := r.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(reqs))
	}
	if !reqs[1].Resume {
		t.Fatal("second run should be a resume")
	}
	if reqs[1].SessionID != reqs[0].SessionID {
		t.Fatalf("resume used session %q, want original %q", reqs[1].SessionID, reqs[0].SessionID)
	}
	state, _ = st.LoadState()
	if !state.IsCompletedOnce("a") {
		t.Fatal("task should be completed after a successful resume")
	}
}

func TestParallelTasksRunConcurrently(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &stub{block: make(chan struct{})}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("p1", true), asapTask("p2", true))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.active == 2
	})
	close(r.block)
	e.WaitIdle()

	if r.maxActive != 2 {
		t.Fatalf("expected 2 concurrent runs, got max %d", r.maxActive)
	}
}

func TestGracefulShutdownLetsRunFinish(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &stub{block: make(chan struct{})}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- e.Loop(ctx, 10*time.Millisecond) }()

	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.active == 1 })
	cancel()                          // stop the daemon while the run is in flight
	time.Sleep(50 * time.Millisecond) // let Loop enter drain
	close(r.block)                    // the run finishes within the grace window

	select {
	case <-loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not return after shutdown")
	}
	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Status != store.StatusSuccess {
		t.Fatalf("in-flight run should finish successfully on graceful shutdown, got %+v", runs)
	}
}

// ctxStub blocks each run until its context is cancelled, then reports the
// interrupted result the real executor produces for a killed process.
type ctxStub struct {
	mu      sync.Mutex
	started int
}

func (s *ctxStub) Run(ctx context.Context, req executor.Request) (provider.Result, error) {
	s.mu.Lock()
	s.started++
	s.mu.Unlock()
	<-ctx.Done()
	return provider.Result{
		Status: store.StatusFailed, SessionID: req.SessionID, ExitCode: -1,
		Message: "run was interrupted before completing (the process was terminated — e.g. the daemon stopped)",
	}, nil
}

func (s *ctxStub) activeStarted() int { s.mu.Lock(); defer s.mu.Unlock(); return s.started }

func TestCancelRunMarksRunCanceled(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &ctxStub{}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, func() bool { return r.activeStarted() == 1 })

	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	e.WaitIdle()

	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Status != store.StatusCanceled {
		t.Fatalf("expected 1 canceled run, got %+v", runs)
	}
	if runs[0].Error != "stopped by the user" {
		t.Fatalf("unexpected error text: %q", runs[0].Error)
	}
	// A canceled one-shot task leaves the queue like any other finished run.
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 0 {
		t.Fatalf("canceled one-shot task should leave the queue, still have %d", len(cfg.Tasks))
	}
	// Cancelling again (run already finished) reports an error.
	if err := e.CancelRun("run-1"); err == nil {
		t.Fatal("CancelRun on a finished run should error")
	}
}

// TestTakeOverRunWaitsForTheRecord: taking a run over hands back the record
// only once the process is gone and the run is written down, so the caller can
// reopen the session without two processes writing to it.
func TestTakeOverRunWaitsForTheRecord(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &ctxStub{}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, func() bool { return r.activeStarted() == 1 })

	rec, err := e.TakeOverRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("TakeOverRun: %v", err)
	}
	if rec.Status != store.StatusCanceled || rec.SessionID == "" {
		t.Fatalf("returned record = %+v, want a canceled run with its session", rec)
	}
	if rec.Error != "taken over in Terminal by the user" {
		t.Fatalf("unexpected error text: %q", rec.Error)
	}
	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Status != store.StatusCanceled {
		t.Fatalf("history should already hold the canceled run, got %+v", runs)
	}
	if _, err := e.TakeOverRun(context.Background(), "run-1"); err == nil {
		t.Fatal("TakeOverRun on a finished run should error")
	}
	e.WaitIdle()
}

// TestTakeOverRunRefusesACanceledRun: a run the user already asked to stop
// is not reopened behind their back by a take-over that came second.
func TestTakeOverRunRefusesACanceledRun(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &ctxStub{}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("a", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, func() bool { return r.activeStarted() == 1 })
	e.mu.Lock()
	e.canceled["run-1"] = true // as CancelRun leaves it before the process is gone
	e.mu.Unlock()
	if _, err := e.TakeOverRun(context.Background(), "run-1"); err == nil {
		t.Fatal("TakeOverRun on a run being canceled should error")
	}
	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	e.WaitIdle()
	runs, _ := st.Runs()
	if len(runs) != 1 || runs[0].Error != "stopped by the user" {
		t.Fatalf("run should stay a plain cancel, got %+v", runs)
	}
}

func TestTakeOverRunUnknownID(t *testing.T) {
	fc := clock.NewFake(time.Now())
	e, _ := newTestEngine(t, &stub{}, fc)
	if _, err := e.TakeOverRun(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown run id")
	}
}

func TestCancelRunUnknownID(t *testing.T) {
	fc := clock.NewFake(time.Now())
	e, _ := newTestEngine(t, &stub{}, fc)
	if err := e.CancelRun("nope"); err == nil {
		t.Fatal("expected error for unknown run id")
	}
}

func TestCancelRunLeavesOtherRunsAlive(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &ctxStub{}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("p1", true), asapTask("p2", true))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitFor(t, func() bool { return r.activeStarted() == 2 })

	// Cancel only the first run; the second keeps running.
	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	waitFor(t, func() bool {
		runs, _ := st.Runs()
		for _, rn := range runs {
			if rn.RunID == "run-1" && rn.Status == store.StatusCanceled {
				return true
			}
		}
		return false
	})
	runs, _ := st.Runs()
	for _, rn := range runs {
		if rn.RunID == "run-2" && rn.Status != store.StatusRunning {
			t.Fatalf("run-2 should still be running, got %s", rn.Status)
		}
	}

	// Shut the second run down via its own cancel to end the test cleanly.
	if err := e.CancelRun("run-2"); err != nil {
		t.Fatalf("CancelRun run-2: %v", err)
	}
	e.WaitIdle()
}

func TestExclusiveTaskRunsAlone(t *testing.T) {
	fc := clock.NewFake(time.Now())
	r := &stub{block: make(chan struct{})}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, asapTask("e1", false), asapTask("e2", false))

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	waitFor(t, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.active == 1
	})

	// A second tick while the exclusive task runs must not start the other.
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	r.mu.Lock()
	active := r.active
	r.mu.Unlock()
	if active != 1 {
		t.Fatalf("exclusive task did not run alone: %d active", active)
	}

	close(r.block)
	e.WaitIdle()
	if r.maxActive != 1 {
		t.Fatalf("expected max 1 concurrent run, got %d", r.maxActive)
	}
}

// A cron task is anchored the first time the daemon sees it, which must not be
// mistaken for an execution: the dashboard shows LastRunAt as "last run".
func TestCronAnchorIsNotRecordedAsARun(t *testing.T) {
	fc := clock.NewFake(time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC))
	r := &stub{}
	e, st := newTestEngine(t, r, fc)
	cron := asapTask("nightly", false)
	cron.Trigger = task.TriggerCron
	cron.Cron = "0 20 * * *"
	saveTasks(t, st, cron)

	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()

	state, err := st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.LastStart("nightly"); !ok {
		t.Fatal("a fresh cron task should be anchored")
	}
	if at, ok := state.LastRun("nightly"); ok {
		t.Fatalf("anchoring must not count as a run, got %v", at)
	}

	if err := e.RunTaskNow(context.Background(), "nightly"); err != nil {
		t.Fatalf("RunTaskNow: %v", err)
	}
	e.WaitIdle()

	state, err = st.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	at, ok := state.LastRun("nightly")
	if !ok {
		t.Fatal("an actual run should be recorded as the last run")
	}
	if !at.Equal(fc.Now()) {
		t.Fatalf("last run = %v, want %v", at, fc.Now())
	}
}

// rateLimitedTask brings a task to the point where its run is paused on the
// rate limit with a resume pending, which is the state the cancel-resume path
// works on.
func rateLimitedTask(t *testing.T, tk task.Task, fc *clock.Fake) (*Engine, *store.Store, *stub) {
	t.Helper()
	r := &stub{result: func(req executor.Request, call int) provider.Result {
		if call == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, RetryAfter: time.Hour}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st := newTestEngine(t, r, fc)
	saveTasks(t, st, tk)
	if tk.Trigger == task.TriggerCron {
		// A fresh cron task is only anchored by the first tick; its first
		// occurrence comes after that.
		if err := e.Tick(context.Background()); err != nil {
			t.Fatalf("anchor tick: %v", err)
		}
		fc.Advance(31 * time.Minute)
	}
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	return e, st, r
}

func runByID(t *testing.T, st *store.Store, runID string) store.Run {
	t.Helper()
	runs, err := st.Runs()
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	for _, r := range runs {
		if r.RunID == runID {
			return r
		}
	}
	t.Fatalf("run %q not in history (%d runs)", runID, len(runs))
	return store.Run{}
}

func TestRateLimitedRunRecordsItsResumeTime(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	e, st, _ := rateLimitedTask(t, asapTask("a", false), fc)

	rec := runByID(t, st, "run-1")
	if rec.ResumeAt == nil {
		t.Fatal("a rate-limited run must record when it resumes")
	}
	if want := e.gates.BlockedUntil(); !rec.ResumeAt.Equal(want) {
		t.Fatalf("resume_at = %v, want the gate's reopen time %v", rec.ResumeAt, want)
	}
}

func TestCancelResumeTakesOneShotOutOfTheQueue(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	e, st, r := rateLimitedTask(t, asapTask("a", false), fc)

	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun on a waiting run: %v", err)
	}

	rec := runByID(t, st, "run-1")
	if rec.Status != store.StatusCanceled {
		t.Fatalf("status = %q, want canceled", rec.Status)
	}
	if rec.Error != "resume canceled by the user" {
		t.Fatalf("unexpected error text: %q", rec.Error)
	}
	if rec.ResumeAt != nil {
		t.Fatal("a canceled run must not still advertise a resume time")
	}
	state, _ := st.LoadState()
	if _, ok := state.PendingResume("a"); ok {
		t.Fatal("pending resume should be cleared")
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 0 {
		t.Fatalf("one-shot task should leave the queue, still have %d", len(cfg.Tasks))
	}

	// The point of the whole exercise: once the gate reopens, nothing starts.
	fc.Advance(2 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick after the reset: %v", err)
	}
	e.WaitIdle()
	if got := len(r.requests()); got != 1 {
		t.Fatalf("canceled task ran again: %d runs", got)
	}

	// Cancelling a second time is not silently accepted.
	if err := e.CancelRun("run-1"); err == nil {
		t.Fatal("cancelling an already-canceled resume should error")
	}
}

func TestCancelResumeKeepsRecurringSchedule(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	cron := task.Task{
		ID: "c", Name: "c", Prompt: "watch", WorkingDir: "/repo",
		Trigger: task.TriggerCron, Cron: "*/30 * * * *", Enabled: true,
		Permissions: task.PermissionsDefault,
	}
	e, st, r := rateLimitedTask(t, cron, fc)

	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun on a waiting run: %v", err)
	}
	state, _ := st.LoadState()
	if _, ok := state.PendingResume("c"); ok {
		t.Fatal("pending resume should be cleared")
	}
	if state.IsCompletedOnce("c") {
		t.Fatal("a recurring task must not be marked completed")
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 1 {
		t.Fatalf("recurring task should keep its place in the queue, have %d", len(cfg.Tasks))
	}

	// Its next occurrence runs, but as a fresh session — the canceled one is gone.
	fc.Advance(2 * time.Hour)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick after the reset: %v", err)
	}
	e.WaitIdle()
	reqs := r.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected the next occurrence to run, got %d runs", len(reqs))
	}
	if reqs[1].Resume {
		t.Fatal("the canceled session must not be resumed")
	}
}

func TestCancelResumeRejectsRunsThatAreNotWaiting(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	e, st, _ := rateLimitedTask(t, asapTask("a", false), fc)

	// A successful run of another task is not a resume to cancel.
	done := store.Run{RunID: "run-9", TaskID: "b", TaskName: "b", StartedAt: start, Status: store.StatusSuccess}
	if err := st.AppendRun(done); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}
	if err := e.CancelRun("run-9"); err == nil {
		t.Fatal("cancelling a finished run should error")
	}

	// Neither is a rate-limited run whose session the state no longer holds.
	if err := st.UpdateState(func(s *store.State) error { s.ClearPendingResume("a"); return nil }); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if err := e.CancelRun("run-1"); err == nil {
		t.Fatal("cancelling a resume that is no longer scheduled should error")
	}
}

func TestCancelResumeWritesTheReasonIntoTheLog(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	e, st, _ := rateLimitedTask(t, asapTask("a", false), fc)

	if err := e.CancelRun("run-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	data, err := os.ReadFile(st.LogPath("run-1"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "resume canceled by the user") {
		t.Fatalf("log does not mention the cancellation:\n%s", data)
	}
}
