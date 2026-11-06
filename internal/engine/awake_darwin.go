//go:build darwin

package engine

import "os/exec"

// holdAwake starts a `caffeinate -i` assertion, which prevents idle system
// sleep, and returns the function that ends it. When caffeinate cannot start
// there is nothing to end.
func holdAwake() (stop func()) {
	cmd := exec.Command("caffeinate", "-i")
	if err := cmd.Start(); err != nil {
		return nil
	}
	go func() { _ = cmd.Wait() }() // reap once it's killed
	return func() { _ = cmd.Process.Kill() }
}
