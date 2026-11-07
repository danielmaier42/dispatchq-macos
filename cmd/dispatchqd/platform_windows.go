//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/system"
)

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ".exe"

// runKeyPath is where Windows keeps the per-user programs it starts at sign-in.
// It needs no administrator rights and follows the signed-in user, which is the
// identity that owns the provider logins the daemon must reach.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// runValueName is the entry the installer and `dispatchqd install` write.
const runValueName = "DispatchQ"

// daemonImage is the process name every daemon instance carries.
const daemonImage = "dispatchqd.exe"

// platformInit prepares the standard streams. dispatchqd is a GUI-subsystem
// program so that starting it at sign-in opens no console window, which also
// means it starts with no stdout or stderr. `run` writes the daemon log to a
// file in the data directory instead (the counterpart of the LaunchAgent's
// StandardOutPath); every other command borrows the console it was typed in.
func platformInit(args []string) {
	if len(args) > 0 && args[0] == "run" {
		redirectToLog()
		return
	}
	attachParentConsole()
}

func redirectToLog() {
	home, err := store.DefaultHome()
	if err != nil {
		return
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "dispatchqd.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	os.Stdout, os.Stderr = f, f
}

// attachParentConsole connects stdout and stderr to the console of the process
// that started this one, when there is one.
func attachParentConsole() {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	if r, _, _ := kernel32.NewProc("AttachConsole").Call(^uintptr(0)); r == 0 { // ATTACH_PARENT_PROCESS
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = out, out
	}
}

// newPlatform wires what this first Windows edition supports: the Run-key
// autostart, Windows toast notifications, native folder and save dialogs, and
// the execution-state sleep hold. Wake-from-sleep needs Task Scheduler wake
// timers and a power-plan check it does not have yet; continue-in-terminal and
// the in-app uninstaller stay nil, which the API answers as unavailable (the
// installer's uninstaller removes the app). There is no published Windows
// installer to update to, so no update service is created.
func newPlatform(r system.Runner) platform {
	toast := &notify.Toast{Runner: r, Register: registerToastApp}
	return platform{
		service: runKeyService{},
		sleep:   system.DefaultSleepInhibitor(),
		desktop: toast,
		// At daemon start: register (or repair) the Start menu identity toasts
		// need, and say in the log when that fails rather than at the first
		// notification. Startup waits for it at most 15 seconds; a slower
		// registration finishes in the background.
		requestNotifyAuth: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := toast.EnsureRegistered(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "dispatchqd:", err)
			}
		},
		notifyStatus:     toast.Status,
		chooseFolder:     api.PowerShellFolderChooser(r),
		saveFile:         api.PowerShellSaveFileDialog(r),
		osVersion:        osVersion(r),
		stopStrayDaemons: killStrayDaemons,
	}
}

// registerToastApp gives the per-user Start menu shortcut DispatchQ's
// AppUserModelID, which Windows requires before it shows a desktop app's
// toasts, and the stub activator CLSID Microsoft documents for keeping them
// in the notification center (not yet verified on Windows for DispatchQ).
// The shortcut and the window app it recreates a lost shortcut for sit where
// the installer puts them: the window app and the icon next to this daemon.
func registerToastApp() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate dispatchqd: %w", err)
	}
	lnk, err := notify.WindowsShortcutPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(self)
	return notify.RegisterWindowsApp(lnk, notify.WindowsAppID, notify.WindowsToastActivatorCLSID,
		filepath.Join(dir, "dispatchqapp"+exeSuffix), filepath.Join(dir, "dispatchq.ico"))
}

// osVersion reports the Windows version, for the feedback issue's environment line.
func osVersion(_ system.Runner) func() string {
	return func() string {
		v := windows.RtlGetVersion()
		return fmt.Sprintf("Windows %d.%d (build %d)", v.MajorVersion, v.MinorVersion, v.BuildNumber)
	}
}

// runCommand is the command line the Run key stores: the daemon's absolute
// path, quoted because Program Files and user names contain spaces.
func runCommand(self string) string { return `"` + self + `" run` }

// runKeyService keeps the daemon running in the signed-in session: it starts at
// every sign-in through the HKCU Run key, and Install starts it right away.
type runKeyService struct{}

// RetireLegacy does nothing: no ClaudeQ build ever shipped for Windows.
func (runKeyService) RetireLegacy(context.Context) error { return nil }

// Install registers the autostart entry and starts the daemon now. The daemon
// redirects its own log (see platformInit), so the spec's log paths are unused.
func (runKeyService) Install(_ context.Context, spec serviceSpec, out io.Writer) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open the Run key: %w", err)
	}
	defer func() { _ = k.Close() }()
	if err := k.SetStringValue(runValueName, runCommand(spec.Bin)); err != nil {
		return fmt.Errorf("register autostart: %w", err)
	}

	cmd := exec.Command(spec.Bin, spec.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	_ = cmd.Process.Release()

	fmt.Fprintf(out, "registered %s to start at sign-in (HKCU\\%s\\%s)\n", spec.Bin, runKeyPath, runValueName)
	fmt.Fprintln(out, "dispatchqd runs in your signed-in session; it stops when you sign out.")
	fmt.Fprintln(out, "Wake-from-sleep is not available on Windows yet: keep the PC awake for queued work.")
	return nil
}

// Uninstall removes the autostart entry and stops the daemon. The daemon is
// stopped even when the entry cannot be removed, so the installer's
// uninstaller can still delete its files; the error is returned after that.
func (runKeyService) Uninstall(_ context.Context, out io.Writer) error {
	err := removeAutostart(func() (valueDeleter, error) {
		k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
		if err != nil {
			return nil, err
		}
		return k, nil
	}, func(err error) bool { return errors.Is(err, registry.ErrNotExist) }, runValueName)
	killStrayDaemons()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "removed the %s autostart entry and stopped the daemon\n", runValueName)
	fmt.Fprintln(out, "To remove the app itself, use Settings > Apps > Installed apps > DispatchQ, or the Uninstall DispatchQ shortcut.")
	return nil
}

// killStrayDaemons ends every other dispatchqd process and waits for the
// loopback port to come free, so the daemon the caller starts next can bind it.
// It never ends the calling process: `dispatchqd install` is itself a
// dispatchqd.exe.
func killStrayDaemons() {
	for _, pid := range otherDaemonPIDs() {
		terminate(pid)
	}
	_ = waitPortFree(5 * time.Second)
}

func terminate(pid uint32) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.TerminateProcess(h, 1)
	_, _ = windows.WaitForSingleObject(h, 3000)
}

// otherDaemonPIDs lists the processes running dispatchqd.exe apart from this one.
func otherDaemonPIDs() []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var pids []uint32
	self := uint32(os.Getpid())
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ProcessID != self && strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), daemonImage) {
			pids = append(pids, e.ProcessID)
		}
	}
	return pids
}
