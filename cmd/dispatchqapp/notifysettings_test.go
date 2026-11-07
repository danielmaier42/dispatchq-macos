//go:build darwin || windows

package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// The button opens the pane of the OS the window runs on, and nothing else.
func TestNotificationSettingsTargetIsTheOSPane(t *testing.T) {
	want := map[string]string{
		"darwin":  "x-apple.systempreferences:com.apple.Notifications-Settings.extension",
		"windows": "ms-settings:notifications",
	}[runtime.GOOS]
	if notificationSettingsTarget != want {
		t.Fatalf("notificationSettingsTarget = %q, want %q on %s", notificationSettingsTarget, want, runtime.GOOS)
	}
}

func TestNotificationSettingsActionLaunchesTheFixedTarget(t *testing.T) {
	var got []string
	act := notificationSettingsAction(notificationSettingsTarget, notificationSettingsPlace, func(target string) error {
		got = append(got, target)
		return nil
	})
	if err := act(); err != nil {
		t.Fatalf("action error = %v, want nil", err)
	}
	if len(got) != 1 || got[0] != notificationSettingsTarget {
		t.Fatalf("launched %q, want exactly [%q]", got, notificationSettingsTarget)
	}
}

// A launcher failure must reach the page with the place to go by hand, never
// vanish: a silently inert button is the bug this action replaces.
func TestNotificationSettingsActionReportsLaunchFailure(t *testing.T) {
	cause := errors.New("no handler for the URI")
	act := notificationSettingsAction(notificationSettingsTarget, notificationSettingsPlace, func(string) error { return cause })
	err := act()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want it to wrap %v", err, cause)
	}
	if !strings.Contains(err.Error(), notificationSettingsPlace) {
		t.Fatalf("error %q does not name %q", err, notificationSettingsPlace)
	}
}
