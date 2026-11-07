package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// limitsAdapter is the stub harness with a scripted allowance.
type limitsAdapter struct {
	stubAdapter
	windows []provider.LimitWindow
	err     error
}

func (a limitsAdapter) ReadLimits(context.Context, provider.Instance, provider.Host) (provider.LimitReading, error) {
	return provider.LimitReading{Windows: a.windows}, a.err
}

func withLimits(t *testing.T, a limitsAdapter) {
	t.Helper()
	previous := newProviderRegistry
	newProviderRegistry = func() *provider.Registry { return provider.NewRegistry(a) }
	t.Cleanup(func() { newProviderRegistry = previous })
}

func TestCmdProviderLimits(t *testing.T) {
	st := newTestStore(t)
	reset := time.Now().Add(3 * time.Hour)
	withLimits(t, limitsAdapter{windows: []provider.LimitWindow{
		{ID: "five_hour", Label: "5 hours", UsedPercent: 42, ResetsAt: &reset},
		{ID: "week", Label: "Week", UsedPercent: 84},
	}})

	out := captureStdout(t, func() error { return cmdProvider(st, []string{"limits"}) })
	if !strings.Contains(out, "5 hours") || !strings.Contains(out, "42%") || !strings.Contains(out, "84%") {
		t.Fatalf("table = %q, want both windows", out)
	}
	if !strings.Contains(out, "(in 2h 59m)") && !strings.Contains(out, "(in 3h 0m)") {
		t.Fatalf("table = %q, want the time until the reset", out)
	}

	out = captureStdout(t, func() error {
		return cmdProvider(st, []string{"limits", provider.DefaultInstanceID, "--json"})
	})
	var views []limitsView
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if len(views) != 1 || views[0].ID != provider.DefaultInstanceID || !views[0].Default ||
		views[0].Limits.State != provider.LimitsOK || len(views[0].Limits.Windows) != 2 {
		t.Fatalf("views = %+v", views)
	}
}

func TestCmdProviderLimitsShowsWhyThereAreNone(t *testing.T) {
	st := newTestStore(t)
	withLimits(t, limitsAdapter{err: errors.New("the stored Claude login has expired")})
	out := captureStdout(t, func() error { return cmdProvider(st, []string{"limits"}) })
	if !strings.Contains(out, "The stored Claude login has expired.") {
		t.Fatalf("table = %q, want the reason", out)
	}
}

func TestCmdProviderLimitsRefusesAnUnknownProvider(t *testing.T) {
	st := newTestStore(t)
	withLimits(t, limitsAdapter{})
	if err := cmdProvider(st, []string{"limits", "nope"}); !errors.Is(err, provider.ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestCmdProviderLimitsArguments(t *testing.T) {
	st := newTestStore(t)
	withLimits(t, limitsAdapter{windows: []provider.LimitWindow{{ID: "week", Label: "Week", UsedPercent: 1}}})
	out := captureStdout(t, func() error {
		return cmdProvider(st, []string{"limits", "--json", provider.DefaultInstanceID})
	})
	var views []limitsView
	if err := json.Unmarshal([]byte(out), &views); err != nil || len(views) != 1 {
		t.Fatalf("id after the flags: %v, %+v", err, views)
	}
	if err := cmdProvider(st, []string{"limits", provider.DefaultInstanceID, "extra"}); err == nil {
		t.Fatal("a second id must be refused")
	}
}

// TestCmdProviderLimitsShowsTheCreditProof: an API-key account shows the
// daemon's latest ping instead of a balance nobody can read.
func TestCmdProviderLimitsShowsTheCreditProof(t *testing.T) {
	st := newTestStore(t)
	withLimits(t, limitsAdapter{err: provider.ErrNoPlanLimits})
	if err := st.UpdateState(func(s *store.State) error {
		s.CreditChecks[provider.DefaultInstanceID] = store.CreditCheck{
			Status: string(provider.BlockCreditExhausted), Source: "ping", CheckedAt: time.Now(),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return cmdProvider(st, []string{"limits"}) })
	if !strings.Contains(out, "No credit left (refused by a ping, ") {
		t.Fatalf("table = %q, want the refused ping", out)
	}
}
