package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// limitsStub is the test harness with an allowance: every read reports the
// week at the number of reads so far, so a test can tell a new reading from a
// remembered one.
type limitsStub struct {
	stubAdapter
	reads *atomic.Int32
}

func (s limitsStub) ReadLimits(context.Context, provider.Instance) (provider.LimitReading, error) {
	n := s.reads.Add(1)
	return provider.LimitReading{Windows: []provider.LimitWindow{{ID: "week", Label: "Week", UsedPercent: float64(n)}}}, nil
}

func TestListLimits(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reads := &atomic.Int32{}
	reg := provider.NewRegistry(limitsStub{stubAdapter: stubAdapter{health: provider.Health{State: provider.HealthReady}}, reads: reads})
	mon := provider.NewLimitMonitor(reg, nil)
	mon.MinAge = 1 // any reading is old enough for a fresh request
	srv := httptest.NewServer(Handler(Deps{Store: st, Registry: reg, Providers: provider.NewChecker(reg), Limits: mon}))
	t.Cleanup(srv.Close)

	var got []limitsView
	do(t, srv, http.MethodGet, "/api/limits", nil).into(t, &got)
	if len(got) != 1 || got[0].ID != provider.DefaultInstanceID || !got[0].Default || got[0].TypeName != "Claude" {
		t.Fatalf("limits = %+v, want the default instance", got)
	}
	if l := got[0].Limits; l.State != provider.LimitsOK || len(l.Windows) != 1 || l.Windows[0].UsedPercent != 1 {
		t.Fatalf("limits = %+v, want the first reading", l)
	}
	// The poll answers from memory…
	do(t, srv, http.MethodGet, "/api/limits", nil).into(t, &got)
	if got[0].Limits.Windows[0].UsedPercent != 1 {
		t.Fatalf("poll read again: %+v", got[0].Limits)
	}
	// …and opening the dashboard reads again.
	do(t, srv, http.MethodGet, "/api/limits?fresh=1", nil).into(t, &got)
	if got[0].Limits.Windows[0].UsedPercent != 2 || reads.Load() != 2 {
		t.Fatalf("fresh = %+v after %d reads, want a second reading", got[0].Limits, reads.Load())
	}
}

func TestListLimitsWithoutAMonitor(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	var got []limitsView
	do(t, srv, http.MethodGet, "/api/limits", nil).into(t, &got)
	if len(got) != 0 {
		t.Fatalf("limits = %+v, want none", got)
	}
}

func TestListLimitsNamesAUsedUpCreditBalance(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg := provider.NewRegistry(limitsStub{stubAdapter: stubAdapter{health: provider.Health{State: provider.HealthReady}}, reads: &atomic.Int32{}})
	until := time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(Handler(Deps{
		Store: st, Registry: reg, Providers: provider.NewChecker(reg), Limits: provider.NewLimitMonitor(reg, nil),
		BlockedProviders: func() map[string]time.Time { return map[string]time.Time{provider.DefaultInstanceID: until} },
		BlockReasons: func() map[string]string {
			return map[string]string{provider.DefaultInstanceID: string(provider.BlockCreditExhausted)}
		},
	}))
	t.Cleanup(srv.Close)

	var got []limitsView
	do(t, srv, http.MethodGet, "/api/limits", nil).into(t, &got)
	if len(got) != 1 || got[0].Blocked == nil {
		t.Fatalf("limits = %+v, want the closed gate", got)
	}
	if b := got[0].Blocked; !b.Until.Equal(until) || b.Reason != string(provider.BlockCreditExhausted) {
		t.Fatalf("blocked = %+v, want the credit block until %v", b, until)
	}

	var health struct {
		Limited []limitedProvider `json:"limited_providers"`
	}
	do(t, srv, http.MethodGet, "/api/health", nil).into(t, &health)
	if len(health.Limited) != 1 || health.Limited[0].Reason != string(provider.BlockCreditExhausted) {
		t.Fatalf("health = %+v, want the credit block named", health.Limited)
	}
}

// apiKeyStub is a harness on an API key: no plan windows to read.
type apiKeyStub struct{ stubAdapter }

func (apiKeyStub) ReadLimits(context.Context, provider.Instance) (provider.LimitReading, error) {
	return provider.LimitReading{}, provider.ErrNoPlanLimits
}

// fakeCredits records the pings Read now asks for and answers with a proof.
type fakeCredits struct {
	pinged []string
	checks map[string]store.CreditCheck
}

func (f *fakeCredits) Checks() (map[string]store.CreditCheck, error) { return f.checks, nil }
func (f *fakeCredits) Failure(string) string                         { return "" }
func (f *fakeCredits) PingAll(_ context.Context, insts []provider.Instance) {
	for _, inst := range insts {
		f.pinged = append(f.pinged, inst.ID)
		f.checks[inst.ID] = store.CreditCheck{Status: "ok", Source: "ping", CheckedAt: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	}
}

// TestReadNowPingsAPIKeys: only ?ping=1 (the Read now button) pings, opening
// the Dashboard (?fresh=1) does not, and the proof comes back on the line.
func TestReadNowPingsAPIKeys(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg := provider.NewRegistry(apiKeyStub{stubAdapter{health: provider.Health{State: provider.HealthReady}}})
	credits := &fakeCredits{checks: map[string]store.CreditCheck{}}
	srv := httptest.NewServer(Handler(Deps{Store: st, Registry: reg, Providers: provider.NewChecker(reg),
		Limits: provider.NewLimitMonitor(reg, nil), Credits: credits}))
	t.Cleanup(srv.Close)

	var got []limitsView
	do(t, srv, http.MethodGet, "/api/limits?fresh=1", nil).into(t, &got)
	if len(credits.pinged) != 0 || got[0].Credit != nil {
		t.Fatalf("opening the Dashboard pinged %v / credit %+v, want nothing", credits.pinged, got[0].Credit)
	}
	do(t, srv, http.MethodGet, "/api/limits?fresh=1&ping=1", nil).into(t, &got)
	if len(credits.pinged) != 1 || credits.pinged[0] != provider.DefaultInstanceID {
		t.Fatalf("Read now pinged %v, want the API-key account", credits.pinged)
	}
	if c := got[0].Credit; c == nil || c.Status != "ok" || c.Source != "ping" {
		t.Fatalf("credit = %+v, want the ping's answer", c)
	}
}
