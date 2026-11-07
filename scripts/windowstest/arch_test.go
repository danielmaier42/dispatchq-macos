// Package windowstest checks the architecture handling of the local Windows
// installer build (scripts/build-windows.sh, scripts/windows/). It runs the shell
// helpers and, when makensis is installed, the NSIS preprocessor; nothing is built.
package windowstest

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const scripts = ".."

// sh runs a bash snippet with scripts/windows/arch.sh sourced.
func sh(t *testing.T, snippet string, args ...string) (string, error) {
	t.Helper()
	full := `. "` + filepath.Join(scripts, "windows", "arch.sh") + `"` + "\n" + snippet
	out, err := exec.Command("bash", append([]string{"-c", full, "arch-test"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestWindowsArch(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"amd64", "amd64 x64 x86_64-w64-mingw32 8664"},
		{"x64", "amd64 x64 x86_64-w64-mingw32 8664"},
		{"x86_64", "amd64 x64 x86_64-w64-mingw32 8664"},
		{"arm64", "arm64 arm64 aarch64-w64-mingw32 aa64"},
		{"aarch64", "arm64 arm64 aarch64-w64-mingw32 aa64"},
	} {
		got, err := sh(t, `windows_arch "$1" && echo "$WIN_GOARCH $WIN_LABEL $WIN_TRIPLE $WIN_MACHINE"`, tc.in)
		if err != nil || got != tc.want {
			t.Errorf("windows_arch %s = %q (%v), want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "x86", "386", "ARM64", "arm"} {
		if got, err := sh(t, `windows_arch "$1"`, bad); err == nil {
			t.Errorf("windows_arch %q succeeded (%q), want failure", bad, got)
		}
	}
}

// writePE writes a minimal file with a DOS stub pointing at a COFF header.
func writePE(t *testing.T, path string, machine uint16, sig string) {
	t.Helper()
	b := make([]byte, 0x90)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	copy(b[0x80:], sig)
	binary.LittleEndian.PutUint16(b[0x84:], machine)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPEMachine(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		machine uint16
		sig     string
		want    string
		ok      bool
	}{
		"x64.exe":   {0x8664, "PE\x00\x00", "8664", true},
		"arm64.exe": {0xaa64, "PE\x00\x00", "aa64", true},
		"x86.exe":   {0x14c, "PE\x00\x00", "14c", true},
		"nope.exe":  {0xaa64, "NE\x00\x00", "", false},
	} {
		path := filepath.Join(dir, name)
		writePE(t, path, tc.machine, tc.sig)
		got, err := sh(t, `pe_machine "$1"`, path)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("pe_machine %s = %q (%v), want %q ok=%v", name, got, err, tc.want, tc.ok)
		}
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := sh(t, `pe_machine "$1"`, short); err == nil {
		t.Errorf("pe_machine on a truncated file = %q, want failure", got)
	}
}

// An unknown architecture stops the build before it touches anything.
func TestBuildRejectsUnknownArch(t *testing.T) {
	for _, args := range [][]string{{"--arch", "x86"}, {"--arch=ia64"}, {"--arch"}, {"--bogus"}} {
		cmd := exec.Command("bash", append([]string{filepath.Join(scripts, "build-windows.sh")}, args...)...)
		cmd.Env = append(os.Environ(), "DISPATCHQ_WINDOWS_ARCH=")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Errorf("build-windows.sh %v: err %v, want exit 2; output %s", args, err, out)
		}
		if strings.Contains(string(out), "==>") {
			t.Errorf("build-windows.sh %v started building: %s", args, out)
		}
	}
}

// The installer refuses the wrong machine for its payload and never installs the
// first-start notes.
func TestInstallerArchGuard(t *testing.T) {
	if _, err := exec.LookPath("makensis"); err != nil {
		t.Skip("makensis not installed")
	}
	nsi := filepath.Join(scripts, "windows", "dispatchq.nsi")
	ppo := func(arch string) (string, error) {
		out, err := exec.Command("makensis", "-PPO", "-INPUTCHARSET", "UTF8", "-DBOOTSTRAPPER",
			"-DVERSION=test", "-DARCH="+arch, "-DSTAGE=stage", "-DOUTFILE=out.exe", nsi).CombinedOutput()
		return string(out), err
	}
	const armOnly, needs64 = "This DispatchQ installer is for Windows on ARM", "DispatchQ needs 64-bit Windows"
	for arch, want := range map[string][2]string{
		"arm64": {armOnly, needs64},
		"x64":   {needs64, armOnly},
	} {
		out, err := ppo(arch)
		if err != nil {
			t.Fatalf("makensis -PPO ARCH=%s: %v\n%s", arch, err, out)
		}
		if !strings.Contains(out, want[0]) || strings.Contains(out, want[1]) {
			t.Errorf("ARCH=%s: want guard %q and not %q", arch, want[0], want[1])
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "INSTALL-WINDOWS") && !strings.HasPrefix(strings.TrimSpace(line), "Delete ") {
				t.Errorf("ARCH=%s installs the first-start notes: %s", arch, line)
			}
		}
	}
	if out, err := ppo("x86"); err == nil || !strings.Contains(out, "ARCH must be x64 or arm64") {
		t.Errorf("ARCH=x86 was accepted: %v\n%s", err, out)
	}
}
