//go:build windows

package binpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckExecutableOnWindows runs the real rule without any override: an
// install's claude.exe carries no execute bit, and must still pass.
func TestCheckExecutableOnWindows(t *testing.T) {
	dir := t.TempDir()
	for name, want := range map[string]error{
		"claude.exe": nil,
		"claude.cmd": nil,
		"claude.bat": nil,
		"claude.txt": ErrNotExecutable,
		"claude":     ErrNotExecutable,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(p); fi.Mode()&0o111 != 0 {
			t.Fatalf("%s reports execute bits %v; the regression assumes Windows has none", name, fi.Mode())
		}
		if err := CheckExecutable(p); want == nil && err != nil || want != nil && !errors.Is(err, want) {
			t.Errorf("CheckExecutable(%s) = %v, want %v", name, err, want)
		}
	}
	if !errors.Is(CheckExecutable(dir), ErrIsDirectory) {
		t.Error("a directory must be refused")
	}
}
