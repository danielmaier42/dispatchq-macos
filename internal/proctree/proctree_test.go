//go:build unix

package proctree

import "testing"

func TestDescendantsGroupsByDepth(t *testing.T) {
	// 100 → 200 → 300, plus an unrelated 400.
	snapshot := []procEntry{
		{pid: 100, ppid: 1, pgid: 100},
		{pid: 200, ppid: 100, pgid: 200},
		{pid: 300, ppid: 200, pgid: 300},
		{pid: 400, ppid: 1, pgid: 400},
	}
	got := descendants(snapshot, 100)
	if len(got) != 2 || len(got[0]) != 1 || got[0][0].pid != 200 || len(got[1]) != 1 || got[1][0].pid != 300 {
		t.Fatalf("generations = %+v, want [[200] [300]]", got)
	}
}

// TestDescendantsToleratesACycle: a reparented process whose ppid was reused can
// make the table look circular, and the teardown must still return.
func TestDescendantsToleratesACycle(t *testing.T) {
	snapshot := []procEntry{
		{pid: 100, ppid: 200, pgid: 100},
		{pid: 200, ppid: 100, pgid: 200},
	}
	if got := descendants(snapshot, 100); len(got) != 1 || got[0][0].pid != 200 {
		t.Fatalf("generations = %+v, want the cycle walked once", got)
	}
}

func TestDescendantsWithoutASnapshot(t *testing.T) {
	if got := descendants(nil, 100); got != nil {
		t.Fatalf("generations = %+v, want none without a process table", got)
	}
}
