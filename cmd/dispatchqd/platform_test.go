package main

import (
	"errors"
	"testing"
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
