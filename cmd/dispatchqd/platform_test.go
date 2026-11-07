package main

import (
	"context"
	"errors"
	"testing"

	"github.com/danielmaier42/claudeq/internal/update"
)

// Without a service manager, install and uninstall refuse before they touch
// anything: install must not stop a running daemon and then fail.
func TestInstallWithoutServiceManagerIsUnsupported(t *testing.T) {
	for name, cmd := range map[string]func(platform) error{
		"install":   cmdInstall,
		"uninstall": cmdUninstall,
	} {
		if err := cmd(platform{}); !errors.Is(err, errUnsupported) {
			t.Errorf("%s without a service manager: err = %v, want errUnsupported", name, err)
		}
	}
}

// The daemon runs an update service only where the platform can open an
// installer and installers are published for the system it was built for.
func TestNewUpdateService(t *testing.T) {
	open := func(context.Context, string) error { return nil }
	winX64 := update.Target{OS: update.OSWindows, Arch: update.ArchX64}
	for _, tc := range []struct {
		name   string
		p      platform
		target update.Target
		want   bool
	}{
		{"windows x64 with an opener", platform{openInstaller: open}, winX64, true},
		{"mac arm64 with an opener", platform{openInstaller: open}, update.Target{OS: update.OSMacOS, Arch: update.ArchARM64}, true},
		{"no opener", platform{}, winX64, false},
		{"unpublished system", platform{openInstaller: open}, update.Target{OS: "linux", Arch: update.ArchX64}, false},
	} {
		if got := newUpdateService(tc.p, tc.target) != nil; got != tc.want {
			t.Errorf("%s: update service = %v, want %v", tc.name, got, tc.want)
		}
	}
}
