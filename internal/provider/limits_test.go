package provider

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// limitAdapter is a fake harness that reports an allowance. Each read returns
// the next scripted answer, the last one repeating.
type limitAdapter struct {
	*fakeAdapter

	mu      sync.Mutex
	reads   int
	answers []limitAnswer
}

type limitAnswer struct {
	windows []LimitWindow
	err     error
}

func (a *limitAdapter) ReadLimits(context.Context, Instance, Host) (LimitReading, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := min(a.reads, len(a.answers)-1)
	a.reads++
	return LimitReading{Windows: a.answers[i].windows}, a.answers[i].err
}

func (a *limitAdapter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reads
}

func week(pct float64) []LimitWindow {
	return []LimitWindow{{ID: "week", Label: "Week", UsedPercent: pct}}
}

// newLimitMonitor returns a monitor over one limit-reporting adapter, its
// instance, and a clock the test moves by hand.
func newLimitMonitor(answers ...limitAnswer) (*LimitMonitor, *limitAdapter, Instance, *time.Time) {
	ad := &limitAdapter{fakeAdapter: &fakeAdapter{kind: "fake"}, answers: answers}
	inst := Instance{ID: "acct", Kind: "fake", Enabled: true}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := NewLimitMonitor(NewRegistry(ad), func() ([]Instance, error) { return []Instance{inst}, nil })
	m.Now = func() time.Time { return now }
	return m, ad, inst, &now
}

func TestLimitMonitorCachesAndHonoursMinAge(t *testing.T) {
	m, ad, inst, now := newLimitMonitor(limitAnswer{windows: week(10)}, limitAnswer{windows: week(20)})
	ctx := context.Background()

	first := m.Get(ctx, []Instance{inst}, false)[0]
	if first.State != LimitsOK || first.Windows[0].UsedPercent != 10 || !first.UpdatedAt.Equal(*now) {
		t.Fatalf("first read = %+v, want 10%% read now", first)
	}
	// A plain poll never reads again.
	m.Get(ctx, []Instance{inst}, false)
	// A fresh request inside MinAge reuses the reading.
	*now = now.Add(DefaultLimitsMinAge - time.Second)
	if got := m.Get(ctx, []Instance{inst}, true)[0]; got.Windows[0].UsedPercent != 10 {
		t.Fatalf("fresh inside MinAge = %+v, want the cached reading", got)
	}
	if ad.count() != 1 {
		t.Fatalf("reads = %d, want 1", ad.count())
	}
	*now = now.Add(2 * time.Second)
	if got := m.Get(ctx, []Instance{inst}, true)[0]; got.Windows[0].UsedPercent != 20 {
		t.Fatalf("fresh after MinAge = %+v, want a new reading", got)
	}
	if ad.count() != 2 {
		t.Fatalf("reads = %d, want 2", ad.count())
	}
}

func TestLimitMonitorRefreshIDIgnoresMinAge(t *testing.T) {
	m, ad, inst, _ := newLimitMonitor(limitAnswer{windows: week(10)}, limitAnswer{windows: week(55)})
	ctx := context.Background()
	m.Get(ctx, []Instance{inst}, false)
	// A run just ended: its account moved, however recent the last reading.
	if err := m.RefreshID(ctx, "acct"); err != nil {
		t.Fatalf("RefreshID: %v", err)
	}
	if got := m.Get(ctx, []Instance{inst}, false)[0]; got.Windows[0].UsedPercent != 55 {
		t.Fatalf("after RefreshID = %+v, want 55%%", got)
	}
	if err := m.RefreshID(ctx, "unknown"); err != nil {
		t.Fatalf("RefreshID of an unknown id: %v", err)
	}
	if ad.count() != 2 {
		t.Fatalf("reads = %d, want 2", ad.count())
	}
}

func TestLimitMonitorKeepsLastWindowsWhenAReadFails(t *testing.T) {
	m, _, inst, now := newLimitMonitor(limitAnswer{windows: week(40)}, limitAnswer{err: errors.New("the stored Claude login has expired")})
	ctx := context.Background()
	read := *now
	m.Get(ctx, []Instance{inst}, false)
	*now = now.Add(time.Hour)
	got := m.Get(ctx, []Instance{inst}, true)[0]
	if got.State != LimitsUnavailable {
		t.Fatalf("state = %q, want unavailable", got.State)
	}
	if got.Reason != "The stored Claude login has expired." {
		t.Fatalf("reason = %q, want the error as a sentence", got.Reason)
	}
	if len(got.Windows) != 1 || got.Windows[0].UsedPercent != 40 || !got.UpdatedAt.Equal(read) {
		t.Fatalf("windows = %+v at %v, want the last good reading from %v", got.Windows, got.UpdatedAt, read)
	}
	if !got.CheckedAt.Equal(*now) {
		t.Fatalf("checked at %v, want %v", got.CheckedAt, *now)
	}
}

// An account on an API key has no plan: nothing failed, so it must not read as
// a broken reading with a warning, wrapped or not.
func TestLimitMonitorReportsAnAccountWithoutAPlanAsUnsupported(t *testing.T) {
	m, _, inst, _ := newLimitMonitor(limitAnswer{err: fmt.Errorf("claude: %w", ErrNoPlanLimits)})
	got := m.Get(context.Background(), []Instance{inst}, true)[0]
	if got.State != LimitsUnsupported || got.Reason != NoCreditReading || !got.APIKey {
		t.Fatalf("limits = %+v, want unsupported with the unknown credit balance", got)
	}
}

func TestLimitMonitorForgetsAReadingOfAnotherConfiguration(t *testing.T) {
	m, ad, inst, _ := newLimitMonitor(limitAnswer{windows: week(10)}, limitAnswer{windows: week(70)})
	ctx := context.Background()
	m.Get(ctx, []Instance{inst}, false)
	// Another configuration directory is another account.
	inst.ConfigDir = "/other"
	if got := m.Get(ctx, []Instance{inst}, false)[0]; got.Windows[0].UsedPercent != 70 || ad.count() != 2 {
		t.Fatalf("after a config change = %+v (%d reads), want a new reading", got, ad.count())
	}
}

func TestLimitMonitorDisabledAndUnsupported(t *testing.T) {
	plain := &fakeAdapter{kind: "plain"}
	m := NewLimitMonitor(NewRegistry(plain), nil)
	ctx := context.Background()
	got := m.Get(ctx, []Instance{
		{ID: "off", Kind: "plain", Enabled: false},
		{ID: "on", Kind: "plain", Enabled: true},
	}, true)
	if got[0].State != LimitsDisabled {
		t.Fatalf("disabled instance = %+v", got[0])
	}
	if got[1].State != LimitsUnsupported || got[1].Reason != "No limit" {
		t.Fatalf("instance without a LimitReader = %+v", got[1])
	}
}

func TestLimitMonitorDoesNotRememberAnInterruptedRead(t *testing.T) {
	m, ad, inst, _ := newLimitMonitor(limitAnswer{err: context.Canceled}, limitAnswer{windows: week(5)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := m.Get(ctx, []Instance{inst}, false)[0]; got.State != LimitsUnavailable {
		t.Fatalf("interrupted read = %+v", got)
	}
	if got := m.Get(context.Background(), []Instance{inst}, false)[0]; got.State != LimitsOK || ad.count() != 2 {
		t.Fatalf("next read = %+v (%d reads), want a real reading", got, ad.count())
	}
}

func TestLimitMonitorLoopReadsAtStartAndOnEveryTick(t *testing.T) {
	m, ad, _, now := newLimitMonitor(limitAnswer{windows: week(1)})
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { m.loop(ctx, tick, t.Logf); close(done) }()

	waitReads := func(n int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for ad.count() < n {
			select {
			case <-deadline:
				t.Fatalf("reads = %d, want %d", ad.count(), n)
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	waitReads(1)
	*now = now.Add(DefaultLimitsInterval)
	tick <- *now
	waitReads(2)
	cancel()
	<-done
}
