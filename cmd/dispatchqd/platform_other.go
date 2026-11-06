//go:build !darwin && !linux

package main

import (
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/system"
)

// newPlatform wires a system that dispatchq has no integration for yet. The
// queue, the runs and the dashboard work as everywhere. Autostart, wake from
// sleep, sleep prevention, desktop notifications, the native dialogs and the
// self-update are unavailable.
func newPlatform(system.Runner) platform {
	return platform{
		// Nil here: no sleep hold is implemented for this system yet.
		sleep: system.DefaultSleepInhibitor(),
		// The notify package answers "unsupported" off macOS.
		notifyStatus: notify.MacAuthorization,
	}
}
