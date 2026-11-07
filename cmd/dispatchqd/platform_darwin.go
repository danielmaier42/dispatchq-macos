package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/fileaccess"
	"github.com/danielmaier42/claudeq/internal/launchd"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/uninstall"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/wake"
)

// newPlatform wires the macOS services: launchd, pmset, caffeinate, the
// Notification Center, and the osascript dialogs.
func newPlatform(r system.Runner) platform {
	return platform{
		service: launchdService{runner: r},
		// pmset needs root. `dispatchqd install` explains the sudoers entry.
		waker:             &wake.Scheduler{Runner: r, Sudo: true},
		sleep:             system.DefaultSleepInhibitor(),
		desktop:           notify.Mac{Runner: r},
		requestNotifyAuth: notify.RequestMacAuthorization,
		notifyStatus:      notify.MacAuthorization,
		chooseFolder:      api.OSAScriptFolderChooser(r),
		saveFile:          api.OSAScriptSaveFileDialog(r),
		openTerminal:      api.OSAScriptTerminalOpener(r),
		warmFileAccess:    warmFileAccess,
		osVersion:         osVersion(r),
		uninstall:         openUninstaller(r),
		stopStrayDaemons:  func() error { killStrayDaemons(); return nil },
		openInstaller:     openPackage(r),
	}
}

// openPackage hands a downloaded installer package to macOS, which opens it in
// the Installer app for the operator to confirm.
func openPackage(r system.Runner) func(ctx context.Context, path string) error {
	return func(ctx context.Context, path string) error {
		if out, err := r.Run(ctx, "open", path); err != nil {
			return fmt.Errorf("open %s: %w: %s", path, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ""

// platformInit runs before anything else; nothing is needed on macOS.
func platformInit(_ []string) {}

// strayDaemonPatterns match a running daemon: this one, and claudeqd from
// before the rename to DispatchQ.
var strayDaemonPatterns = []string{"dispatchqd run", "claudeqd run"}

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
	// Still held: a daemon that ignored SIGTERM, or one started from another
	// copy of the app. Escalate rather than bootstrapping into a taken port.
	for _, p := range strayDaemonPatterns {
		_ = exec.Command("pkill", "-9", "-f", p).Run()
	}
	_ = waitPortFree(2 * time.Second)
}

// launchdService installs the daemon as a user LaunchAgent.
type launchdService struct {
	runner system.Runner
}

func (s launchdService) agent(label string) (launchd.Agent, error) {
	dir, err := launchAgentsDir()
	if err != nil {
		return launchd.Agent{}, err
	}
	return launchd.Agent{Runner: s.runner, Dir: dir, Label: label, UID: os.Getuid()}, nil
}

// RetireLegacy boots out ClaudeQ's agent. launchd would restart a daemon its
// job still owns, and it would run the same queue next to this one.
func (s launchdService) RetireLegacy(ctx context.Context) error {
	legacy, err := s.agent(launchd.LegacyLabel)
	if err != nil {
		return err
	}
	if err := legacy.Uninstall(ctx); err != nil {
		return fmt.Errorf("remove ClaudeQ LaunchAgent: %w", err)
	}
	return nil
}

func (s launchdService) Install(ctx context.Context, spec serviceSpec, out io.Writer) error {
	cfg := launchd.Config{
		Label:      launchd.DefaultLabel,
		BinPath:    spec.Bin,
		Args:       spec.Args,
		StdoutPath: spec.StdoutPath,
		StderrPath: spec.StderrPath,
	}
	// When running from inside the app bundle, tie the agent to it so Login Items
	// shows the DispatchQ name and icon instead of a bare "dispatchqd".
	if strings.Contains(spec.Bin, ".app/Contents/MacOS/") {
		cfg.AssociatedBundleID = launchd.DefaultLabel // == the app bundle id
	}
	plist, err := launchd.Plist(cfg)
	if err != nil {
		return err
	}
	agent, err := s.agent(launchd.DefaultLabel)
	if err != nil {
		return err
	}
	if err := agent.Install(ctx, plist); err != nil {
		return err
	}

	fmt.Fprintf(out, "installed LaunchAgent %s (%s)\n", launchd.DefaultLabel, filepath.Join(agent.Dir, launchd.DefaultLabel+".plist"))
	fmt.Fprintln(out, "dispatchqd will now start at login and restart on exit.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "To enable wake-from-sleep, pmset must run as root. Add a sudoers entry once:")
	fmt.Fprintf(out, "  echo '%s ALL=(root) NOPASSWD: /usr/bin/pmset' | sudo tee /etc/sudoers.d/dispatchq\n", currentUser())
	return nil
}

func (s launchdService) Uninstall(ctx context.Context, out io.Writer) error {
	agent, err := s.agent(launchd.DefaultLabel)
	if err != nil {
		return err
	}
	if err := agent.Uninstall(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed LaunchAgent %s\n", launchd.DefaultLabel)
	cli := resolveQueueBin()
	if cli == "" {
		cli = filepath.Join(update.DefaultAppPath, "Contents", "MacOS", "dispatchq")
	}
	fmt.Fprintf(out, "To remove the app, the wake permission and the installer receipt as well, run: %s uninstall\n", cli)
	return nil
}

func launchAgentsDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(h, "Library", "LaunchAgents"), nil
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "<your-username>"
}

// openUninstaller opens the uninstaller of the app bundle this daemon runs
// from in the macOS Installer (Settings > System > Uninstall).
func openUninstaller(r system.Runner) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate executable: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		return uninstall.Open(ctx, r, self)
	}
}

// warmFileAccess reads the given directories so macOS raises its file-access
// consent prompt while the user is present (see call sites for why). It is
// best-effort and bounded: a directory stuck behind an unanswered prompt cannot
// wedge it, and it never blocks the scheduler. If access is already granted (the
// common case) it is a silent no-op. Folders are first collapsed to their macOS
// privacy category (ConsentTargets), so many tasks under ~/Documents provoke the
// Documents prompt once rather than one blocked read each; it then probes every
// remaining target (ProbeAll, not the first-block Probe) so distinct categories
// each get their prompt in one pass. Wired into the API as Deps.WarmFileAccess so
// a folder added at runtime is warmed the moment its task is created or edited.
func warmFileAccess(dirs []string) {
	home, _ := os.UserHomeDir()
	targets := fileaccess.ConsentTargets(dirs, home)
	for _, res := range fileaccess.ProbeAll(targets, fileaccess.DefaultProbeTimeout) {
		switch res.Reason {
		case fileaccess.ReasonTimeout:
			// The read didn't return in time — during warming this almost always
			// means macOS is now showing its consent prompt for the folder, which is
			// exactly what we wanted. Nothing to fix; the user just clicks Allow.
			fmt.Fprintf(os.Stdout, "dispatchqd: prompting for file access to %q "+
				"(answer the macOS dialog to allow it)\n", res.BlockedPath)
		default:
			// Genuinely denied — point the user at where they can grant it.
			fmt.Fprintf(os.Stderr, "dispatchqd: file access to %q is blocked (%s); allow it in "+
				"System Settings > Privacy & Security > Files and Folders (or Full Disk Access)\n",
				res.BlockedPath, res.Reason)
		}
	}
}

// osVersion names the system and its product version (e.g. "macOS 15.6") for
// the feedback issue's environment line. It is asked once and cached: the version cannot
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
			if v := strings.TrimSpace(string(out)); v != "" {
				cached = "macOS " + v
			}
		})
		return cached
	}
}
