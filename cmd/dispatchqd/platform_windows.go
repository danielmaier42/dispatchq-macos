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
	"runtime"
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
	"github.com/danielmaier42/claudeq/internal/webview2"
)

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ".exe"

// runKeyPath is where Windows keeps the per-user programs it starts at sign-in.
// It needs no administrator rights and follows the signed-in user, which is the
// identity that owns the provider logins the daemon must reach.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// runValueName is the entry the installer and `dispatchqd install` write.
const runValueName = "DispatchQ"

// daemonImage is the file name of the daemon executable.
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
	// The load check reports to the pipes of the webview2-status that started
	// it, never to a console that one may have attached.
	if len(args) > 0 && args[0] == "webview2-load-client" {
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
// installer's uninstaller removes the app). Updates download the release's
// Windows installer for this architecture and open it for the operator, whose
// setup then stops and replaces this installation the way a manual run does.
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
		stopInstallation: stopInstallation,
		webView2Status:   webView2Status,
		openInstaller:    openInstaller(shellOpen),

		loadWebView2Client: webview2.LoadClient,
	}
}

// openInstaller hands a downloaded setup program to the operator: launch
// starts it in a window of its own, without the silent switch, so the operator
// confirms the update in setup and setup itself stops this installation
// (stop-installation) before it replaces anything. Only an .exe is started.
func openInstaller(launch func(path string) error) func(ctx context.Context, path string) error {
	return func(ctx context.Context, path string) error {
		if !strings.EqualFold(filepath.Ext(path), exeSuffix) {
			return fmt.Errorf("refusing to start %s: not a Windows setup program", path)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := launch(path); err != nil {
			return fmt.Errorf("start setup %s: %w", path, err)
		}
		return nil
	}
}

// sFalse is S_FALSE: COM was already initialized on this thread, and the call
// still has to be balanced by CoUninitialize.
const sFalse = syscall.Errno(1)

// shellOpen opens path the way Explorer does on a double click, through
// ShellExecuteW, which also shows the elevation prompt should a program ask
// for it. ShellExecute can involve COM-activated shell extensions, so COM is
// initialized on this thread first, as the ShellExecuteW documentation asks.
func shellOpen(path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); {
	case err == nil, errors.Is(err, sFalse):
		defer windows.CoUninitialize()
	case errors.Is(err, syscall.Errno(windows.RPC_E_CHANGED_MODE)):
		// The thread already runs a multithreaded apartment; ShellExecute
		// works there too, and that initialization is not ours to undo.
	default:
		return fmt.Errorf("initialize COM: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL)
}

// webView2Status repeats the window's own WebView2 lookup (the daemon and the
// window are built for the same architecture) and prints what it found, with
// the user's profile folders shortened.
//
// Loading the client library runs its code, so it happens in a copy of this
// program (webview2-load-client) that is ended after
// webview2.LoadCheckTimeout: a library that hangs is reported, not waited for.
func webView2Status(out io.Writer) (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("locate dispatchqd: %w", err)
	}
	st := webview2.ProbeSystem(func(path string) error {
		return webview2.RunLoadCheck(context.Background(), webview2.LoadCheckTimeout, self, "webview2-load-client", path)
	})
	return st.Available, st.Write(out, webview2.SystemSanitizer())
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

// Uninstall stops this installation's window and daemon, then removes the
// autostart entry. When the daemon of the data directory belongs to another
// installation or cannot be identified, nothing is stopped or removed and the
// reason is returned. Another installation's window or daemon, and a daemon
// for another data directory, keep running.
func (runKeyService) Uninstall(_ context.Context, out io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate dispatchqd: %w", err)
	}
	s, err := newDaemonStopper([]string{self})
	if err != nil {
		return err
	}
	return uninstallInOrder(
		func() error { return stopWindows([]string{siblingWindow(self)}) },
		s.stop,
		func() error {
			return removeAutostart(func() (valueDeleter, error) {
				k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
				if err != nil {
					return nil, err
				}
				return k, nil
			}, func(err error) bool { return errors.Is(err, registry.ErrNotExist) }, runValueName)
		},
		runValueName, s.home, out)
}

// installDirKey and installDirValue are where the installer records the
// folder it installed into.
const (
	installDirKey   = `Software\DispatchQ`
	installDirValue = "InstallDir"
)

// windowImage is the window app installed next to the daemon.
const windowImage = "dispatchqapp.exe"

func siblingWindow(daemon string) string { return filepath.Join(filepath.Dir(daemon), windowImage) }

// killStrayDaemons stops this installation's daemon for the current data
// directory and waits for the loopback port to come free, so the daemon the
// caller starts next can bind it. This installation is the executable running
// this command or the folder the installer last recorded: an upgrade into
// another folder replaces that installation (the Run entry and the data
// directory are per user). It never ends the calling process, a daemon of
// another installation, or one serving another data directory
// (DISPATCHQ_HOME); a data directory owned by such a process is an error.
func killStrayDaemons() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate dispatchqd: %w", err)
	}
	s, err := newDaemonStopper(installationDaemons(self, registeredInstallDir()))
	if err != nil {
		return err
	}
	if _, err := s.stop(); err != nil {
		return err
	}
	if !waitPortFree(5 * time.Second) {
		fmt.Fprintf(os.Stderr, "dispatchqd: %s is still in use by another program, perhaps DispatchQ for a different data directory. "+
			"The daemon for %s will run queued work, but its dashboard stays unavailable until that program quits and this daemon is restarted\n",
			defaultAddr, s.home)
	}
	return nil
}

// stopInstallation is the installer's check before it changes anything: it
// stops the window and the daemon of the installation in dir (and of the
// folder the installer last recorded) and fails, stopping nothing, when the
// data directory is used by another installation or an unidentifiable
// process. The installer runs it from a copy of the new dispatchqd in its
// temporary folder, never the installed one, which may be an older build.
func stopInstallation(dir string, out io.Writer) error {
	daemons := installationDaemons(filepath.Join(dir, daemonImage), registeredInstallDir())
	windowsToStop := make([]string, len(daemons))
	for i, d := range daemons {
		windowsToStop[i] = siblingWindow(d)
	}
	if err := stopWindows(windowsToStop); err != nil {
		return err
	}
	s, err := newDaemonStopper(daemons)
	if err != nil {
		return err
	}
	stopped, err := s.stop()
	if err != nil {
		return err
	}
	if len(stopped) == 0 {
		fmt.Fprintf(out, "no daemon was running for %s\n", s.home)
	} else {
		fmt.Fprintf(out, "stopped the daemon for %s (pid %s)\n", s.home, joinPIDs(stopped))
	}
	return nil
}

// installationDaemons is the daemon executable of an installation plus the
// one in the folder the installer recorded, when there is one.
func installationDaemons(daemon, registeredDir string) []string {
	images := []string{daemon}
	if registeredDir != "" {
		if prev := filepath.Join(registeredDir, daemonImage); !strings.EqualFold(filepath.Clean(prev), filepath.Clean(daemon)) {
			images = append(images, prev)
		}
	}
	return images
}

// newDaemonStopper builds the stopper for the current data directory that
// accepts the given daemon executables.
func newDaemonStopper(images []string) (daemonStopper, error) {
	home, err := store.DefaultHome()
	if err != nil {
		return daemonStopper{}, err
	}
	return daemonStopper{
		home:      home,
		self:      uint32(os.Getpid()),
		images:    images,
		probe:     func() (store.DaemonHolder, error) { return store.ProbeDaemonLock(home) },
		lockUsers: func() ([]processInstance, error) { return processesUsingFile(store.DaemonLockPath(home)) },
		open:      openWindowsProcess,
		sameFile:  sameExecutable,
		sleep:     time.Sleep,
		attempts:  30,
		exitWait:  3 * time.Second,
	}, nil
}

// registeredInstallDir is the folder the installer recorded, or "" when there
// is none or it is not an absolute path.
func registeredInstallDir() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, installDirKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	dir, _, err := k.GetStringValue(installDirValue)
	if err != nil || !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// stopWindows closes the window apps that run one of images. A window of
// another installation keeps running.
func stopWindows(images []string) error {
	_, err := stopInstallationProcesses(processesNamed(windowImage), uint32(os.Getpid()),
		images, openWindowsProcess, sameExecutable, 3*time.Second)
	return err
}

// processesNamed lists the pids whose executable is called name. The name is
// only a preselection: callers confirm the full path before acting.
func processesNamed(name string) []uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var pids []uint32
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), name) {
			pids = append(pids, e.ProcessID)
		}
	}
	return pids
}

// The Restart Manager reports which processes have a file open, by process
// instance. https://learn.microsoft.com/windows/win32/rstmgr/restart-manager-portal
var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

const (
	cchRmSessionKey = 32 // CCH_RM_SESSION_KEY: sizeof(GUID) * 2
	cchRmMaxAppName = 255
	cchRmMaxSvcName = 63
	errorMoreData   = 234
)

// rmUniqueProcess is RM_UNIQUE_PROCESS.
type rmUniqueProcess struct {
	ProcessID        uint32
	ProcessStartTime windows.Filetime
}

// rmProcessInfo is RM_PROCESS_INFO.
type rmProcessInfo struct {
	Process          rmUniqueProcess
	AppName          [cchRmMaxAppName + 1]uint16
	ServiceShortName [cchRmMaxSvcName + 1]uint16
	ApplicationType  uint32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

// processesUsingFile lists the processes that have path open, as the Restart
// Manager reports them: pid and the start time the system recorded for that
// process instance.
func processesUsingFile(path string) ([]processInstance, error) {
	var session uint32
	var key [cchRmSessionKey + 1]uint16
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return nil, fmt.Errorf("RmStartSession: %w", syscall.Errno(r))
	}
	defer func() { _, _, _ = procRmEndSession.Call(uintptr(session)) }()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	names := []*uint16{name}
	if r, _, _ := procRmRegisterResources.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("RmRegisterResources: %w", syscall.Errno(r))
	}
	for range 5 {
		var needed, count, reasons uint32
		infos := make([]rmProcessInfo, 8)
		count = uint32(len(infos))
		r, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
			uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
		if r == errorMoreData {
			infos = make([]rmProcessInfo, needed)
			count = needed
			r, _, _ = procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)),
				uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
		}
		if r == errorMoreData {
			continue // the list grew in between; ask again
		}
		if r != 0 {
			return nil, fmt.Errorf("RmGetList: %w", syscall.Errno(r))
		}
		out := make([]processInstance, 0, count)
		for _, in := range infos[:count] {
			out = append(out, processInstance{PID: in.Process.ProcessID, Started: time.Unix(0, in.Process.ProcessStartTime.Nanoseconds())})
		}
		return out, nil
	}
	return nil, errors.New("RmGetList: the list of processes kept changing")
}

// windowsProcess is a process handle opened for inspection and termination.
type windowsProcess struct{ h windows.Handle }

func openWindowsProcess(pid uint32) (daemonProcess, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return nil, err
	}
	return windowsProcess{h: h}, nil
}

func (p windowsProcess) Image() (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(p.h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func (p windowsProcess) Created() (time.Time, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.h, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()), nil
}

func (p windowsProcess) Terminate() error { return windows.TerminateProcess(p.h, 1) }

func (p windowsProcess) Wait(d time.Duration) bool {
	ev, err := windows.WaitForSingleObject(p.h, uint32(d.Milliseconds()))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func (p windowsProcess) Close() error { return windows.CloseHandle(p.h) }

// RM_PROCESS_INFO is 668 bytes on every Windows architecture; these fail to
// compile when the Go layout differs.
var (
	_ [unsafe.Sizeof(rmProcessInfo{}) - 668]byte
	_ [668 - unsafe.Sizeof(rmProcessInfo{})]byte
)
