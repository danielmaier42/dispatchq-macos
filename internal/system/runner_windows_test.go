//go:build windows

package system

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// startupEnv makes the test binary report how it was started instead of
// running the tests, so a test can start it through a runner and see what
// Windows handed the child.
const startupEnv = "DISPATCHQ_TEST_REPORT_STARTUP"

func TestMain(m *testing.M) {
	if os.Getenv(startupEnv) == "1" {
		reportStartup()
		return
	}
	os.Exit(m.Run())
}

// reportStartup prints whether the process was told to start hidden and
// whether it has a console window.
func reportStartup() {
	var si windows.StartupInfo
	if err := windows.GetStartupInfo(&si); err != nil {
		fmt.Printf("error %v", err)
		return
	}
	hidden := si.Flags&windows.STARTF_USESHOWWINDOW != 0 && si.ShowWindow == windows.SW_HIDE
	console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	fmt.Printf("hidden=%v console=%v", hidden, console != 0)
}

func startedBy(t *testing.T, r Runner) string {
	t.Helper()
	t.Setenv(startupEnv, "1")
	out, err := r.Run(context.Background(), os.Args[0])
	if err != nil {
		t.Fatalf("run the child: %v (%s)", err, out)
	}
	return strings.TrimSpace(string(out))
}

// A background command opens no console and starts hidden.
func TestRealStartsChildHiddenWithoutConsole(t *testing.T) {
	if got := startedBy(t, Real{}); got != "hidden=true console=false" {
		t.Fatalf("Real child reports %q, want hidden=true console=false", got)
	}
}

// A command that shows a dialog opens no console either, but must not start
// hidden: Windows applies SW_HIDE to the first window the child shows, which
// left the folder dialog invisible while the dashboard waited for it.
func TestInteractiveStartsChildVisibleWithoutConsole(t *testing.T) {
	if got := startedBy(t, ShowingWindows(Real{})); got != "hidden=false console=false" {
		t.Fatalf("Interactive child reports %q, want hidden=false console=false", got)
	}
}
