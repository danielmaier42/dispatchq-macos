//go:build !darwin && !linux && !windows

package main

import (
	"testing"

	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/system"
)

// A system without an integration offers no service that would call a macOS
// tool, and says so instead of failing on every use.
func TestOtherPlatformOffersNoMacTools(t *testing.T) {
	p := newPlatform(system.Real{})
	if p.service != nil || p.waker != nil || p.sleep != nil || p.desktop != nil || p.requestNotifyAuth != nil ||
		p.chooseFolder != nil || p.saveFile != nil || p.openTerminal != nil ||
		p.warmFileAccess != nil || p.osVersion != nil || p.uninstall != nil || p.selfUpdate {
		t.Fatalf("platform = %+v, want no system services", p)
	}
	if got := p.notifyStatus(); got != notify.AuthorizationUnsupported {
		t.Fatalf("notifyStatus = %q, want %q", got, notify.AuthorizationUnsupported)
	}
}
