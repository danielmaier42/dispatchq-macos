//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

// The Settings page for notifications (System > Notifications), from the
// ms-settings URI scheme Microsoft documents for launching Windows Settings:
// https://learn.microsoft.com/windows/apps/develop/launch/launch-settings
const (
	notificationSettingsTarget = "ms-settings:notifications"
	notificationSettingsPlace  = "Settings > System > Notifications"
)

// sFalse is S_FALSE: COM was already initialized on this thread, and the call
// still has to be balanced by CoUninitialize.
const sFalse = syscall.Errno(1)

// launchSettings opens the fixed ms-settings URI through ShellExecuteW, which
// reports a failure (no handler registered, access denied) where rundll32
// would exit without a word. ShellExecute can hand the URI to COM-activated
// shell extensions, so COM is initialized on this thread first, as the
// ShellExecuteW documentation asks.
func launchSettings(target string) error {
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
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
