package main

import (
	"context"
	"errors"
	"io"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/engine"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/update"
)

// errUnsupported is the answer of an operation that this operating system
// does not offer.
var errUnsupported = errors.New("not supported on this operating system")

// platform is everything the daemon gets from the operating system it runs
// on. newPlatform builds it in one file per system (platform_darwin.go, and
// platform_other.go for the rest), so the daemon itself names no system tool.
// A nil field means the system has no such service. The daemon then skips it.
// The dashboard endpoints of a missing dialog, terminal, uninstaller or update
// service answer "not available".
type platform struct {
	// service installs the daemon so it starts at login and restarts on exit.
	service serviceManager
	// waker wakes the machine from sleep for upcoming work.
	waker engine.Waker
	// sleep holds off idle system sleep while runs are in flight.
	sleep engine.SleepInhibitor
	// desktop shows a notification on this machine's screen.
	desktop notify.Notifier
	// requestNotifyAuth asks once for permission to show notifications.
	requestNotifyAuth func()
	// notifyStatus reports whether the system shows this app's notifications.
	notifyStatus func() string
	// chooseFolder, saveFile and openTerminal are the native dialogs and the
	// terminal window the dashboard opens on the operator's screen.
	chooseFolder api.FolderChooser
	saveFile     api.SaveFileDialog
	openTerminal api.TerminalOpener
	// warmFileAccess raises the system's file-access consent prompts for the
	// given folders while the operator is present.
	warmFileAccess func(dirs []string)
	// osVersion reports the system version for the feedback issue.
	osVersion func() string
	// uninstall opens the app's uninstaller.
	uninstall func(ctx context.Context) error
	// stopStrayDaemons ends the running daemon of this app and waits for the
	// dashboard port to come free, so the daemon that install starts next can
	// bind it. It never ends the calling process. An error means a daemon that
	// must not or could not be stopped still owns the data directory. Nil where
	// nothing is installed.
	stopStrayDaemons func() error
	// stopInstallation stops the window and daemon of the installation in a
	// folder before the installer replaces it, and fails without stopping
	// anything when another installation owns the data directory. Nil where
	// there is no such installer.
	stopInstallation func(dir string, out io.Writer) error
	// webView2Status writes whether the window will find a usable WebView2
	// Runtime and reports it. Nil where the window does not use WebView2.
	webView2Status func(out io.Writer) (available bool, err error)
	// loadWebView2Client loads a WebView2 client library the way the window
	// does; webView2Status runs it in a separate, time-limited process.
	loadWebView2Client func(path string) error
	// openInstaller hands a downloaded release installer to the operator, who
	// finishes the update in it. Nil where no installer is published for this
	// system; the daemon then runs no update service.
	openInstaller func(ctx context.Context, path string) error
}

// newUpdateService builds the release check and installer download for target,
// the system this daemon was built for, handing downloads to the platform's
// installer opener. It is nil, and the daemon runs no update service, where the
// platform cannot open an installer or none is published for target.
func newUpdateService(p platform, target update.Target) *update.Service {
	if p.openInstaller == nil || !target.Supported() {
		return nil
	}
	return update.NewService(update.GitHubFetcher{Target: target}, update.DefaultInterval,
		update.WithTarget(target), update.WithOpener(p.openInstaller))
}

// serviceSpec is the daemon command a service manager starts.
type serviceSpec struct {
	Bin        string   // absolute path to dispatchqd
	Args       []string // arguments, e.g. ["run"]
	StdoutPath string
	StderrPath string
}

// serviceManager registers the daemon with the system's service manager.
type serviceManager interface {
	// RetireLegacy stops and removes the service of an earlier install (the
	// app's name before DispatchQ). It runs before the data directory moves.
	RetireLegacy(ctx context.Context) error
	// Install registers spec, starts it, and tells the operator about it on out.
	Install(ctx context.Context, spec serviceSpec, out io.Writer) error
	// Uninstall stops and removes the service, and tells the operator on out.
	Uninstall(ctx context.Context, out io.Writer) error
}

// stopStray runs the platform's stray-daemon termination, if it has one.
func (p platform) stopStray() error {
	if p.stopStrayDaemons != nil {
		return p.stopStrayDaemons()
	}
	return nil
}
