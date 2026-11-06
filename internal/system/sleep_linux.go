//go:build linux

package system

// defaultSleepInhibitor holds a logind block lock on idle and sleep for as
// long as `systemd-inhibit` runs. A systemd user service gets this lock
// without a password prompt.
func defaultSleepInhibitor() SleepInhibitor {
	return Hold{Argv: []string{"systemd-inhibit",
		"--what=idle:sleep", "--who=DispatchQ", "--why=Running a queued task", "--mode=block",
		"sleep", "infinity"}}
}
