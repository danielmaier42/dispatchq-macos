// Package binpath holds the platform rules for finding a provider CLI that the
// three adapters share: where a native Windows install puts it, and what counts
// as an executable there.
package binpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WindowsExts are the extensions a CLI install may carry: a native binary, or
// the .cmd shim npm and scoop create for a CLI they installed.
var WindowsExts = []string{".exe", ".cmd"}

// WindowsCandidates lists the places a native Windows install puts the CLI
// called name, in the order they are tried: the CLI's own installer
// directories, the npm global prefix and scoop's shim directory. A bare PATH
// lookup follows them in the adapters; they exist because a daemon started at
// login can see a PATH older than the install. extraHomeDirs are directories
// under the user profile that only this CLI uses (for example ".claude\local").
// appData and home are passed in so the list is testable on any platform.
func WindowsCandidates(name, home, appData, localAppData string, extraHomeDirs ...string) []string {
	var dirs []string
	dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	for _, d := range extraHomeDirs {
		dirs = append(dirs, filepath.Join(home, d))
	}
	if appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm"))
	}
	if localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "Programs", name))
	}
	dirs = append(dirs, filepath.Join(home, "scoop", "shims"), filepath.Join(home, "bin"))

	var out []string
	for _, d := range dirs {
		for _, ext := range WindowsExts {
			out = append(out, filepath.Join(d, name+ext))
		}
	}
	return out
}

// CurrentWindowsCandidates is [WindowsCandidates] for the running user.
func CurrentWindowsCandidates(name string, extraHomeDirs ...string) []string {
	home, _ := os.UserHomeDir()
	return WindowsCandidates(name, home, os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"), extraHomeDirs...)
}

// runnableWindowsExts are the extensions Windows can start as a provider CLI:
// a native binary, or a batch shim that [CommandContext] knows how to start
// without handing an argument to cmd.exe as syntax. Broader than
// [WindowsExts], which only lists what an install is searched for.
var runnableWindowsExts = []string{".exe", ".cmd", ".bat"}

// hostOS is the platform whose rules [CheckExecutable] applies. Only
// [UseOSRulesForTest] changes it.
var hostOS = runtime.GOOS

var (
	// ErrIsDirectory is returned by [CheckExecutable] for a directory.
	ErrIsDirectory = errors.New("it is a directory")
	// ErrNotExecutable is returned by [CheckExecutable] for a file the
	// platform will not run.
	ErrNotExecutable = errors.New("it is not executable")
)

// CheckExecutable reports why p cannot be run as a provider CLI, or nil when
// it can. Unix decides by the permission bits. Windows has none (a file's mode
// never carries an execute bit there) and decides by the extension, so a stray
// text file named like the CLI does not pass. A failed stat is returned as it
// is; callers that show the reason redact it.
func CheckExecutable(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	return checkMode(fi, p, hostOS)
}

func checkMode(fi os.FileInfo, p, goos string) error {
	switch {
	case fi.IsDir():
		return ErrIsDirectory
	case goos == "windows":
		if !hasExt(p, runnableWindowsExts) {
			return fmt.Errorf("%w: Windows runs only .exe, .cmd and .bat files", ErrNotExecutable)
		}
	case fi.Mode()&0o111 == 0:
		return ErrNotExecutable
	}
	return nil
}

// IsExecutableFile reports whether p is a file the OS can run; see
// [CheckExecutable].
func IsExecutableFile(p string) bool { return CheckExecutable(p) == nil }

// UseOSRulesForTest makes [CheckExecutable] apply goos's rules until the
// returned function is called. It lets a test on one platform drive an
// adapter's readiness check through another platform's rules; it must not be
// used outside tests or from parallel tests.
func UseOSRulesForTest(goos string) (restore func()) {
	prev := hostOS
	hostOS = goos
	return func() { hostOS = prev }
}

func hasExt(p string, exts []string) bool {
	ext := filepath.Ext(p)
	for _, e := range exts {
		if equalFold(ext, e) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
