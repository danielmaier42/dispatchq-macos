package main

import "fmt"

// notificationSettingsAction is what the page's notification settings button
// runs in the app window. The page passes nothing: the window opens its own
// operating system's notification pane, so no page-supplied URL or argument
// ever reaches the launcher. A failure comes back to the page as a rejected
// promise, which the page shows instead of a button that silently does nothing.
func notificationSettingsAction(target, place string, launch func(target string) error) func() error {
	return func() error {
		if err := launch(target); err != nil {
			return fmt.Errorf("could not open %s: %w", place, err)
		}
		return nil
	}
}
