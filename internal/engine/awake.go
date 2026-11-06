package engine

import "sync"

// SleepInhibitor holds off idle system sleep. Inhibit takes the hold, and the
// returned release gives it back. system.Hold satisfies it.
type SleepInhibitor interface {
	Inhibit() (release func(), err error)
}

// sleepGuard keeps the machine awake while runs are in flight. Without it, an
// idle sleep can freeze a task mid-run. It is reference-counted, so concurrent
// runs share a single hold. Without an inhibitor (see Engine.SetSleepInhibitor)
// it does nothing. A hold that cannot be taken is tried again by the next run
// that starts.
type sleepGuard struct {
	mu        sync.Mutex
	n         int
	inhibitor SleepInhibitor
	hold      func() // releases the current hold, nil when none is held
}

func (g *sleepGuard) setInhibitor(i SleepInhibitor) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inhibitor = i
}

func (g *sleepGuard) current() SleepInhibitor {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inhibitor
}

func (g *sleepGuard) acquire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	if g.hold == nil && g.inhibitor != nil {
		if release, err := g.inhibitor.Inhibit(); err == nil {
			g.hold = release
		}
	}
}

func (g *sleepGuard) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n > 0 {
		g.n--
	}
	if g.n == 0 && g.hold != nil {
		g.hold()
		g.hold = nil
	}
}
