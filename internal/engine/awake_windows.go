//go:build windows

package engine

import (
	"runtime"

	"golang.org/x/sys/windows"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// holdAwake asks Windows not to idle-sleep the system until stop is called.
// SetThreadExecutionState belongs to the calling thread, so a goroutine pins
// itself to one OS thread, sets the state there and clears it on the same
// thread when told to stop. The display may still turn off.
func holdAwake() (stop func()) {
	release := make(chan struct{})
	set := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	ready := make(chan bool, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		r, _, _ := set.Call(uintptr(esContinuous | esSystemRequired))
		ready <- r != 0
		if r == 0 {
			return
		}
		<-release
		_, _, _ = set.Call(uintptr(esContinuous))
	}()
	if !<-ready {
		return nil
	}
	return func() { close(release) }
}
