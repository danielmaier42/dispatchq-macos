package opencode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider/binpath"
)

// DetectBinary finds the opencode CLI. It returns an absolute path, or "" if
// it cannot be located.
//
// It exists for the same reason the Claude Code and Codex ones do: dispatchq's
// daemon and app run under launchd with a minimal PATH that excludes the usual
// CLI install locations, so a plain `opencode` lookup fails even when the CLI
// works fine in the operator's terminal. The common locations are checked
// directly, then the user's login shell is asked (which sources their profile
// and rebuilds the real PATH).
func DetectBinary() string {
	// An explicit override wins — used by CI/tests and by an installation in
	// an unusual place.
	if p := os.Getenv("DISPATCHQ_OPENCODE_BIN"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, c := range installCandidates(home) {
		if isExecutableFile(c) {
			return c
		}
	}
	if p := viaLoginShell(); p != "" {
		return p
	}
	if p, err := exec.LookPath(BinaryName); err == nil {
		return p
	}
	return ""
}

// installCandidates lists the common install locations of the CLI, most likely
// first. Native Windows installs live in other places than the Unix ones.
func installCandidates(home string) []string {
	if runtime.GOOS == "windows" {
		return binpath.CurrentWindowsCandidates("opencode")
	}
	return []string{
		"/opt/homebrew/bin/opencode",
		"/usr/local/bin/opencode",
		filepath.Join(home, ".local", "bin", "opencode"),
		filepath.Join(home, "bin", "opencode"),
	}
}

// loginShellTimeout bounds the profile-sourcing probe. A shell profile is
// arbitrary code; one that blocks must not hold up whoever asked where the CLI
// is.
const loginShellTimeout = 5 * time.Second

// viaLoginShell asks the user's login+interactive shell to resolve `opencode`,
// so PATH additions in their profile are honoured.
func viaLoginShell() string {
	if runtime.GOOS == "windows" {
		return "" // no POSIX login shell; the PATH lookup covers the profile
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginShellTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-ilc", "command -v "+BinaryName)
	// Without this, a grandchild holding the output pipe open would keep Output
	// waiting even after the shell itself was killed.
	cmd.WaitDelay = time.Second
	// The output is scanned even when the wait failed: a slow profile can trip
	// the timeout after the shell already printed the answer, and throwing that
	// away would leave the daemon believing the CLI is not installed.
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		p := strings.TrimSpace(lines[i])
		if filepath.IsAbs(p) && isExecutableFile(p) {
			return p
		}
	}
	return ""
}

func isExecutableFile(p string) bool { return binpath.IsExecutableFile(p) }
