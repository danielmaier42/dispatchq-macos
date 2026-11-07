package provider

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps an adapter [Kind] to its implementation. Adding a harness means
// registering one more adapter here — the scheduler, the store and the run
// history are unaffected, because they only ever name a provider ID or ask for
// a capability.
type Registry struct {
	mu     sync.RWMutex
	byKind map[Kind]Adapter
	// wsl runs WSL instances; nil where WSL does not exist (see SetWSL).
	wsl WSLService
}

// NewRegistry returns a registry holding the given adapters. It panics on a
// duplicate or malformed adapter, because the adapter set is wired at startup
// and a broken one is a programming error, not a runtime condition.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{byKind: map[Kind]Adapter{}}
	for _, a := range adapters {
		if err := r.Register(a); err != nil {
			panic("provider: " + err.Error())
		}
	}
	return r
}

// Register adds an adapter. A kind may be registered only once.
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return fmt.Errorf("register adapter: adapter is nil")
	}
	kind := a.Kind()
	if kind == "" {
		return fmt.Errorf("register adapter: empty kind")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byKind[kind]; dup {
		return fmt.Errorf("register adapter: kind %q is already registered", kind)
	}
	r.byKind[kind] = a
	return nil
}

// Lookup returns the adapter for a kind.
func (r *Registry) Lookup(kind Kind) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byKind[kind]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for provider kind %q", kind)
	}
	return a, nil
}

// Kinds returns the registered kinds in a stable order.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Kind, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
