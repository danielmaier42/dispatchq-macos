//go:build !darwin && !windows

package engine

// holdAwake has no mechanism on this platform.
func holdAwake() (stop func()) { return nil }
