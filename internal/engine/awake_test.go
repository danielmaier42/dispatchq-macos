package engine

import (
	"errors"
	"testing"
)

// fakeInhibitor counts the holds it hands out and how many are still held.
type fakeInhibitor struct {
	taken, held int
	err         error
}

func (f *fakeInhibitor) Inhibit() (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.taken++
	f.held++
	return func() { f.held-- }, nil
}

func TestSleepGuardRefcount(t *testing.T) {
	f := &fakeInhibitor{}
	var g sleepGuard
	g.setInhibitor(f)
	g.acquire()
	g.acquire()
	if f.taken != 1 || f.held != 1 {
		t.Fatalf("two runs: taken %d, held %d; want one shared hold", f.taken, f.held)
	}
	g.release()
	if f.held != 1 {
		t.Fatal("hold released while a run is still active (refcount broken)")
	}
	g.release()
	if f.held != 0 {
		t.Fatal("hold not released once no runs are active")
	}
	g.acquire()
	if f.taken != 2 || f.held != 1 {
		t.Fatalf("next run: taken %d, held %d; want a fresh hold", f.taken, f.held)
	}
	g.release()
}

func TestSleepGuardWithoutInhibitorIsANoOp(t *testing.T) {
	var g sleepGuard
	g.acquire()
	g.release()
	g.release() // an extra release must not drive the count negative
	if g.n != 0 || g.hold != nil {
		t.Fatalf("n = %d, hold set = %t; want an idle guard", g.n, g.hold != nil)
	}
}

// A hold that fails is taken by the next run that starts, even while the
// first run is still in flight.
func TestSleepGuardRetriesAFailedHold(t *testing.T) {
	f := &fakeInhibitor{err: errors.New("no helper")}
	var g sleepGuard
	g.setInhibitor(f)
	g.acquire()
	if g.hold != nil {
		t.Fatal("hold set although Inhibit failed")
	}
	f.err = nil
	g.acquire()
	if f.taken != 1 || f.held != 1 {
		t.Fatalf("second run: taken %d, held %d; want the retried hold", f.taken, f.held)
	}
	g.release()
	g.release()
	if f.held != 0 || g.n != 0 {
		t.Fatalf("after both runs: held %d, n %d; want an idle guard", f.held, g.n)
	}
}
