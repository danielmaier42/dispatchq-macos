package claudecode

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

// DetectBinary finds the Claude Code CLI. It returns an absolute path, or ""
// if it cannot be located.
//
// This exists because dispatchq's daemon and app run under launchd with a minimal
// PATH (/usr/bin:/bin:…) that excludes the usual CLI install locations like
// ~/.local/bin — so a plain `claude` lookup fails even when the CLI is
// installed and works from the user's terminal. We therefore check the common
// install locations directly, then fall back to the user's login shell (which
// sources their profile and reconstructs the real PATH).
func DetectBinary() string {
	// An explicit env override wins — used by CI/tests and power users whose
	// claude lives somewhere unusual.
	if p := os.Getenv("DISPATCHQ_CLAUDE_BIN"); p != "" {
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
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return ""
}

// installCandidates lists the common install locations of the CLI, most likely
// first. Native Windows installs live in other places than the Unix ones.
func installCandidates(home string) []string {
	if runtime.GOOS == "windows" {
		return binpath.CurrentWindowsCandidates("claude", ".claude\\local")
	}
	return []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".claude", "local", "claude"),
		"/opt/homebrew/bin/claude",
		"/usr/local/bin/claude",
		filepath.Join(home, "bin", "claude"),
	}
}

// loginShellTimeout bounds the profile-sourcing probe below. A shell profile is
// arbitrary code; an interactive one that blocks (a prompt, a slow network
// call) must not hold up whoever asked where the CLI is.
const loginShellTimeout = 5 * time.Second

// viaLoginShell asks the user's login+interactive shell to resolve `claude`, so
// PATH additions in their profile (~/.zprofile, ~/.zshrc, …) are honoured.
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
	// -i -l so both login and interactive profiles are sourced (~/.local/bin is
	// commonly added in ~/.zshrc, which only an interactive shell reads).
	cmd := exec.CommandContext(ctx, shell, "-ilc", "command -v claude")
	// Without this, a grandchild holding the output pipe open would keep Output
	// waiting even after the shell itself was killed.
	cmd.WaitDelay = time.Second
	// The output is scanned even when the wait failed: a slow profile can trip
	// the timeout, and a background grandchild holding stdout open trips
	// WaitDelay, after the shell already printed the answer. Throwing that away
	// would leave the daemon believing the CLI is not installed.
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	// A login/interactive shell may print profile noise; take the last line that
	// is an absolute path to an existing executable.
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
