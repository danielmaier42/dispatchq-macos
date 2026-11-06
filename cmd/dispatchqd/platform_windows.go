//go:build windows

package main

import (
	"fmt"
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
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/wake"
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

// platformWaker is nil: waking the machine from sleep needs Task Scheduler wake
// timers and a power-plan check that this first Windows edition does not have.
func platformWaker() *wake.Scheduler { return nil }

// platformUpdates is nil: there is no published Windows installer to update to
// (releases ship only the macOS package), so the dashboard offers no update.
func platformUpdates() *update.Service { return nil }

// platformNotifiers is empty: native Windows notifications are not implemented.
// Pushover, ntfy and webhooks are added by the caller and work as on macOS.
func platformNotifiers() []notify.Notifier { return nil }

// platformDeps wires what Windows supports: native folder and save dialogs.
// Continue-in-terminal, native notification status and the in-app uninstaller
// stay nil, which the API answers as unavailable; the installer's uninstaller
// is the way to remove the app.
func platformDeps(d *api.Deps) {
	d.ChooseFolder = api.PowerShellFolderChooser(system.Real{})
	d.SaveFile = api.PowerShellSaveFileDialog(system.Real{})
	d.NotifyStatus = func() string { return notify.AuthorizationUnsupported }
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

// cmdInstall makes the daemon start at every sign-in and starts it now.
func cmdInstall() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	home, err := store.DefaultHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	// Stop an older daemon first: it holds the dashboard port and the store
	// lock, and a hand-over to a new version would otherwise start a daemon that
	// cannot bind.
	killStrayDaemons()

	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open the Run key: %w", err)
	}
	defer func() { _ = k.Close() }()
	if err := k.SetStringValue(runValueName, runCommand(self)); err != nil {
		return fmt.Errorf("register autostart: %w", err)
	}

	cmd := exec.Command(self, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	_ = cmd.Process.Release()

	fmt.Printf("registered %s to start at sign-in (HKCU\\%s\\%s)\n", self, runKeyPath, runValueName)
	fmt.Println("dispatchqd runs in your signed-in session; it stops when you sign out.")
	fmt.Println("Wake-from-sleep is not available on Windows yet: keep the PC awake for queued work.")
	return nil
}

// cmdUninstall stops the daemon and removes its autostart entry.
func cmdUninstall() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err == nil {
		defer func() { _ = k.Close() }()
		if err := k.DeleteValue(runValueName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("remove autostart: %w", err)
		}
	}
	killStrayDaemons()
	fmt.Printf("removed the %s autostart entry and stopped the daemon\n", runValueName)
	fmt.Println("To remove the app itself, use Settings > Apps > Installed apps > DispatchQ, or the Uninstall DispatchQ shortcut.")
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
