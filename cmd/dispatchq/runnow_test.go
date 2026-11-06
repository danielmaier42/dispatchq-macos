package main

import (
	"testing"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/system"
)

// `dispatchq run-now` builds its own engine, separate from the daemon's. It must
// get the platform's sleep inhibitor too, or a long manual run is frozen when
// the machine idles to sleep while the user is away.
func TestRunNowEngineHoldsOffSleepLikeTheDaemon(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	eng := newRunNowEngine(st, clock.Real{}, "dispatchq")
	want := system.DefaultSleepInhibitor()
	got := eng.SleepInhibitor()
	if (want != nil) != (got != nil) {
		t.Fatalf("run-now engine inhibitor set = %t, platform offers one = %t", got != nil, want != nil)
	}
}
