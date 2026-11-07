// Package releasetest checks the release asset contract: the installer file
// names dispatchq-<version>-<platform>-<architecture>(-beta).<ending> and the
// check that lets a release publish only when every installer was built. It
// runs the shell scripts on fixture files; nothing is built or published.
package releasetest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const scripts = ".."

func TestWindowsInstallerName(t *testing.T) {
	for _, tc := range []struct{ version, label, want string }{
		{"2.1.0", "x64", "dispatchq-2.1.0-windows-x64-beta.exe"},
		{"2.1.0", "arm64", "dispatchq-2.1.0-windows-arm64-beta.exe"},
		{"dev-pr151-ea313fc", "x64", "dispatchq-dev-pr151-ea313fc-windows-x64-beta.exe"},
	} {
		snippet := `. "` + filepath.Join(scripts, "windows", "arch.sh") + `"` + "\n" + `windows_installer_name "$1" "$2"`
		out, err := exec.Command("bash", "-c", snippet, "name-test", tc.version, tc.label).CombinedOutput()
		if got := strings.TrimSpace(string(out)); err != nil || got != tc.want {
			t.Errorf("windows_installer_name %s %s = %q (%v), want %q", tc.version, tc.label, got, err, tc.want)
		}
	}
}

// artifact writes files into dir/name with a SHA256SUMS listing them, as a
// build job uploads them.
func artifact(t *testing.T, root, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for _, f := range files {
		body := []byte("installer " + f)
		if err := os.WriteFile(filepath.Join(dir, f), body, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		sums.WriteString(hex.EncodeToString(sum[:]) + "  " + f + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// complete lays out the three artifacts of a successful release build.
func complete(t *testing.T, root string) {
	t.Helper()
	artifact(t, root, "release-macos-arm64", "dispatchq-2.1.0-macos-arm64.pkg")
	artifact(t, root, "release-windows-x64", "dispatchq-2.1.0-windows-x64-beta.exe")
	artifact(t, root, "release-windows-arm64", "dispatchq-2.1.0-windows-arm64-beta.exe")
}

func collect(t *testing.T, macArch, artifacts string) (string, []string, error) {
	t.Helper()
	release := filepath.Join(t.TempDir(), "release")
	out, err := exec.Command("bash", filepath.Join(scripts, "release-assets.sh"), "2.1.0", macArch, artifacts, release).CombinedOutput()
	entries, _ := os.ReadDir(release)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return string(out), names, err
}

func TestReleaseAssetsComplete(t *testing.T) {
	root := t.TempDir()
	complete(t, root)
	out, got, err := collect(t, "arm64", root)
	if err != nil {
		t.Fatalf("release-assets.sh failed: %v\n%s", err, out)
	}
	want := []string{
		"dispatchq-2.1.0-macos-arm64.pkg",
		"dispatchq-2.1.0-windows-arm64-beta.exe",
		"dispatchq-2.1.0-windows-x64-beta.exe",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("release holds %v, want %v", got, want)
	}
}

// Anything but exactly the three expected installers stops the release before
// a single file is collected for publishing.
func TestReleaseAssetsRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		macArch string
		layout  func(t *testing.T, root string)
	}{
		{"missing Windows ARM64 build", "arm64", func(t *testing.T, root string) {
			artifact(t, root, "release-macos-arm64", "dispatchq-2.1.0-macos-arm64.pkg")
			artifact(t, root, "release-windows-x64", "dispatchq-2.1.0-windows-x64-beta.exe")
		}},
		{"missing macOS build", "arm64", func(t *testing.T, root string) {
			artifact(t, root, "release-windows-x64", "dispatchq-2.1.0-windows-x64-beta.exe")
			artifact(t, root, "release-windows-arm64", "dispatchq-2.1.0-windows-arm64-beta.exe")
		}},
		{"old Windows name", "arm64", func(t *testing.T, root string) {
			artifact(t, root, "release-macos-arm64", "dispatchq-2.1.0-macos-arm64.pkg")
			artifact(t, root, "release-windows-x64", "DispatchQ-2.1.0-windows-x64-setup.exe")
			artifact(t, root, "release-windows-arm64", "dispatchq-2.1.0-windows-arm64-beta.exe")
		}},
		{"old macOS name", "arm64", func(t *testing.T, root string) {
			artifact(t, root, "release-macos-arm64", "dispatchq-2.1.0.pkg")
			artifact(t, root, "release-windows-x64", "dispatchq-2.1.0-windows-x64-beta.exe")
			artifact(t, root, "release-windows-arm64", "dispatchq-2.1.0-windows-arm64-beta.exe")
		}},
		{"extra file", "arm64", func(t *testing.T, root string) {
			complete(t, root)
			artifact(t, root, "release-extra", "dispatchq-2.1.0.pkg")
		}},
		{"other version", "arm64", func(t *testing.T, root string) {
			artifact(t, root, "release-macos-arm64", "dispatchq-2.1.0-macos-arm64.pkg")
			artifact(t, root, "release-windows-x64", "dispatchq-2.0.9-windows-x64-beta.exe")
			artifact(t, root, "release-windows-arm64", "dispatchq-2.1.0-windows-arm64-beta.exe")
		}},
		{"macOS architecture differs from the verified one", "x64", complete},
		{"unknown macOS architecture", "universal", complete},
		{"checksum mismatch", "arm64", func(t *testing.T, root string) {
			complete(t, root)
			if err := os.WriteFile(filepath.Join(root, "release-windows-x64", "dispatchq-2.1.0-windows-x64-beta.exe"), []byte("tampered"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing checksum hidden by a duplicate", "arm64", func(t *testing.T, root string) {
			complete(t, root)
			if err := os.Remove(filepath.Join(root, "release-windows-arm64", "SHA256SUMS")); err != nil {
				t.Fatal(err)
			}
			list := filepath.Join(root, "release-windows-x64", "SHA256SUMS")
			line, err := os.ReadFile(list)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(list, append(line, line...), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"installer without checksum", "arm64", func(t *testing.T, root string) {
			complete(t, root)
			if err := os.Remove(filepath.Join(root, "release-windows-arm64", "SHA256SUMS")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.layout(t, root)
			out, got, err := collect(t, tc.macArch, root)
			if err == nil {
				t.Fatalf("release-assets.sh accepted it:\n%s", out)
			}
			if len(got) != 0 {
				t.Errorf("collected %v for publishing despite failing", got)
			}
		})
	}
}

// The package and Windows checks refuse an architecture outside the contract
// before they look at any file.
func TestVerifyRejectsUnknownArch(t *testing.T) {
	for _, args := range [][]string{
		{filepath.Join(scripts, "verify-pkg.sh"), "universal", "2.1.0", "abc", t.TempDir()},
		{filepath.Join(scripts, "windows", "verify-build.sh"), "x86", "2.1.0", "abc", t.TempDir()},
	} {
		out, err := exec.Command("bash", args...).CombinedOutput()
		if code := exitCode(err); code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s", args, code, out)
		}
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return -1
}
