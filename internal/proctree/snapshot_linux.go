//go:build linux

package proctree

import (
	"context"
	"os"
	"strconv"
	"strings"
)

// processSnapshot reads the process table from /proc. A minimal Linux
// distribution may have no `ps`, and the WSL helper must still find its tree.
func processSnapshot(_ context.Context) []procEntry {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []procEntry
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		st, err := readStat(pid)
		if err != nil {
			continue
		}
		out = append(out, procEntry{pid: pid, ppid: st.ppid, pgid: st.pgid})
	}
	return out
}

type procStat struct {
	ppid, pgid int
	start      string
}

// readStat parses /proc/<pid>/stat. The command name in parentheses may hold
// spaces and parentheses itself, so the fields are read after the last ')'.
func readStat(pid int) (procStat, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procStat{}, err
	}
	return parseStat(string(raw))
}

func parseStat(s string) (procStat, error) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procStat{}, errMalformed
	}
	// Fields after the name: state(3) ppid(4) pgrp(5) ... starttime(22).
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return procStat{}, errMalformed
	}
	ppid, err1 := strconv.Atoi(f[1])
	pgid, err2 := strconv.Atoi(f[2])
	if err1 != nil || err2 != nil {
		return procStat{}, errMalformed
	}
	return procStat{ppid: ppid, pgid: pgid, start: f[19]}, nil
}

// StartTime identifies one incarnation of pid: its start time in clock ticks
// since boot. A reused pid has a different one.
func StartTime(pid int) (string, error) {
	st, err := readStat(pid)
	if err != nil {
		return "", err
	}
	return st.start, nil
}
