package main

import (
	"errors"
	"testing"
)

type countedDaemonHandle struct {
	daemonProcess
	closed *int
}

func (h countedDaemonHandle) Close() error {
	*h.closed++
	return h.daemonProcess.Close()
}

func TestStopperClosesEveryVerifiedHandleOnFailure(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.daemon(200, ourImage, homeA, t0)
	inv.termErr = errors.New("termination refused")
	stopper := inv.stopper(homeA)
	closed := map[uint32]*int{100: new(int), 200: new(int)}
	stopper.open = func(pid uint32) (daemonProcess, error) {
		h, err := inv.open(pid)
		if err != nil {
			return nil, err
		}
		return countedDaemonHandle{daemonProcess: h, closed: closed[pid]}, nil
	}
	stopped, err := stopper.stop()
	if !errors.Is(err, inv.termErr) || len(stopped) != 0 {
		t.Fatalf("stop = %v, %v; want the termination failure", stopped, err)
	}
	for pid, count := range closed {
		if *count != 1 {
			t.Errorf("pid %d handle closed %d times, want exactly once", pid, *count)
		}
	}
}
