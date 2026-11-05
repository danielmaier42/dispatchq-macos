package engine

import (
	"context"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// TestAPoolMemberOutOfCreditHandsTheTaskOn: a pool member refused for a used-up
// credit balance does not keep the task waiting; the next tick starts it on
// another member.
func TestAPoolMemberOutOfCreditHandsTheTaskOn(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	fc := clock.NewFake(now)
	r := &stub{result: func(req executor.Request, n int) provider.Result {
		if n == 1 {
			return provider.Result{Status: store.StatusRateLimited, SessionID: req.SessionID, RetryAfter: time.Hour, Block: provider.BlockCreditExhausted}
		}
		return provider.Result{Status: store.StatusSuccess, SessionID: req.SessionID}
	}}
	e, st, _ := newTestEngineWithProvider(t, r, fc)
	if err := st.SaveConfig(poolConfig(poolTask("a", false))); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := e.Tick(context.Background()); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		e.WaitIdle()
		fc.Advance(time.Minute)
	}
	reqs := r.requests()
	if len(reqs) != 2 || reqs[0].Provider.ID == reqs[1].Provider.ID {
		t.Fatalf("runs = %d on %v, want a second run on the other member", len(reqs), providers(reqs))
	}
}

func providers(reqs []executor.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Provider.ID
	}
	return out
}
