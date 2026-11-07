package system

// SleepInhibitor holds off idle system sleep. Inhibit takes the hold, and the
// returned release gives it back. Hold and the Windows execution-state hold
// satisfy it, and so does engine.SleepInhibitor.
type SleepInhibitor interface {
	Inhibit() (release func(), err error)
}

// DefaultSleepInhibitor is the sleep hold of the operating system this binary
// runs on, or nil where none is implemented. The daemon and the CLI's run-now
// both take it from here, so a manual run is protected from idle sleep like a
// daemon run.
func DefaultSleepInhibitor() SleepInhibitor { return defaultSleepInhibitor() }
