package limit

import (
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
)

func TestGateStartsOpen(t *testing.T) {
	g := New(clock.NewFake(time.Unix(0, 0)))
	if !g.Open() {
		t.Fatal("new gate should be open")
	}
	if !g.BlockedUntil().IsZero() {
		t.Fatal("open gate should report zero BlockedUntil")
	}
}

func TestBlockForClosesThenReopens(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	g := New(fc)

	g.BlockFor(2 * time.Hour)

	if g.Open() {
		t.Fatal("gate should be closed right after BlockFor")
	}
	if want := start.Add(2 * time.Hour); !g.BlockedUntil().Equal(want) {
		t.Fatalf("BlockedUntil = %v, want %v", g.BlockedUntil(), want)
	}

	fc.Advance(2 * time.Hour)
	if !g.Open() {
		t.Fatal("gate should reopen once the block time passes")
	}
	if !g.BlockedUntil().IsZero() {
		t.Fatal("reopened gate should report zero BlockedUntil")
	}
}

func TestLongestBlockWins(t *testing.T) {
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	g := New(fc)

	g.BlockFor(3 * time.Hour)
	g.BlockFor(1 * time.Hour) // shorter: must not shorten the existing block

	if want := start.Add(3 * time.Hour); !g.BlockedUntil().Equal(want) {
		t.Fatalf("BlockedUntil = %v, want %v (shorter block must not win)", g.BlockedUntil(), want)
	}
}

func TestConcurrentBlockAndRead(_ *testing.T) {
	fc := clock.NewFake(time.Unix(0, 0))
	g := New(fc)

	done := make(chan struct{})
	go func() {
		for range 1000 {
			g.BlockFor(time.Minute)
		}
		close(done)
	}()
	for range 1000 {
		_ = g.Open()
		_ = g.BlockedUntil()
	}
	<-done
}

func TestBlockReasonLastsWhileClosedAndIsForgottenOnReopen(t *testing.T) {
	start := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	fc := clock.NewFake(start)
	gates := NewGates(fc)
	g := gates.For("api")

	g.BlockBecause(start.Add(time.Hour), "credit_exhausted")
	// A plain limit reported while the account block stands keeps its reason.
	g.Block(start.Add(2 * time.Hour))
	if got := gates.Reasons(); got["api"] != "credit_exhausted" || len(got) != 1 {
		t.Fatalf("reasons = %v, want the credit block", got)
	}

	fc.Advance(2 * time.Hour)
	if g.Reason() != "" || len(gates.Reasons()) != 0 {
		t.Fatalf("reason = %q after reopening, want none", g.Reason())
	}
	// A plain limit after the gate reopened does not bring the old reason back.
	g.Block(fc.Now().Add(time.Hour))
	if g.Reason() != "" {
		t.Fatalf("reason = %q for a plain limit, want none", g.Reason())
	}
}

func TestLiftReopensOnlyAnAccountBlock(t *testing.T) {
	start := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	g := New(clock.NewFake(start))

	g.BlockBecause(start.Add(7*24*time.Hour), "credit_exhausted")
	g.Lift("billing_refused")
	if g.Open() {
		t.Fatal("Lift ended a block whose reason it was not given")
	}
	g.Lift("credit_exhausted")
	if !g.Open() || g.Reason() != "" {
		t.Fatalf("after Lift: open=%v reason=%q, want open and no reason", g.Open(), g.Reason())
	}

	g.Block(start.Add(time.Hour))
	g.Lift("credit_exhausted")
	if g.Open() {
		t.Fatal("Lift reopened a gate an ordinary limit closed")
	}

	// An ordinary limit that came during an account block outlives the lift.
	g.BlockBecause(start.Add(7*24*time.Hour), "credit_exhausted")
	g.Block(start.Add(2 * time.Hour))
	g.Lift("credit_exhausted")
	if got := g.BlockedUntil(); !got.Equal(start.Add(2 * time.Hour)) {
		t.Fatalf("after Lift the gate reopens at %v, want the limit's reset", got)
	}
}
