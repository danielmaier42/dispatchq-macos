//go:build windows

package system

import "testing"

func TestExecutionStateHoldTakesAndReleases(t *testing.T) {
	release, err := DefaultSleepInhibitor().Inhibit()
	if err != nil {
		t.Fatalf("Inhibit: %v", err)
	}
	release()
	release() // a second release must be harmless
}
