package binpath

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWindowsCandidatesCoverNativeInstallLocations(t *testing.T) {
	got := WindowsCandidates("claude", `C:\Users\dm`, `C:\Users\dm\AppData\Roaming`, `C:\Users\dm\AppData\Local`, ".claude/local")
	for _, want := range []string{
		filepath.Join(`C:\Users\dm`, ".local", "bin", "claude.exe"),
		filepath.Join(`C:\Users\dm`, ".claude/local", "claude.exe"),
		filepath.Join(`C:\Users\dm\AppData\Roaming`, "npm", "claude.cmd"),
		filepath.Join(`C:\Users\dm\AppData\Local`, "Programs", "claude", "claude.exe"),
		filepath.Join(`C:\Users\dm`, "scoop", "shims", "claude.exe"),
	} {
		if !slices.Contains(got, want) {
			t.Errorf("candidates lack %q:\n%v", want, got)
		}
	}
	for _, c := range got {
		if ext := filepath.Ext(c); ext != ".exe" && ext != ".cmd" {
			t.Errorf("candidate %q has no Windows executable extension", c)
		}
	}
}

func TestWindowsCandidatesSkipUnsetEnvironment(t *testing.T) {
	for _, c := range WindowsCandidates("codex", "home", "", "") {
		if filepath.Base(filepath.Dir(c)) == "npm" {
			t.Errorf("npm location %q listed without APPDATA", c)
		}
	}
}

func TestIsExecutableFile(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "claude")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "claude.EXE")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsExecutableFile(dir) {
		t.Error("a directory is not an executable")
	}
	if IsExecutableFile(filepath.Join(dir, "missing")) {
		t.Error("a missing file is not an executable")
	}
	if !IsExecutableFile(exe) {
		t.Error("an executable file (any case of .exe) must pass")
	}
	// Unix decides by permission bits, Windows by extension: neither accepts this.
	if IsExecutableFile(plain) {
		t.Error("an extensionless file without execute permission must not pass")
	}
}

func TestHasWindowsExtIgnoresCase(t *testing.T) {
	for p, want := range map[string]bool{"a.exe": true, "a.CMD": true, "a.Exe": true, "a.txt": false, "a": false, "a.bat": false} {
		if hasWindowsExt(p) != want {
			t.Errorf("hasWindowsExt(%q) = %v, want %v", p, !want, want)
		}
	}
}
