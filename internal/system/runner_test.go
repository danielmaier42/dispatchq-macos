package system

import (
	"context"
	"testing"
)

type plainRunner struct{}

func (plainRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func TestShowingWindowsPicksTheInteractiveRunner(t *testing.T) {
	if got := ShowingWindows(Real{}); got != (Interactive{}) {
		t.Errorf("ShowingWindows(Real) = %T, want Interactive", got)
	}
	if got := ShowingWindows(Interactive{}); got != (Interactive{}) {
		t.Errorf("ShowingWindows(Interactive) = %T, want Interactive", got)
	}
	if got := ShowingWindows(plainRunner{}); got != (plainRunner{}) {
		t.Errorf("a runner without a variant must be kept, got %T", got)
	}
}
