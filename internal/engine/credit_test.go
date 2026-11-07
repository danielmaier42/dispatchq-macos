package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// creditRefused answers every run on the named account the way Claude Code
// answers an API key without credit, and every other run with a success.
func creditRefused(id string) func(executor.Request, int) provider.Result {
	return func(req executor.Request, _ int) provider.Result {
		if req.Provider.ID == id {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID,
				Block: provider.BlockCreditExhausted, Message: "credit balance is too low"}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}
}

// creditAccounts are Claude accounts with the given fallbacks (id → fallback).
func creditAccounts(ids []string, fallback map[string]string) []store.Provider {
	out := make([]store.Provider, 0, len(ids))
	for _, id := range ids {
		p := claudeProvider("", "")
		p.ID, p.Name, p.FallbackProvider = id, id, fallback[id]
		out = append(out, p)
	}
	return out
}

// tickAndWait runs one scheduler pass, lets its runs finish and moves the
// clock on by one daemon tick.
func tickAndWait(t *testing.T, e *Engine, fc *clock.Fake) {
	t.Helper()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	fc.Advance(5 * time.Second)
}

// TestACreditRefusalMovesThePoolTaskOnAtOnce is the bug: an API key whose
// credit is used up refuses the run, and the task must go to the next pool
// member that can work on the following tick, not wait an hour behind the
// refused account. It holds however the run was started, including by hand on
// a task re-queued under the id of one that had already run once, which is what
// kept the task waiting in the field.
func TestACreditRefusalMovesThePoolTaskOnAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		byHand bool
	}{
		{"started by the scheduler", false},
		{"started by hand after an earlier run of the same id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 5, 22, 8, 0, 0, time.UTC)
			fc := clock.NewFake(now)
			r := &stub{result: creditRefused("api0")}
			e, st, _ := newTestEngineWithProvider(t, r, fc)
			// api0 ranks first, team has used up its week, api1 can work.
			e.SetLimits(readings(map[string]provider.Limits{
				"api0": weekReading(now, 10, 2*time.Hour),
				"team": weekReading(now, 100, 3*24*time.Hour),
				"api1": weekReading(now, 50, 6*24*time.Hour),
			}))
			tk := asapTask("a", true)
			tk.Pool = "claude-pool"
			cfg := store.Config{
				Settings:  store.Settings{DefaultProvider: "api0"},
				Providers: creditAccounts([]string{"api0", "team", "api1"}, nil),
				Pools: []store.Pool{{ID: "claude-pool", Name: "Claude", Members: []store.PoolMember{
					{Provider: "api0"}, {Provider: "team"}, {Provider: "api1"},
				}}},
				Tasks: []task.Task{tk},
			}
			if err := st.SaveConfig(cfg); err != nil {
				t.Fatalf("SaveConfig: %v", err)
			}
			if tc.byHand {
				if err := st.UpdateState(func(s *store.State) error {
					s.MarkCompletedOnce("a")
					return nil
				}); err != nil {
					t.Fatalf("UpdateState: %v", err)
				}
				if err := e.RunTaskNow(context.Background(), "a"); err != nil {
					t.Fatalf("RunTaskNow: %v", err)
				}
				fc.Advance(5 * time.Second)
			} else {
				tickAndWait(t, e, fc)
			}
			tickAndWait(t, e, fc)

			reqs := r.requests()
			if len(reqs) != 2 {
				t.Fatalf("runs = %d, want the refused one and one on the next member", len(reqs))
			}
			if reqs[0].Provider.ID != "api0" {
				t.Fatalf("first run on %q, want the best-ranked api0", reqs[0].Provider.ID)
			}
			if got := reqs[1].Provider.ID; got != "api1" {
				t.Fatalf("second run on %q, want api1: api0 has no credit and team has used up its week", got)
			}
			if reqs[1].Resume {
				t.Fatalf("the run on api1 resumed api0's session; a session belongs to its account")
			}
			// The work is done: the task leaves the queue and nothing waits for
			// api0's credit any more.
			after, err := st.LoadState()
			if err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			if _, ok := after.PendingResume("a"); ok {
				t.Fatalf("a session is still waiting after the task finished elsewhere")
			}
			tickAndWait(t, e, fc)
			if n := len(r.requests()); n != 2 {
				t.Fatalf("runs = %d after the task finished, want no third", n)
			}
		})
	}
}

// TestACreditRefusalWalksTheFallbackChain: a task that names a provider goes
// down its fallback chain on the next tick, stepping over a hop that is waiting
// out its own limit and one whose reading says its week is used up, to the first
// account that can work.
func TestACreditRefusalWalksTheFallbackChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		// hold puts the middle hop out of the running.
		hold func(e *Engine, now time.Time) map[string]provider.Limits
	}{
		{"hop waiting out its limit", func(e *Engine, _ time.Time) map[string]provider.Limits {
			e.gates.For("c1").BlockFor(3 * time.Hour)
			return nil
		}},
		{"hop read as used up", func(_ *Engine, now time.Time) map[string]provider.Limits {
			return map[string]provider.Limits{"c1": weekReading(now, 100, 3*24*time.Hour)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 11, 5, 22, 8, 0, 0, time.UTC)
			fc := clock.NewFake(now)
			r := &stub{result: creditRefused("api0")}
			e, st, _ := newTestEngineWithProvider(t, r, fc)
			e.SetLimits(readings(tc.hold(e, now)))
			tk := asapTask("a", true)
			tk.Provider = "api0"
			cfg := store.Config{
				Settings: store.Settings{DefaultProvider: "api0"},
				Providers: creditAccounts([]string{"api0", "c1", "api1", "teamapi"},
					map[string]string{"api0": "c1", "c1": "api1", "api1": "teamapi"}),
				Tasks: []task.Task{tk},
			}
			if err := st.SaveConfig(cfg); err != nil {
				t.Fatalf("SaveConfig: %v", err)
			}

			tickAndWait(t, e, fc)
			tickAndWait(t, e, fc)

			reqs := r.requests()
			if len(reqs) != 2 {
				t.Fatalf("runs = %d, want the refused one and one on the fallback", len(reqs))
			}
			if reqs[0].Provider.ID != "api0" || reqs[1].Provider.ID != "api1" {
				t.Fatalf("ran on %q then %q, want api0 then api1", reqs[0].Provider.ID, reqs[1].Provider.ID)
			}
		})
	}
}

// TestRunsReportTheirCreditAndAnAccountBlockHoldsUntilLifted: a refused run
// and a successful one tell the credit observer what they proved, and an
// account block stays closed past the refused run's fixed hour until a ping
// lifts it.
func TestRunsReportTheirCreditAndAnAccountBlockHoldsUntilLifted(t *testing.T) {
	start := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	refuse := true
	r := &stub{result: func(req executor.Request, _ int) provider.Result {
		if refuse {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, RetryAfter: time.Hour, Block: provider.BlockCreditExhausted}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st := newTestEngine(t, r, fc)
	type seen struct {
		id       string
		block    provider.Block
		answered bool
		at       time.Time
	}
	var mu sync.Mutex
	var got []seen
	e.SetCreditSeen(func(id string, b provider.Block, answered bool, at time.Time) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, seen{id, b, answered, at})
	})
	saveTasks(t, st, asapTask("a", false))
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	mu.Lock()
	if len(got) != 1 || got[0] != (seen{store.DefaultProviderID, provider.BlockCreditExhausted, false, start}) {
		t.Fatalf("credit seen = %+v, want one refusal", got)
	}
	mu.Unlock()

	e.BlockAccount(store.DefaultProviderID, provider.BlockCreditExhausted)
	fc.Advance(2 * time.Hour)
	if e.gateOpen(store.DefaultProviderID) {
		t.Fatal("the account block reopened on its own after the run's hour")
	}
	e.LiftAccountBlock(store.DefaultProviderID)
	if !e.gateOpen(store.DefaultProviderID) {
		t.Fatal("LiftAccountBlock did not reopen the gate")
	}

	refuse = false
	if err := e.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	e.WaitIdle()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[1] != (seen{store.DefaultProviderID, "", true, start.Add(2 * time.Hour)}) {
		t.Fatalf("credit seen = %+v, want the success reported", got)
	}
}
