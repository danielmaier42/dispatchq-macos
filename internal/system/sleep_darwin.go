//go:build darwin

package system

// defaultSleepInhibitor holds a `caffeinate -i` assertion, which prevents idle
// system sleep for as long as it runs.
func defaultSleepInhibitor() SleepInhibitor { return Hold{Argv: []string{"caffeinate", "-i"}} }
