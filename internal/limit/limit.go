// Package limit implements the reactive, global rate-limit gate (PLAN.md D1).
//
// There is no reliable way to query the remaining allowance ahead of time, so
// the gate is purely reactive: a task simply runs, and if Claude Code reports a
// rate limit the gate is Blocked until a derived reset time. While blocked, no
// new task is started; the block clears automatically once the time passes.
package limit

import (
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
)

// Gate is a concurrency-safe global rate-limit gate shared by all tasks.
type Gate struct {
	clock clock.Clock

	mu           sync.Mutex
	blockedUntil time.Time // zero => open
	// reason names what closed the gate when that was more than a used-up
	// allowance window (a provider.Block); it is only read while blocked.
	reason string
}

// New returns an open Gate using the given clock.
func New(c clock.Clock) *Gate {
	return &Gate{clock: c}
}

// Block closes the gate until until. A call with an earlier time than the
// current block is ignored, so the latest/longest known reset always wins.
func (g *Gate) Block(until time.Time) { g.BlockBecause(until, "") }

// BlockBecause closes the gate like Block and remembers why, so the dashboard
// can tell an account block (a used-up credit balance, say) from a limit that
// resets on its own. A reason outlives later blocks without one while the gate
// stays closed, and is forgotten once it reopens.
func (g *Gate) BlockBecause(until time.Time, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.now().Before(g.blockedUntil) {
		g.reason = ""
	}
	if until.After(g.blockedUntil) {
		g.blockedUntil = until
	}
	if reason != "" {
		g.reason = reason
	}
}

// BlockFor closes the gate for the given delay from now. It is the fallback
// entry point for rate-limit events that expose a retry delay but no absolute
// reset timestamp (PLAN.md V2); when the CLI reports one (rate_limit_event),
// the engine uses Block with that time directly.
func (g *Gate) BlockFor(delay time.Duration) {
	g.Block(g.clock.Now().Add(delay))
}

// Reason returns why the gate is closed, or "" when it is open or was closed
// by an ordinary limit.
func (g *Gate) Reason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now().Before(g.blockedUntil) {
		return g.reason
	}
	return ""
}

// Open reports whether a task may start now.
func (g *Gate) Open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.now().Before(g.blockedUntil)
}

// BlockedUntil returns the time the gate reopens, or the zero time if open.
func (g *Gate) BlockedUntil() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.now().Before(g.blockedUntil) {
		return g.blockedUntil
	}
	return time.Time{}
}

func (g *Gate) now() time.Time { return g.clock.Now() }

// Gates are the rate-limit gates of several provider instances, one each.
//
// An allowance belongs to an account, not to dispatchq: a Codex limit says
// nothing about a Claude subscription, and two Claude subscriptions do not
// share one either. So a blocked provider holds up only its own tasks, and the
// rest of the queue keeps going.
type Gates struct {
	clock clock.Clock

	mu sync.Mutex
	by map[string]*Gate
}

// NewGates returns an empty set of gates using the given clock.
func NewGates(c clock.Clock) *Gates {
	return &Gates{clock: c, by: map[string]*Gate{}}
}

// For returns the gate of one provider instance, creating it on first use.
func (g *Gates) For(providerID string) *Gate {
	g.mu.Lock()
	defer g.mu.Unlock()
	gate, ok := g.by[providerID]
	if !ok {
		gate = New(g.clock)
		g.by[providerID] = gate
	}
	return gate
}

// BlockedUntil returns the earliest time any gate reopens, or the zero time
// when none is blocked. It answers "when does the queue continue?" for a
// dashboard that shows one banner: the first provider to come back is when
// something can run again.
func (g *Gates) BlockedUntil() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	var earliest time.Time
	for _, gate := range g.by {
		until := gate.BlockedUntil()
		if until.IsZero() {
			continue
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return earliest
}

// Blocked returns the provider instance IDs currently waiting out a rate
// limit, each with the time its own gate reopens. It answers "who is the
// queue waiting on?" so a banner naming only "the queue" never leaves the
// operator guessing which account is blocked.
func (g *Gates) Blocked() map[string]time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]time.Time{}
	for id, gate := range g.by {
		if until := gate.BlockedUntil(); !until.IsZero() {
			out[id] = until
		}
	}
	return out
}

// Reasons returns, for the provider instances currently blocked by more than
// an ordinary limit, why each one is (see Gate.BlockBecause).
func (g *Gates) Reasons() map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]string{}
	for id, gate := range g.by {
		if r := gate.Reason(); r != "" {
			out[id] = r
		}
	}
	return out
}
