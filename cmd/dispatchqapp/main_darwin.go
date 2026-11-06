//go:build darwin

// Command dispatchqapp is the native macOS window for dispatchq: a thin WKWebView
// that shows the dashboard served by the dispatchqd daemon (PLAN.md D3, phase 5b).
// It starts the daemon if it isn't already running and injects the real macOS
// accent color (which WebKit's CSS AccentColor does not expose reliably).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	webview "github.com/webview/webview_go"

	"github.com/danielmaier42/claudeq/internal/launchd"
	"github.com/danielmaier42/claudeq/internal/notify"
)

// systemSettingsScheme is the URL scheme that opens a System Settings pane.
const systemSettingsScheme = "x-apple.systempreferences:"

func main() {
	runtime.LockOSThread()
	// Claim notification clicks before anything else: webview.New runs the app's
	// launch cycle, and a click that launched DispatchQ is only delivered to a
	// delegate that was already in place by then.
	installNotifyDelegate()
	takeOverFromClaudeQ()
	ensureDaemon()

	// Opening the window is a reliable "user is present" moment, so ask the daemon
	// to (re)provoke the macOS file-access prompt for the current task folders now
	// — the daemon is a persistent LaunchAgent that doesn't restart when the window
	// is reopened, so its own startup warm-up wouldn't fire here. Best-effort.
	go requestWarm()

	w := webview.New(false)
	defer w.Destroy()

	// Ask for notification permission from here, not from the daemon: this is the
	// foreground app, so macOS can actually put the prompt on screen. A request
	// from the background LaunchAgent errors out and leaves the app unauthorized,
	// which silently swallows every notification it posts.
	notify.RequestMacAuthorization()
	w.SetTitle("DispatchQ")
	w.SetSize(1120, 760, webview.HintNone)
	installOpenPanel(w.Window())

	// Native menu bar (webview_go creates none). Custom items drive the dashboard
	// via the same JS the sidebar uses: openAdd() and select('settings'), and
	// View > Refresh (Cmd+R) re-reads every view from the daemon: cqRefresh().
	installMenu(
		func() { w.Dispatch(func() { w.Eval("window.openAdd && window.openAdd()") }) },
		func() { w.Dispatch(func() { w.Eval("window.select && window.select('settings')") }) },
		func() { w.Dispatch(func() { w.Eval("window.cqRefresh && window.cqRefresh()") }) },
	)

	// Open external (http/https) links in the default browser — WKWebView won't
	// open target=_blank links on its own. The System Settings scheme is allowed
	// too, so the Settings view can send the operator straight to the macOS
	// notification preferences.
	_ = w.Bind("cqOpenExternal", func(u string) {
		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") ||
			strings.HasPrefix(u, systemSettingsScheme) {
			_ = exec.Command("open", u).Start()
		}
	})

	// Clicking a notification opens what it is about in the window: the
	// artifact, the run's log, or the Notifications view. The target is fetched
	// by the page (not pushed) so that a click during launch — before the page
	// exists — is picked up as soon as it has loaded.
	_ = w.Bind("cqTakePendingNotification", takePendingNotification)
	onNotificationClick(func() {
		w.Dispatch(func() { w.Eval("window.cqOpenPendingNotification && window.cqOpenPendingNotification()") })
	})

	// Settings → Appearance pins the window light or dark. The page colours
	// itself (web/src/lib/theme.ts); this sets the AppKit side, so the title bar
	// and what WKWebView reports to the page follow suit.
	_ = w.Bind("cqSetAppearance", func(mode string) { w.Dispatch(func() { setAppearance(mode) }) })

	// Expose the macOS accent to the page and tell it about every change. The
	// page decides what to do with it: Settings → Appearance can pin another
	// accent, so the system one is only a value it may use, not a style.
	_ = w.Bind("cqReadAccent", func() string { return accentHex() })
	w.Init(`
		window.cqApplyAccent = function(hex){
			window.cqSystemAccent = hex || '';
			window.dispatchEvent(new CustomEvent('cq:system-accent', {detail: hex || ''}));
		};
		window.cqOpenPendingNotification = async function(){
			try {
				var t = await window.cqTakePendingNotification();
				if (t && window.cqOpenNotificationTarget) window.cqOpenNotificationTarget(t);
			} catch (e) {}
		};
		window.addEventListener('DOMContentLoaded', async function(){
			try { window.cqApplyAccent(await window.cqReadAccent()); } catch (e) {}
			window.cqOpenPendingNotification();
		});
	`)

	// Live updates: re-apply the accent whenever a system notification fires (the
	// app runs in the GUI session, so the distributed-notification observer
	// actually fires here). We re-read a few times over ~1.2s because a change to
	// the accent color reaches AppKit instantly but the underlying preference can
	// lag briefly — the retries make the update land without a second event.
	startAccentObserver(func() { applyAccent(w) })

	w.Navigate(dashboardURL)
	w.Run()
}

// applyAccent pushes the current accent color into the page, re-reading over a
// few seconds because cfprefsd can serve the old AppleAccentColor for 1-3s after
// a change — the spread of re-reads makes the new color land without needing a
// second event (like a dark/light toggle).
var accentRetries = []time.Duration{
	0,
	200 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	1800 * time.Millisecond,
	2800 * time.Millisecond,
}

func applyAccent(w webview.WebView) {
	for _, d := range accentRetries {
		d := d
		go func() {
			if d > 0 {
				time.Sleep(d)
			}
			hex := accentHex()
			w.Dispatch(func() {
				w.Eval("window.cqApplyAccent && window.cqApplyAccent(" + strconv.Quote(hex) + ")")
			})
		}()
	}
}

// takeOverFromClaudeQ hands the background service over from ClaudeQ, the
// app's name before 2.0, when the installer could not: it does so only for the
// user logged in at the screen, so an install with nobody logged in leaves
// ClaudeQ's agent behind, and its daemon would keep answering on the dashboard
// port. The bundled `dispatchqd install` boots that agent out, stops its
// daemon and installs DispatchQ's own.
func takeOverFromClaudeQ() {
	home, err := os.UserHomeDir()
	if err != nil || !claudeQAgentInstalled(home) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	daemon := filepath.Join(filepath.Dir(exe), "dispatchqd")
	if !fileExists(daemon) {
		return
	}
	cmd := exec.Command(daemon, "install")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqapp: could not take over from ClaudeQ:", err)
	}
}

// claudeQAgentInstalled reports whether ClaudeQ's LaunchAgent is still in
// place under home.
func claudeQAgentInstalled(home string) bool {
	return fileExists(filepath.Join(home, "Library", "LaunchAgents", launchd.LegacyLabel+".plist"))
}

// accentHex maps the current macOS accent index to a hex color, or "" for the
// default/unset accent (multicolor), which leaves the dashboard on its default.
func accentHex() string {
	return macAccentHex[readAccentIndex()]
}

var macAccentHex = map[int]string{
	-1: "#8e8e93", // graphite
	0:  "#ff5257", // red
	1:  "#f7821b", // orange
	2:  "#ffc600", // yellow
	3:  "#62ba46", // green
	4:  "#007aff", // blue
	5:  "#8944ab", // purple
	6:  "#f74f9e", // pink
}

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ""

// daemonCommand is how the window starts the daemon it did not find running.
// Its own words matter: a daemon that refuses because another one already owns
// the store says so on stderr, and that line is the whole explanation.
func daemonCommand(bin string) *exec.Cmd {
	cmd := exec.Command(bin, "run")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd
}
