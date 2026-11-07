package main

import (
	"context"
	"errors"
	"testing"
)

type swVersRunner struct {
	out string
	err error
}

func (f swVersRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(f.out), f.err
}

// The feedback footer prints the value as it is, so the platform names its
// own system; an unreadable version stays empty rather than a bare "macOS ".
func TestOSVersionNamesMacOS(t *testing.T) {
	if got := osVersion(swVersRunner{out: "15.6\n"})(); got != "macOS 15.6" {
		t.Fatalf("osVersion = %q, want %q", got, "macOS 15.6")
	}
	if got := osVersion(swVersRunner{err: errors.New("no sw_vers")})(); got != "" {
		t.Fatalf("osVersion on error = %q, want empty", got)
	}
	if got := osVersion(swVersRunner{out: "  \n"})(); got != "" {
		t.Fatalf("osVersion on blank output = %q, want empty", got)
	}
}
