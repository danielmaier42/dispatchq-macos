package main

import (
	"testing"

	"github.com/danielmaier42/claudeq/internal/system"
)

// macOS has every platform service; a nil field would switch a feature off.
func TestDarwinPlatformHasEveryService(t *testing.T) {
	p := newPlatform(system.Real{})
	checks := map[string]bool{
		"service":           p.service != nil,
		"waker":             p.waker != nil,
		"sleep":             p.sleep != nil,
		"desktop":           p.desktop != nil,
		"requestNotifyAuth": p.requestNotifyAuth != nil,
		"notifyStatus":      p.notifyStatus != nil,
		"chooseFolder":      p.chooseFolder != nil,
		"saveFile":          p.saveFile != nil,
		"openTerminal":      p.openTerminal != nil,
		"warmFileAccess":    p.warmFileAccess != nil,
		"osVersion":         p.osVersion != nil,
		"uninstall":         p.uninstall != nil,
		"selfUpdate":        p.selfUpdate,
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("macOS platform has no %s", name)
		}
	}
}
