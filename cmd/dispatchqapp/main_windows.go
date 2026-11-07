//go:build windows

// Command dispatchqapp is the native Windows window for dispatchq: a WebView2
// window that shows the dashboard served by the dispatchqd daemon, the Windows
// counterpart of the macOS WKWebView app. It starts the daemon if it is not
// already running, and reads the Windows accent colour for the page.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/danielmaier42/claudeq/internal/notify"
)

func main() {
	runtime.LockOSThread()
	// The window takes the AppUserModelID its Start menu shortcut and the
	// daemon's toasts carry, so all three are one app to the taskbar and to
	// Settings > System > Notifications. A failure only affects grouping.
	_ = notify.SetProcessAppID(notify.WindowsAppID)
	ensureDaemon()

	w := webview.New(false)
	if w == nil {
		fail("The DispatchQ window could not be created. It needs the Microsoft Edge WebView2 Runtime, " +
			"which comes with Windows 11 and current Microsoft Edge. Install it from " +
			"https://developer.microsoft.com/microsoft-edge/webview2/ and start DispatchQ again.")
		return
	}
	defer w.Destroy()
	w.SetTitle("DispatchQ")
	w.SetSize(1120, 760, webview.HintNone)

	// Links that leave the dashboard open in the default browser.
	_ = w.Bind("cqOpenExternal", func(u string) {
		if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
		}
	})
	// The macOS-only hooks the page asks for. A Windows toast opens its link
	// itself and has no artifact or run click target, so there is never a
	// pending click, and the title bar follows the system theme without a pin.
	_ = w.Bind("cqTakePendingNotification", func() string { return "" })
	_ = w.Bind("cqSetAppearance", func(string) {})
	_ = w.Bind("cqReadAccent", func() string { return accentHex() })
	w.Init(`
		window.cqApplyAccent = function(hex){
			window.cqSystemAccent = hex || '';
			window.dispatchEvent(new CustomEvent('cq:system-accent', {detail: hex || ''}));
		};
		window.addEventListener('DOMContentLoaded', async function(){
			try { window.cqApplyAccent(await window.cqReadAccent()); } catch (e) {}
		});
	`)

	w.Navigate(dashboardURL)
	w.Run()
}

// fail tells the user why the window cannot open. The app has no console, so a
// message box is the only place the words can go.
func fail(msg string) {
	text, _ := windows.UTF16PtrFromString(msg)
	title, _ := windows.UTF16PtrFromString("DispatchQ")
	_, _ = windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONERROR)
	fmt.Fprintln(os.Stderr, "dispatchqapp:", msg)
	os.Exit(1)
}

// exeSuffix is the extension of an executable next to this one.
const exeSuffix = ".exe"

// daemonCommand starts the daemon without a console window and detached from
// the window, so closing the window leaves scheduling running. The daemon
// writes its own log to the data directory.
func daemonCommand(bin string) *exec.Cmd {
	cmd := exec.Command(bin, "run")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	return cmd
}

// accentHex reads the Windows accent colour, "" when it cannot be read.
// DWM stores it as 0xAABBGGRR.
func accentHex() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\DWM`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetIntegerValue("AccentColor")
	if err != nil {
		return ""
	}
	return abgrToHex(uint32(v))
}

// abgrToHex converts a DWM 0xAABBGGRR value to "#rrggbb".
func abgrToHex(v uint32) string {
	return fmt.Sprintf("#%02x%02x%02x", v&0xff, (v>>8)&0xff, (v>>16)&0xff)
}
