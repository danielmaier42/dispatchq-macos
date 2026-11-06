//go:build unix

package engine

import (
	"os"
	"syscall"
	"testing"
)

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode information on this platform")
	}
	return sys.Ino
}
