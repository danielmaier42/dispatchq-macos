//go:build unix

// Package proctree finds and stops a process's whole descendant tree on Unix
// systems. The daemon uses it for a run's harness on macOS; the WSL helper
// (cmd/dispatchq-wsl) uses the same code for the tree it owns inside Linux.
package proctree

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// killTreeTimeout bounds the whole teardown, including the check afterwards. A
// cancel has to return promptly even when the process table is being awkward.
const killTreeTimeout = 5 * time.Second

// KillTree terminates root, every process descended from it, and every process
// group any of them belongs to.
//
// The order is leaf-first, so a parent cannot spawn a replacement for a child
// that has just been killed. Groups are collected before anything is signalled,
// because a dying process disappears from the table and would take the record of
// its group with it.
func KillTree(root int) {
	ctx, cancel := context.WithTimeout(context.Background(), killTreeTimeout)
	defer cancel()

	snapshot := processSnapshot(ctx)
	generations := descendants(snapshot, root)

	// Leaf-first: the deepest generation first, the harness itself last.
	for i := len(generations) - 1; i >= 0; i-- {
		for _, p := range generations[i] {
			if p.pgid > 1 && p.pgid != root {
				_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
			}
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
	}
	// The harness's own group last, so its children are already gone.
	_ = syscall.Kill(-root, syscall.SIGKILL)
	_ = syscall.Kill(root, syscall.SIGKILL)

	// Anything that survived the first pass gets a second one: a process can be
	// mid-fork when the first signal arrives, and the child born from that fork
	// is exactly the orphan this is meant to prevent.
	if left := descendants(processSnapshot(ctx), root); len(left) > 0 {
		for i := len(left) - 1; i >= 0; i-- {
			for _, p := range left[i] {
				if p.pgid > 1 {
					_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
				}
				_ = syscall.Kill(p.pid, syscall.SIGKILL)
			}
		}
	}
}

// procEntry is one row of the process table dispatchq cares about.
type procEntry struct {
	pid, ppid, pgid int
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

// descendants groups the processes below root by depth: index 0 holds root's
// direct children, index 1 their children, and so on. root itself is not
// included — the caller kills it last, by group.
func descendants(snapshot []procEntry, root int) [][]procEntry {
	if len(snapshot) == 0 {
		return nil
	}
	children := map[int][]procEntry{}
	for _, p := range snapshot {
		children[p.ppid] = append(children[p.ppid], p)
	}
	var out [][]procEntry
	seen := map[int]bool{root: true}
	level := []int{root}
	// The depth is bounded so a process table that claims a cycle — a reparented
	// process whose ppid was reused — cannot loop forever.
	for depth := 0; depth < 32 && len(level) > 0; depth++ {
		var generation []procEntry
		var next []int
		for _, parent := range level {
			for _, child := range children[parent] {
				if child.pid <= 1 || seen[child.pid] {
					continue
				}
				seen[child.pid] = true
				generation = append(generation, child)
				next = append(next, child.pid)
			}
		}
		if len(generation) == 0 {
			break
		}
		out = append(out, generation)
		level = next
	}
	return out
}
