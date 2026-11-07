//go:build windows

package system

import (
	"errors"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// executionStateHold asks Windows not to idle-sleep the system. It needs no
// helper process: SetThreadExecutionState belongs to the calling thread, so
// each hold pins a goroutine to one OS thread, sets the state there and clears
// it on the same thread when released. The display may still turn off.
type executionStateHold struct{}

func defaultSleepInhibitor() SleepInhibitor { return executionStateHold{} }

// Inhibit takes the hold and returns the function that ends it. It fails when
// Windows refuses the request.
func (executionStateHold) Inhibit() (release func(), err error) {
	done := make(chan struct{})
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
		<-done
		_, _, _ = set.Call(uintptr(esContinuous))
	}()
	if !<-ready {
		return nil, errors.New("SetThreadExecutionState refused the request")
	}
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }, nil
}
