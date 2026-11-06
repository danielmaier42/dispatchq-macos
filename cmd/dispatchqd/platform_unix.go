//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/launchd"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/uninstall"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/wake"
)

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ""

// platformInit runs before anything else; nothing is needed on macOS.
func platformInit(_ []string) {}

// platformWaker schedules wake-from-sleep through pmset.
func platformWaker() *wake.Scheduler { return &wake.Scheduler{Runner: system.Real{}, Sudo: true} }

// platformUpdates is the GitHub release checker that offers the .pkg installer.
func platformUpdates() *update.Service {
	return update.NewService(update.GitHubFetcher{}, update.DefaultInterval)
}

// platformNotifiers is the native macOS notification channel.
func platformNotifiers() []notify.Notifier {
	return []notify.Notifier{notify.Mac{Runner: system.Real{}}}
}

// platformDeps wires the macOS dialogs, Terminal hand-off and uninstaller.
func platformDeps(d *api.Deps) {
	d.ChooseFolder = api.OSAScriptFolderChooser(system.Real{})
	d.SaveFile = api.OSAScriptSaveFileDialog(system.Real{})
	d.OpenTerminal = api.OSAScriptTerminalOpener(system.Real{})
	d.NotifyStatus = notify.MacAuthorization
	d.Uninstall = openUninstaller
}

// strayDaemonPatterns match a running daemon: this one, and claudeqd from
// before the rename to DispatchQ.
var strayDaemonPatterns = []string{"dispatchqd run", "claudeqd run"}

func cmdInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	agentsDir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	// ClaudeQ's agent and daemon go first. launchd would restart a daemon its
	// job still owns, and it would run the same queue next to this one; and
	// DefaultHome below moves ClaudeQ's data, which its daemon must no longer
	// be writing to.
	legacy := launchd.Agent{Runner: system.Real{}, Dir: agentsDir, Label: launchd.LegacyLabel, UID: os.Getuid()}
	if err := legacy.Uninstall(context.Background()); err != nil {
		return fmt.Errorf("remove ClaudeQ LaunchAgent: %w", err)
	}
	killStrayDaemons()

	home, err := store.DefaultHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	cfg := launchd.Config{
		Label:      launchd.DefaultLabel,
		BinPath:    self,
		Args:       []string{"run"},
		StdoutPath: filepath.Join(home, "dispatchqd.out.log"),
		StderrPath: filepath.Join(home, "dispatchqd.err.log"),
	}
	// When running from inside the app bundle, tie the agent to it so Login Items
	// shows the DispatchQ name and icon instead of a bare "dispatchqd".
	if strings.Contains(self, ".app/Contents/MacOS/") {
		cfg.AssociatedBundleID = launchd.DefaultLabel // == the app bundle id
	}
	plist, err := launchd.Plist(cfg)
	if err != nil {
		return err
	}

	// Stop any stray daemon still holding the loopback port (an orphan from a
	// prior run/version that launchd's bootout won't catch) so the freshly
	// bootstrapped one can bind and actually take over — otherwise an update
	// wouldn't take effect until a reboot.
	killStrayDaemons()

	agent := launchd.Agent{Runner: system.Real{}, Dir: agentsDir, Label: launchd.DefaultLabel, UID: os.Getuid()}
	if err := agent.Install(context.Background(), plist); err != nil {
		return err
	}

	fmt.Printf("installed LaunchAgent %s (%s)\n", launchd.DefaultLabel, agent2path(agentsDir))
	fmt.Println("dispatchqd will now start at login and restart on exit.")
	fmt.Println()
	fmt.Println("To enable wake-from-sleep, pmset must run as root. Add a sudoers entry once:")
	fmt.Printf("  echo '%s ALL=(root) NOPASSWD: /usr/bin/pmset' | sudo tee /etc/sudoers.d/dispatchq\n", currentUser())
	return nil
}

// killStrayDaemons terminates any running `dispatchqd run` process and waits for
// the loopback port to come free, so the daemon the caller is about to
// bootstrap can actually bind it. The current process is `dispatchqd install`, so
// the pattern never matches itself.
//
// Waiting on the port rather than on a fixed delay matters: a daemon that keeps
// the port keeps serving its own (older) version, which is what makes an update
// look like it did nothing.
func killStrayDaemons() {
	for _, p := range strayDaemonPatterns {
		_ = exec.Command("pkill", "-f", p).Run()
	}
	if waitPortFree(3 * time.Second) {
		return
	}
	// Still held — a daemon that ignored SIGTERM, or one started from another
	// copy of the app. Escalate rather than bootstrapping into a taken port.
	for _, p := range strayDaemonPatterns {
		_ = exec.Command("pkill", "-9", "-f", p).Run()
	}
	_ = waitPortFree(2 * time.Second)
}

func cmdUninstall() error {
	agentsDir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	agent := launchd.Agent{Runner: system.Real{}, Dir: agentsDir, Label: launchd.DefaultLabel, UID: os.Getuid()}
	if err := agent.Uninstall(context.Background()); err != nil {
		return err
	}
	fmt.Printf("removed LaunchAgent %s\n", launchd.DefaultLabel)
	cli := resolveQueueBin()
	if cli == "" {
		cli = filepath.Join(update.DefaultAppPath, "Contents", "MacOS", "dispatchq")
	}
	fmt.Printf("To remove the app, the wake permission and the installer receipt as well, run: %s uninstall\n", cli)
	return nil
}

// openUninstaller opens the uninstaller of the app bundle this daemon runs
// from in the macOS Installer (Settings > System > Uninstall).
func openUninstaller(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return uninstall.Open(ctx, system.Real{}, self)
}

func launchAgentsDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(h, "Library", "LaunchAgents"), nil
}

func agent2path(dir string) string {
	return filepath.Join(dir, launchd.DefaultLabel+".plist")
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "<your-username>"
}

// osVersion reports the macOS product version (e.g. "15.6") for the feedback
// issue's environment line. It is asked once and cached: the version cannot
// change while the daemon runs, and an unreadable one simply stays empty.
func osVersion(r system.Runner) func() string {
	var once sync.Once
	var cached string
	return func() string {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := r.Run(ctx, "sw_vers", "-productVersion")
			if err != nil {
				return
			}
			cached = strings.TrimSpace(string(out))
		})
		return cached
	}
}
