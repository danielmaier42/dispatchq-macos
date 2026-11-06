package main

import (
	"context"
	"errors"
	"io"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/engine"
	"github.com/danielmaier42/claudeq/internal/notify"
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
	// stopStrayDaemons ends every other running daemon of this app and waits for
	// the dashboard port to come free, so the daemon that install starts next can
	// bind it. It never ends the calling process. Nil where nothing is installed.
	stopStrayDaemons func()
	// selfUpdate tells whether a downloaded release installs on this system.
	selfUpdate bool
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
func (p platform) stopStray() {
	if p.stopStrayDaemons != nil {
		p.stopStrayDaemons()
	}
}
