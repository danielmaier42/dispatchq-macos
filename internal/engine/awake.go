package engine

import "sync"

// sleepGuard keeps the machine awake while runs are in flight, so a task can't
// be frozen mid-run when the machine would otherwise idle-sleep. It is
// reference-counted, so concurrent runs share a single assertion. How the
// assertion is held is the platform's: `caffeinate -i` on macOS, an execution
// state request on Windows (see holdAwake). Where there is no mechanism it does
// nothing.
type sleepGuard struct {
	mu   sync.Mutex
	n    int
	stop func()
	// hold starts the assertion; nil means the platform's holdAwake. A test
	// replaces it.
	hold func() func()
}

func (g *sleepGuard) acquire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	if g.n == 1 && g.stop == nil {
		hold := g.hold
		if hold == nil {
			hold = holdAwake
		}
		g.stop = hold()
	}
}

func (g *sleepGuard) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n > 0 {
		g.n--
	}
	if g.n == 0 && g.stop != nil {
		g.stop()
		g.stop = nil
	}
}
