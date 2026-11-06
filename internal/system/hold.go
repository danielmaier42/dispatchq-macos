package system

import (
	"errors"
	"fmt"
	"os/exec"
)

// Hold keeps a helper process running for as long as a system assertion must
// last, such as `caffeinate -i`, which holds off idle sleep until it exits.
// Inhibit starts the process and returns the function that ends it.
type Hold struct {
	Argv []string
}

// Inhibit starts the helper process. The returned release kills it. The
// process is reaped in the background, so an early exit leaves no zombie.
func (h Hold) Inhibit() (release func(), err error) {
	if len(h.Argv) == 0 {
		return nil, errors.New("hold: no command")
	}
	cmd := exec.Command(h.Argv[0], h.Argv[1:]...) //nolint:gosec // a fixed helper chosen by the platform wiring
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", h.Argv[0], err)
	}
	go func() { _ = cmd.Wait() }()
	return func() { _ = cmd.Process.Kill() }, nil
}
