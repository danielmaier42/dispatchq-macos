//go:build windows

package main

import (
	"testing"

	"github.com/danielmaier42/claudeq/internal/system"
)

// The Windows platform wires the services this edition has, and leaves the
// rest nil so the daemon and the dashboard treat them as unavailable.
func TestWindowsPlatformServices(t *testing.T) {
	p := newPlatform(system.Real{})
	for name, ok := range map[string]bool{
		"service":          p.service != nil,
		"sleep":            p.sleep != nil,
		"chooseFolder":     p.chooseFolder != nil,
		"saveFile":         p.saveFile != nil,
		"osVersion":        p.osVersion != nil,
		"notifyStatus":     p.notifyStatus != nil,
		"stopStrayDaemons": p.stopStrayDaemons != nil,
	} {
		if !ok {
			t.Errorf("Windows platform has no %s", name)
		}
	}
	if p.waker != nil || p.desktop != nil || p.openTerminal != nil || p.uninstall != nil || p.selfUpdate {
		t.Errorf("Windows platform wires an unimplemented service: %+v", p)
	}
}
