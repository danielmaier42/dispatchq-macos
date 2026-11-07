//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// uninstallerName is the uninstaller the Windows installer writes next to the
// program files.
const uninstallerName = "Uninstall DispatchQ.exe"

// cmdUninstall starts the installer's uninstaller: it removes the app, the
// autostart entry and the shortcuts, which is what the macOS package does for
// its files. The uninstaller stops a daemon that is still running.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete tasks, run history, artifacts and settings")
	yes := fs.Bool("yes", false, "run silently, without the uninstaller's window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	uninstaller := filepath.Join(filepath.Dir(exe), uninstallerName)
	if _, err := os.Stat(uninstaller); err != nil {
		return fmt.Errorf("no uninstaller next to %s: use Settings > Apps > Installed apps > DispatchQ", exe)
	}
	cmd := exec.Command(uninstaller, uninstallerArgs(*purge, *yes)...)
	// The uninstaller deletes this very program, so it must not be our child.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the uninstaller: %w", err)
	}
	_ = cmd.Process.Release()
	fmt.Println("The uninstaller was started; it removes DispatchQ and stops its background service.")
	return nil
}

// uninstallerArgs maps the CLI flags onto the NSIS uninstaller's switches.
func uninstallerArgs(purge, silent bool) []string {
	var args []string
	if silent {
		args = append(args, "/S")
	}
	if purge {
		args = append(args, "/PURGE")
	}
	return args
}
