// Package binpath holds the platform rules for finding a provider CLI that the
// three adapters share: where a native Windows install puts it, and what counts
// as an executable there.
package binpath

import (
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

// IsExecutableFile reports whether p is a file the OS can run. Unix decides by
// the permission bits; Windows has none and decides by the extension, so a
// stray text file named like the CLI does not pass.
func IsExecutableFile(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return hasWindowsExt(p)
	}
	return fi.Mode()&0o111 != 0
}

func hasWindowsExt(p string) bool {
	ext := filepath.Ext(p)
	for _, e := range WindowsExts {
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
