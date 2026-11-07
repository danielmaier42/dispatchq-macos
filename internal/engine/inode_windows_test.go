//go:build windows

package engine

import "testing"

// Windows has no inode in os.FileInfo; the tests that compare inodes skip.
func inodeOf(t *testing.T, _ string) uint64 {
	t.Helper()
	t.Skip("no inode information on this platform")
	return 0
}
