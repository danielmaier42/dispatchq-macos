// Command dispatchqd is the dispatchq background daemon: it runs the scheduling
// loop that executes due tasks via the Claude Code CLI, and can install itself
// as a login service, a launchd LaunchAgent on macOS (PLAN.md build phases 2–3).
//
// Usage:
//
//	dispatchqd --version
//	dispatchqd run [--interval 5s]
//	dispatchqd install     # install & start the login service (autostart)
//	dispatchqd uninstall    # stop & remove the login service
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/aside"
	"github.com/danielmaier42/claudeq/internal/assistant"
	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/credit"
	"github.com/danielmaier42/claudeq/internal/engine"
	"github.com/danielmaier42/claudeq/internal/execenv"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/feedback"
	"github.com/danielmaier42/claudeq/internal/limit"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/adapters"
	"github.com/danielmaier42/claudeq/internal/review"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
	"github.com/danielmaier42/claudeq/internal/webview2"
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
	platformInit(os.Args[1:])
	if err := run(os.Args[1:]); err != nil {
		var refused errClientRefused
		if errors.As(err, &refused) {
			// The reason is quoted by webview2-status as it is.
			fmt.Fprintln(os.Stderr, err)
		} else {
			fmt.Fprintln(os.Stderr, "dispatchqd:", err)
		}
		os.Exit(exitCode(err))
	}
}

// exitWebView2Missing is the exit code of webview2-status when the window
// will not find a usable WebView2 Runtime. Any other failure exits with 1, so
// the installer can tell a missing runtime from a check that did not run.
const exitWebView2Missing = 10

var errWebView2Missing = errors.New("webview2-status: the DispatchQ window will not find a usable WebView2 Runtime")

// errClientRefused wraps why webview2-load-client could not use the library.
type errClientRefused struct{ err error }

func (e errClientRefused) Error() string { return e.err.Error() }

func exitCode(err error) int {
	var refused errClientRefused
	switch {
	case errors.Is(err, errWebView2Missing):
		return exitWebView2Missing
	case errors.As(err, &refused):
		return webview2.ExitClientRefused
	}
	return 1
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
		return cmdInstall(newPlatform(system.Real{}))
	case "uninstall":
		return cmdUninstall(newPlatform(system.Real{}))
	case "stop-installation":
		return cmdStopInstallation(newPlatform(system.Real{}), args[1:], os.Stdout)
	case "webview2-status":
		return cmdWebView2Status(newPlatform(system.Real{}), args[1:], os.Stdout)
	case "webview2-load-client":
		return cmdWebView2LoadClient(newPlatform(system.Real{}), args[1:])
	default:
		fmt.Println("usage: dispatchqd run [--interval 5s] | install | uninstall | --version")
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	interval := fs.Duration("interval", 5*time.Second, "scheduler tick interval")
	noWake := fs.Bool("no-wake", false, "do not schedule wakes from sleep")
	addr := fs.String("addr", defaultAddr, "dashboard/API listen address (loopback only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	plat := newPlatform(system.Real{})

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
	// DispatchQ for Windows can run a provider inside WSL. Installing the
	// service starts nothing; wsl.exe runs only for an instance configured to
	// run there, so a native-only setup never touches WSL.
	if svc := execenv.Platform(home); svc != nil {
		registry.SetWSL(svc)
		go reapWSL(st, registry)
	}
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
	wakeOn := !*noWake && plat.waker != nil
	if wakeOn {
		eng.SetWaker(plat.waker)
	} else if !*noWake {
		fmt.Fprintln(os.Stdout, "dispatchqd: wake from sleep is not supported on this system")
	}
	if plat.sleep != nil {
		eng.SetSleepInhibitor(plat.sleep)
	}
	eng.SetNotifier(liveNotifier{st: st, desktop: plat.desktop})
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
	// Ask for notification permission up front (on macOS only does anything when
	// running from the app bundle) so run-outcome notifications carry the app icon.
	if plat.requestNotifyAuth != nil {
		plat.requestNotifyAuth()
	}
	// Provoke the macOS file-access (TCC) consent prompt now, at startup — which
	// happens at install and at every login, while the user is present — rather
	// than mid-run overnight, where the prompt has no one to answer it and the
	// run stalls. Reading each task folder is what makes macOS raise the prompt;
	// once answered the decision sticks and this becomes a no-op. Folders added
	// later are warmed per-task from the API (Deps.WarmFileAccess) at add/edit.
	if plat.warmFileAccess != nil {
		go warmEnabledTasks(st, plat.warmFileAccess)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background update checker: polls GitHub for a newer release hourly and
	// caches the result so the dashboard's "update available" banner and the
	// manual "Check for updates" button answer instantly (see internal/update).
	// Each check and download looks for this system's own installer (operating
	// system and architecture). A system without a release installer gets no
	// update service at all. The update endpoints then answer "not available".
	updates := newUpdateService(plat, update.CurrentTarget())
	if updates != nil {
		go updates.Run(ctx)
	}
	go limits.Run(ctx, provider.DefaultLimitsInterval, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "dispatchqd: "+format+"\n", args...)
	})

	// Prompt review, the assistant chat and the feedback assistant ask a
	// harness questions of dispatchq's own (see internal/aside); all go through
	// the one runner, so they cannot drift apart in how they invoke a provider.
	asides := &aside.Runner{Registry: registry}
	// An API key's credit cannot be read, so it is proven: by a daily one-word
	// ping and by every run. An account proven out of credit stays closed, also
	// across a restart, until a ping gets an answer again.
	credits := &credit.Monitor{
		Store: st, Ask: asides, APIKey: limits.APIKey,
		List: func() ([]provider.Instance, error) {
			set, err := app.Providers(st)
			if err != nil {
				return nil, err
			}
			return set.All(), nil
		},
		Block: eng.BlockAccount, Lift: eng.LiftAccountBlock,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "dispatchqd: "+format+"\n", args...)
		},
	}
	if err := credits.Restore(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqd:", err)
	}
	eng.SetCreditSeen(credits.RunSeen)
	// The pings need to know which accounts are on an API key, which the first
	// allowance reading finds out; the timer above takes it at once.
	go func() {
		if err := limits.RefreshAll(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "dispatchqd: provider limits:", err)
		}
		credits.Run(ctx, provider.DefaultLimitsInterval)
	}()
	osVer := func() string { return "" }
	if plat.osVersion != nil {
		osVer = plat.osVersion
	}
	// The assistant runs the CLI that ships next to this daemon, the same one
	// every run gets as DISPATCHQ_BIN, and tells the model where it is.
	cliBin := resolveQueueBin()
	cli := assistant.ExecCLI{Bin: cliBin}
	rt := assistant.PlatformRuntime(runtime.GOOS, osVer(), executor.RunsScripts(runtime.GOOS))
	rt.Version, rt.CLIPath, rt.DataDir = version.String(), cliBin, st.Home()

	httpSrv := &http.Server{
		Addr: *addr,
		Handler: api.Handler(api.Deps{
			Store: st, Runner: eng, Canceler: eng, TakeOver: eng,
			ChooseFolder: plat.chooseFolder, ActiveTasks: eng.ActiveTaskIDs,
			SaveFile:     plat.saveFile,
			OpenTerminal: plat.openTerminal,
			WakeError:    eng.WakeError, WarmFileAccess: plat.warmFileAccess, Updates: updates,
			LimitedUntil: eng.LimitedUntil, BlockedProviders: eng.BlockedProviders, BlockReasons: eng.BlockReasons,
			Holds: eng.Holds, RunningOn: eng.RunningOn,
			NotifyStatus: plat.notifyStatus,
			Feedback:     feedback.New(asides), OSVersion: plat.osVersion,
			Assistant: assistant.New(asides, &assistant.StoreSource{Store: st, CLI: cli, Runtime: rt}, cli, rt),
			Review:    &review.Reviewer{Ask: asides},
			Registry:  registry, Providers: checker, Limits: limits, Credits: credits,
			Uninstall: plat.uninstall,
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
		version.String(), home, *addr, *interval, wakeOn)
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
type liveNotifier struct {
	st *store.Store
	// desktop is the platform's on-screen channel. It is nil when there is none.
	desktop notify.Notifier
}

// Notify delivers to every channel that is switched on and configured. A
// config that fails to load is reported rather than swallowed: silently
// falling back to a zero Settings would drop every remote channel from this
// send (and every send after, until the file is fixed) with no trace of why.
func (l liveNotifier) Notify(ctx context.Context, n notify.Notification) error {
	cfg, err := l.st.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: notify: load config: %v (falling back to desktop notifications only)\n", err)
	}
	return notify.Multi{Notifiers: notifyChannels(l.desktop, cfg.Settings)}.Notify(ctx, n)
}

// notifyChannels assembles the notification channels: the desktop channel
// whenever the platform has one (native macOS), plus every remote channel that
// is both switched on and configured (FA-39/40/41).
// A channel switched on but left half-filled in is skipped rather than attempted
// and logged as an error on every single notification.
func notifyChannels(desktop notify.Notifier, s store.Settings) []notify.Notifier {
	var notifiers []notify.Notifier
	if desktop != nil {
		notifiers = append(notifiers, desktop)
	}
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

// warmEnabledTasks provokes the prompt for every enabled task's folder at
// startup (install time and each login, while the user is present).
func warmEnabledTasks(st *store.Store, warm func(dirs []string)) {
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
	warm(dirs)
}

func cmdInstall(p platform) error {
	// Check first: on a system without a service manager, install must not stop
	// a running daemon and then fail.
	if p.service == nil {
		return fmt.Errorf("install: %w", errUnsupported)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	ctx := context.Background()
	// ClaudeQ's service and daemon go first. DefaultHome below moves ClaudeQ's
	// data, which its daemon must no longer be writing to.
	if err := p.service.RetireLegacy(ctx); err != nil {
		return err
	}
	if err := p.stopStray(); err != nil {
		return err
	}

	home, err := store.DefaultHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	spec := serviceSpec{
		Bin:        self,
		Args:       []string{"run"},
		StdoutPath: filepath.Join(home, "dispatchqd.out.log"),
		StderrPath: filepath.Join(home, "dispatchqd.err.log"),
	}

	// Stop any stray daemon still holding the loopback port (an orphan from a
	// prior run/version that the service manager does not catch) so the freshly
	// started one can bind and actually take over — otherwise an update
	// wouldn't take effect until a reboot.
	if err := p.stopStray(); err != nil {
		return err
	}

	return p.service.Install(ctx, spec, os.Stdout)
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

// cmdStopInstallation is run by the Windows installer before it changes
// anything: --dir names the installation folder whose window and daemon it
// may stop. It is not part of the documented command set.
func cmdStopInstallation(p platform, args []string, out io.Writer) error {
	if p.stopInstallation == nil {
		return fmt.Errorf("stop-installation: %w", errUnsupported)
	}
	fs := flag.NewFlagSet("stop-installation", flag.ContinueOnError)
	dir := fs.String("dir", "", "installation folder")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || !filepath.IsAbs(*dir) || fs.NArg() != 0 {
		return errors.New("stop-installation: --dir must name the installation folder as an absolute path")
	}
	return p.stopInstallation(filepath.Clean(*dir), out)
}

// cmdWebView2Status is run by the Windows installer before and after the
// WebView2 bootstrapper, and by hand to check a PC: it prints whether the
// window will find a usable WebView2 Runtime and the registrations behind the
// answer, and fails with errWebView2Missing when it will not. It changes
// nothing.
func cmdWebView2Status(p platform, args []string, out io.Writer) error {
	if p.webView2Status == nil {
		return fmt.Errorf("webview2-status: %w", errUnsupported)
	}
	if len(args) != 0 {
		return errors.New("webview2-status takes no arguments")
	}
	ok, err := p.webView2Status(out)
	if err != nil {
		return fmt.Errorf("webview2-status: %w", err)
	}
	if !ok {
		return errWebView2Missing
	}
	return nil
}

// cmdWebView2LoadClient is the separate process webview2-status starts to
// load the chosen WebView2 client library like the window does; it exits with
// webview2.ExitClientRefused and the reason when that fails. It is not part of
// the documented command set.
func cmdWebView2LoadClient(p platform, args []string) error {
	if p.loadWebView2Client == nil {
		return fmt.Errorf("webview2-load-client: %w", errUnsupported)
	}
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		return errors.New("webview2-load-client takes the library's absolute path")
	}
	if err := p.loadWebView2Client(args[0]); err != nil {
		return errClientRefused{err}
	}
	return nil
}

func cmdUninstall(p platform) error {
	if p.service == nil {
		return fmt.Errorf("uninstall: %w", errUnsupported)
	}
	return p.service.Uninstall(context.Background(), os.Stdout)
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
	cand := filepath.Join(filepath.Dir(self), "dispatchq"+exeSuffix)
	if info, err := os.Stat(cand); err == nil && !info.IsDir() {
		return cand
	}
	return ""
}

// reapWSL stops Linux process trees an earlier daemon of this installation
// left behind in WSL, when that daemon died so abruptly that its helpers could
// not stop them and their liveness lease did not either (the helper itself
// was killed). It asks only the distributions and users a configured provider
// runs in, and the helper there touches only this installation's records whose
// helper is provably gone.
func reapWSL(st *store.Store, reg *provider.Registry) {
	set, err := app.Providers(st)
	if err != nil {
		return
	}
	svc := reg.WSL()
	seen := map[provider.Execution]bool{}
	for _, inst := range set.All() {
		e := inst.Execution
		if e.Type != provider.ExecWSL || seen[e] || !inst.Enabled {
			continue
		}
		seen[e] = true
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		if err := svc.Reap(ctx, e); err != nil {
			fmt.Fprintf(os.Stderr, "dispatchqd: cleaning up after earlier runs in %s: %v\n", e.Label(), err)
		}
		cancel()
	}
}
