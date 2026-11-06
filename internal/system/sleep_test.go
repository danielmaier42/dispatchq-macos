package system

import (
	"runtime"
	"testing"
)

func TestDefaultSleepInhibitorExistsWhereImplemented(t *testing.T) {
	want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if got := DefaultSleepInhibitor() != nil; got != want {
		t.Fatalf("DefaultSleepInhibitor set = %t on %s, want %t", got, runtime.GOOS, want)
	}
}
