//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// The System Settings pane that holds DispatchQ's notification switch and
// alert style.
const (
	notificationSettingsTarget = "x-apple.systempreferences:com.apple.Notifications-Settings.extension"
	notificationSettingsPlace  = "System Settings > Notifications"
)

// launchSettings hands the fixed pane URL to open(1). It waits for open, which
// returns at once, so a failure reaches the page with open's own words.
func launchSettings(target string) error {
	out, err := exec.Command("open", target).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
