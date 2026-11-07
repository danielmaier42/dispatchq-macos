//go:build unix && !linux

package proctree

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func processSnapshot(ctx context.Context) []procEntry { return psSnapshot(ctx) }

// StartTime identifies one incarnation of pid by the start time ps reports.
func StartTime(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", errMalformed
	}
	return s, nil
}

// psSnapshot reads the process table through `ps`, because there is no
// portable syscall for "list every process", and /proc does not exist on
// macOS. A snapshot that cannot be taken yields nothing, and the caller falls
// back to killing the harness's own group, which is what dispatchq did before.
func psSnapshot(ctx context.Context) []procEntry {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return nil
	}
	var entries []procEntry
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		pgid, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		entries = append(entries, procEntry{pid: pid, ppid: ppid, pgid: pgid})
	}
	return entries
}
