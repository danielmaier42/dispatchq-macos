//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/system"
)

// The Windows platform wires the services this edition has, and leaves the
// rest nil so the daemon and the dashboard treat them as unavailable.
func TestWindowsPlatformServices(t *testing.T) {
	p := newPlatform(system.Real{})
	for name, ok := range map[string]bool{
		"service":           p.service != nil,
		"sleep":             p.sleep != nil,
		"chooseFolder":      p.chooseFolder != nil,
		"saveFile":          p.saveFile != nil,
		"osVersion":         p.osVersion != nil,
		"desktop":           p.desktop != nil,
		"requestNotifyAuth": p.requestNotifyAuth != nil,
		"notifyStatus":      p.notifyStatus != nil,
		"stopStrayDaemons":  p.stopStrayDaemons != nil,
		"stopInstallation":  p.stopInstallation != nil,
		"openInstaller":     p.openInstaller != nil,
	} {
		if !ok {
			t.Errorf("Windows platform has no %s", name)
		}
	}
	if p.waker != nil || p.openTerminal != nil || p.uninstall != nil {
		t.Errorf("Windows platform wires an unimplemented service: %+v", p)
	}
}

// The desktop channel is the Windows toast notifier, not a stand-in.
func TestWindowsPlatformDesktopIsToast(t *testing.T) {
	if _, ok := newPlatform(system.Real{}).desktop.(*notify.Toast); !ok {
		t.Fatal("Windows desktop notifier is not a *notify.Toast")
	}
}

// The update service hands a downloaded setup program to the shell as it is,
// and never starts anything but an .exe.
func TestWindowsOpenInstaller(t *testing.T) {
	var launched []string
	open := openInstaller(func(path string) error { launched = append(launched, path); return nil })
	if err := open(context.Background(), `C:\Users\me\Downloads\dispatchq-2.1.0-windows-x64.exe`); err != nil {
		t.Fatalf("open setup: %v", err)
	}
	for _, path := range []string{`C:\Users\me\Downloads\dispatchq-2.1.0-macos-arm64.pkg`, `C:\x\setup.bat`, `C:\x\setup`} {
		if err := open(context.Background(), path); err == nil {
			t.Errorf("%s was started", path)
		}
	}
	if len(launched) != 1 || launched[0] != `C:\Users\me\Downloads\dispatchq-2.1.0-windows-x64.exe` {
		t.Fatalf("launched = %q", launched)
	}
	failing := openInstaller(func(string) error { return errors.New("access denied") })
	if err := failing(context.Background(), `C:\x\dispatchq-2.1.0-windows-x64.exe`); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("launch error = %v", err)
	}
}
