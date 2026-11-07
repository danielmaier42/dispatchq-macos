// Command dispatchqd is the dispatchq background daemon: it runs the scheduling
// loop that executes due tasks via the Claude Code CLI, and can install itself
// as a launchd LaunchAgent (PLAN.md build phases 2–3).
//
// Usage:
//
//	dispatchqd --version
//	dispatchqd run [--interval 5s]
//	dispatchqd install     # install & start the LaunchAgent (autostart)
//	dispatchqd uninstall    # stop & remove the LaunchAgent
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/aside"
	"github.com/danielmaier42/claudeq/internal/assistant"
	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/engine"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/feedback"
	"github.com/danielmaier42/claudeq/internal/fileaccess"
	"github.com/danielmaier42/claudeq/internal/launchd"
	"github.com/danielmaier42/claudeq/internal/limit"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/adapters"
	"github.com/danielmaier42/claudeq/internal/review"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/uninstall"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
	"github.com/danielmaier42/claudeq/internal/wake"
)

// defaultAddr is the loopback address the dashboard/API listens on. The
// LaunchAgent starts the daemon without flags, so this is the address in
// practice — and the one an old daemon has to release before a new one can
// take over.
const defaultAddr = "127.0.0.1:10765"

// daemonLockWait is how long a starting daemon waits for a predecessor to let go
// of the data directory. It covers the hand-over during an update, where the new
// daemon is bootstrapped while the old one is still shutting down, so it has to
// outlast that shutdown: the outgoing daemon lets in-flight runs finish for up
// to engine.ShutdownGrace, then closes the HTTP server, and releases the lock
// last of all.
const daemonLockWait = engine.ShutdownGrace + 20*time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: dispatchqd run [--interval 5s] | install | uninstall | --version")
		return nil
	}
	switch args[0] {
	case "--version", "-version":
		fmt.Println(version.String())
		return nil
	case "run":
		return cmdRun(args[1:])
	case "install":
		return cmdInstall()
	case "uninstall":
		return cmdUninstall()
	default:
		fmt.Println("usage: dispatchqd run [--interval 5s] | install | uninstall | --version")
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	interval := fs.Duration("interval", 5*time.Second, "scheduler tick interval")
	noWake := fs.Bool("no-wake", false, "do not schedule pmset wakes")
	addr := fs.String("addr", defaultAddr, "dashboard/API listen address (loopback only)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	home, err := store.DefaultHome()
	if err != nil {
		return err
	}
	st, err := store.Open(home)
	if err != nil {
		return err
	}

	// One daemon per data directory, and this is where that is enforced — before
	// anything is written. Both launchd and the app window start a daemon, and
	// the window used to add one whenever its probe of the running daemon timed
	// out; the extra process lost the dashboard port but kept scheduling, so
	// every due task ran twice and each start wrote off the other's in-flight
	// run as interrupted.
	lock, err := st.LockDaemon(daemonLockWait)
	if err != nil {
		var running *store.ErrDaemonRunning
		if errors.As(err, &running) {
			return fmt.Errorf("%w; not starting a second daemon on the same store "+
				"(stop the running one, or set DISPATCHQ_HOME to another directory)", running)
		}
		return err
	}
	defer func() { _ = lock.Close() }()

	if err := st.PrimeWhatsNew(api.WhatsNewEdition); err != nil {
		return err
	}

	// A fresh process means nothing is actually running: mark any run left as
	// "running" (from a crash/power loss) as interrupted, and prune old history.
	if n, err := st.ReconcileRunningRuns(time.Now()); err == nil && n > 0 {
		fmt.Fprintf(os.Stdout, "dispatchqd: reconciled %d interrupted run(s)\n", n)
	}
	if cfg, err := st.LoadConfig(); err == nil {
		_ = st.PruneHistory(cfg.Settings.RunHistoryLimit())
		// Say it out loud: a daemon that starts nothing all night looks broken
		// otherwise, and the log is the only place to look.
		if cfg.Settings.Paused {
			fmt.Fprintln(os.Stdout, "dispatchqd: all runs are paused (Settings → Pause all runs); nothing will start")
		}
	}
	// Rewrite a config left over from an older version, so the file on disk says
	// what the daemon does (e.g. the retired global skip-permissions default,
	// which every task that relied on it now carries itself).
	if migrated, err := st.MigrateConfig(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd: migrate config:", err)
	} else if migrated {
		fmt.Fprintln(os.Stdout, "dispatchqd: migrated config.toml to the current format")
	}
	// Installs predating the last-run bookkeeping have it only in history.
	if _, err := st.BackfillLastRuns(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd: backfill last runs:", err)
	}

	// The adapter registry is what the engine runs jobs through; every harness
	// dispatchq supports contributes one adapter to it and nothing else changes.
	// The checker built on it answers whether a configured instance can run a
	// job, and is shared with the API so the dashboard's polling costs no extra
	// processes.
	registry := adapters.Default()
	// Runs from before dispatchq kept the model the harness reported get it
	// read back out of their logs, so usage splits by the model that ran.
	if _, err := provider.BackfillReportedModels(st, registry); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd: backfill reported models:", err)
	}
	checker := provider.NewChecker(registry)

	c := clock.Real{}
	eng := engine.New(st, limit.NewGates(c), &executor.Executor{
		Registry: registry,
		Home:     home,
		QueueBin: resolveQueueBin(),
	}, c, checker)
	if !*noWake {
		eng.SetWaker(&wake.Scheduler{Runner: system.Real{}, Sudo: true})
	}
	eng.SetNotifier(liveNotifier{st: st})
	// Each provider's allowance is read without spending any of it: on a
	// timer, when the dashboard asks, and whenever a run on that account ends.
	limits := provider.NewLimitMonitor(registry, func() ([]provider.Instance, error) {
		set, err := app.Providers(st)
		if err != nil {
			return nil, err
		}
		return set.All(), nil
	})
	eng.SetLimits(func(_ context.Context, inst provider.Instance) (provider.Limits, bool) {
		return limits.Cached(inst)
	})
	eng.SetRunFinished(func(providerID string) {
		go func() {
			if err := limits.RefreshID(context.Background(), providerID); err != nil {
				fmt.Fprintln(os.Stderr, "dispatchqd: provider limits:", err)
			}
		}()
	})
	// Ask for notification permission up front (only does anything when running
	// from the app bundle) so run-outcome notifications carry the app icon.
	notify.RequestMacAuthorization()
	// Provoke the macOS file-access (TCC) consent prompt now, at startup — which
	// happens at install and at every login, while the user is present — rather
	// than mid-run overnight, where the prompt has no one to answer it and the
	// run stalls. Reading each task folder is what makes macOS raise the prompt;
	// once answered the decision sticks and this becomes a no-op. Folders added
	// later are warmed per-task from the API (Deps.WarmFileAccess) at add/edit.
	go warmEnabledTasks(st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background update checker: polls GitHub for a newer release hourly and
	// caches the result so the dashboard's "update available" banner and the
	// manual "Check for updates" button answer instantly (see internal/update).
	updSvc := update.NewService(update.GitHubFetcher{}, update.DefaultInterval)
	go updSvc.Run(ctx)
	go limits.Run(ctx, provider.DefaultLimitsInterval, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "dispatchqd: "+format+"\n", args...)
	})

	// Prompt review, the assistant chat and the feedback assistant ask a
	// harness questions of dispatchq's own (see internal/aside); all go through
	// the one runner, so they cannot drift apart in how they invoke a provider.
	asides := &aside.Runner{Registry: registry}
	osVer := osVersion(system.Real{})
	// The assistant runs the CLI that ships next to this daemon, the same one
	// every run gets as DISPATCHQ_BIN, and tells the model where it is.
	cliBin := resolveQueueBin()
	cli := assistant.ExecCLI{Bin: cliBin}
	rt := assistant.PlatformRuntime(runtime.GOOS, osVer())
	rt.Version, rt.CLIPath, rt.DataDir = version.String(), cliBin, st.Home()

	httpSrv := &http.Server{
		Addr: *addr,
		Handler: api.Handler(api.Deps{
			Store: st, Runner: eng, Canceler: eng, TakeOver: eng,
			ChooseFolder: api.OSAScriptFolderChooser(system.Real{}), ActiveTasks: eng.ActiveTaskIDs,
			SaveFile:     api.OSAScriptSaveFileDialog(system.Real{}),
			OpenTerminal: api.OSAScriptTerminalOpener(system.Real{}),
			WakeError:    eng.WakeError, WarmFileAccess: warmFileAccess, Updates: updSvc,
			LimitedUntil: eng.LimitedUntil, BlockedProviders: eng.BlockedProviders, BlockReasons: eng.BlockReasons,
			Holds: eng.Holds, RunningOn: eng.RunningOn,
			NotifyStatus: notify.MacAuthorization,
			Feedback:     feedback.New(asides), OSVersion: osVer,
			Assistant: assistant.New(asides, &assistant.StoreSource{Store: st, CLI: cli}, cli, rt),
			Review:    &review.Reviewer{Ask: asides},
			Registry:  registry, Providers: checker, Limits: limits,
			Uninstall: openUninstaller,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "dispatchqd: http server:", err)
		}
	}()
	// Report every configured provider's state: an unattended queue that starts
	// nothing all night looks broken otherwise, and the daemon log is the only
	// place to look. It happens *after* the dashboard is being served, because
	// every verdict here spawns a CLI — and the app window waits for the daemon
	// to answer before it opens, not for the harnesses to introduce themselves.
	go reportProviderHealth(st, checker)
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	fmt.Printf("dispatchqd %s: watching %s\n  dashboard: http://%s  (tick %s, wake %t)\n",
		version.String(), home, *addr, *interval, !*noWake)
	if err := eng.Loop(ctx, *interval); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	fmt.Println("dispatchqd: stopped")
	return nil
}

// reportProviderHealth checks every configured provider once at startup and
// writes the verdict to the daemon log, and returns the Claude Code instance's
// binary for the parts of the app that still ask for one directly. A daemon
// that starts nothing all night because a CLI is missing or logged out has to
// say so somewhere, and the log is where the operator looks.
func reportProviderHealth(st *store.Store, checker *provider.Checker) {
	cfg, err := st.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd: read provider configuration:", err)
		return
	}
	set, err := provider.FromConfig(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd: read provider configuration:", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, s := range checker.Statuses(ctx, set) {
		if s.Health.Ready() {
			fmt.Fprintf(os.Stdout, "dispatchqd: provider %q ready (%s)\n", s.Instance.ID, s.Health.Binary)
		} else {
			fmt.Fprintf(os.Stderr, "dispatchqd: provider %q is %s: %s\n", s.Instance.ID, s.Health.State, s.Health.Reason)
		}
	}
}

// liveNotifier is the daemon's notification fan-out. It reads the settings on
// every send instead of once at startup, so a channel switched on (or a token
// corrected) in Settings takes effect with the next notification rather than
// after the next restart.
type liveNotifier struct{ st *store.Store }

// Notify delivers to every channel that is switched on and configured. A
// config that fails to load is reported rather than swallowed: silently
// falling back to a zero Settings would drop every remote channel from this
// send (and every send after, until the file is fixed) with no trace of why.
func (l liveNotifier) Notify(ctx context.Context, n notify.Notification) error {
	cfg, err := l.st.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: notify: load config: %v (falling back to macOS-only)\n", err)
	}
	return notify.Multi{Notifiers: notifyChannels(cfg.Settings)}.Notify(ctx, n)
}

// notifyChannels assembles the notification channels: native macOS always, plus
// every remote channel that is both switched on and configured (FA-39/40/41).
// A channel switched on but left half-filled in is skipped rather than attempted
// and logged as an error on every single notification.
func notifyChannels(s store.Settings) []notify.Notifier {
	notifiers := []notify.Notifier{notify.Mac{Runner: system.Real{}}}
	if po := (notify.Pushover{Token: s.Pushover.Token, UserKey: s.Pushover.UserKey}); s.Pushover.Enabled && po.Configured() {
		notifiers = append(notifiers, po)
	}
	if nt := (notify.Ntfy{Server: s.Ntfy.Server, Topic: s.Ntfy.Topic, Token: s.Ntfy.Token}); s.Ntfy.Enabled && nt.Configured() {
		notifiers = append(notifiers, nt)
	}
	if wh := (notify.Webhook{URL: s.Webhook.URL, Template: s.Webhook.Template}); s.Webhook.Enabled && wh.Configured() {
		notifiers = append(notifiers, wh)
	}
	return notifiers
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

// warmEnabledTasks provokes the prompt for every enabled task's folder at
// startup (install time and each login, while the user is present).
func warmEnabledTasks(st *store.Store) {
	// Let the login/session settle so the prompt actually surfaces rather than
	// being lost in login-time churn.
	time.Sleep(2 * time.Second)
	cfg, err := st.LoadConfig()
	if err != nil {
		return
	}
	dirs := make([]string, 0, len(cfg.Tasks))
	for _, t := range cfg.Tasks {
		if t.Enabled {
			dirs = append(dirs, t.WorkingDir)
		}
	}
	warmFileAccess(dirs)
}

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
	// Still held — a daemon that ignored SIGTERM, or one started from another
	// copy of the app. Escalate rather than bootstrapping into a taken port.
	for _, p := range strayDaemonPatterns {
		_ = exec.Command("pkill", "-9", "-f", p).Run()
	}
	_ = waitPortFree(2 * time.Second)
}

// waitPortFree reports whether nothing is listening on the dashboard port,
// waiting up to d for a dying daemon to let go of it.
func waitPortFree(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		conn, err := net.DialTimeout("tcp", defaultAddr, 200*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// resolveQueueBin finds the dispatchq CLI shipped next to this daemon binary (in
// the app bundle's Contents/MacOS, or the dev build directory). Its absolute
// path is handed to each run as DISPATCHQ_BIN so a task can queue follow-up work
// even though the daemon's launchd PATH does not include it. Empty when it
// cannot be located, in which case runs fall back to a bare "dispatchq" lookup.
func resolveQueueBin() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	cand := filepath.Join(filepath.Dir(self), "dispatchq")
	if info, err := os.Stat(cand); err == nil && !info.IsDir() {
		return cand
	}
	return ""
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
