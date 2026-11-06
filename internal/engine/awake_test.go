package engine

import "testing"

func TestSleepGuardRefcount(t *testing.T) {
	started, stopped := 0, 0
	g := sleepGuard{hold: func() func() {
		started++
		return func() { stopped++ }
	}}
	g.acquire()
	g.acquire()
	if started != 1 {
		t.Fatalf("started %d assertions for two concurrent runs, want 1", started)
	}
	g.release()
	if stopped != 0 {
		t.Fatal("assertion released while a run is still active (refcount broken)")
	}
	g.release()
	if stopped != 1 {
		t.Fatal("assertion not released once no runs are active")
	}
	g.acquire()
	if started != 2 {
		t.Fatalf("a later run did not take a fresh assertion (started=%d)", started)
	}
}

func TestSleepGuardSurvivesAPlatformWithoutAMechanism(_ *testing.T) {
	g := sleepGuard{hold: func() func() { return nil }}
	g.acquire()
	g.release() // must not call a nil stop
}
