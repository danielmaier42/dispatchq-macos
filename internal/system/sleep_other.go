//go:build !darwin && !windows

package system

// defaultSleepInhibitor is nil: there is no implementation for this system yet.
func defaultSleepInhibitor() SleepInhibitor { return nil }
