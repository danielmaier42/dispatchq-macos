// Package app implements the store-level operations behind the dispatchq CLI and
// HTTP API: task CRUD, reordering (priority), and run read-status. Mutations go
// through the store's atomic update helpers so concurrent callers (CLI + API)
// never lose each other's changes.
package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// AddTask appends a task and persists the config. IDs must be unique.
//
// The id may have been used before: a one-shot task leaves the queue when it
// finishes, and its scheduling state stays behind. The new task does not
// inherit it, so one queued again under the id of a task that already ran
// runs instead of counting as done.
func AddTask(s *store.Store, t task.Task) error {
	if err := checkDependencies(s, t); err != nil {
		return err
	}
	// Forgotten before the task enters the queue, never after: once it is in,
	// the scheduler may start it, and its new state must not be wiped. Only an
	// id no queued task holds is cleared, because that state is a dead task's.
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	if indexOf(cfg.Tasks, t.ID) >= 0 {
		return fmt.Errorf("task %q already exists", t.ID)
	}
	if err := forgetTaskState(s, t.ID); err != nil {
		return err
	}
	return s.UpdateConfig(func(cfg *store.Config) error {
		if indexOf(cfg.Tasks, t.ID) >= 0 {
			return fmt.Errorf("task %q already exists", t.ID)
		}
		cfg.Tasks = append(cfg.Tasks, t)
		// A task that names a group joins that group's block rather than sitting
		// alone at the end of the file: the stored order stays the order the
		// queue shows.
		cfg.Tasks = GroupedOrder(cfg.Tasks)
		return nil
	})
}

// ErrUnknownDependency reports a job a task wants to wait for that does not
// exist here.
var ErrUnknownDependency = errors.New("unknown job")

// ErrInvalidDependency reports a job that exists but cannot be waited for.
var ErrInvalidDependency = errors.New("cannot be waited for")

// checkDependencies refuses a task that waits for a job dispatchq has never heard
// of: it would wait for good, which in an unattended queue means a deliverable
// that silently never appears.
//
// It is also what makes a cycle impossible. A job may only name jobs that
// already exist, and its own id is new, so the dependencies can only ever point
// backwards in time.
func checkDependencies(s *store.Store, t task.Task) error {
	if len(t.DependsOn) == 0 {
		return nil
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	var ran map[string]bool
	for _, dep := range t.DependsOn {
		if i := indexOf(cfg.Tasks, dep); i >= 0 {
			// A recurring job has no last run: it finishes and comes round again,
			// so "wait until it is done" has no meaning. Waiting for one would
			// release the join on its first occurrence and never again.
			if cfg.Tasks[i].Trigger == task.TriggerCron {
				return fmt.Errorf("%w: job %q runs on a schedule, so it never reaches a final result to wait for",
					ErrInvalidDependency, dep)
			}
			continue
		}
		// Not in the queue: a one-shot job that has already finished is a
		// perfectly good thing to depend on, so history is the second place to
		// look. It is read at most once.
		if ran == nil {
			runs, err := s.Runs()
			if err != nil {
				return fmt.Errorf("read history: %w", err)
			}
			ran = make(map[string]bool, len(runs))
			for _, r := range runs {
				ran[r.TaskID] = true
			}
		}
		if !ran[dep] {
			return fmt.Errorf("%w %q: a job can only wait for one that already exists", ErrUnknownDependency, dep)
		}
	}
	return nil
}

// RemoveTask deletes a task by id, along with its scheduling state, so the id
// can be reused (by a re-import, say) without inheriting the old task's
// completed-once flag or cron anchor.
func RemoveTask(s *store.Store, id string) error {
	err := s.UpdateConfig(func(cfg *store.Config) error {
		idx := indexOf(cfg.Tasks, id)
		if idx < 0 {
			return fmt.Errorf("task %q not found", id)
		}
		cfg.Tasks = append(cfg.Tasks[:idx], cfg.Tasks[idx+1:]...)
		return nil
	})
	if err != nil {
		return err
	}
	if err := forgetTaskState(s, id); err != nil {
		return err
	}
	return pruneGroupState(s)
}

// forgetTaskState clears the scheduling bookkeeping kept for a task id.
func forgetTaskState(s *store.Store, id string) error {
	return s.UpdateState(func(st *store.State) error {
		st.ForgetTask(id)
		return nil
	})
}

// EditTask applies a change to one existing task in place. The mutation runs
// inside the store's atomic update, so a concurrent edit from the app or another
// CLI call is never clobbered. The task id is fixed: apply must not change it.
func EditTask(s *store.Store, id string, apply func(*task.Task) error) error {
	err := s.UpdateConfig(func(cfg *store.Config) error {
		idx := indexOf(cfg.Tasks, id)
		if idx < 0 {
			return fmt.Errorf("task %q not found", id)
		}
		edited := cfg.Tasks[idx]
		if err := apply(&edited); err != nil {
			return err
		}
		if edited.ID != id {
			return fmt.Errorf("task id cannot be changed (%q -> %q)", id, edited.ID)
		}
		cfg.Tasks[idx] = edited
		// An edit may have moved the task to another group; keep the file in the
		// order the queue renders.
		cfg.Tasks = GroupedOrder(cfg.Tasks)
		return nil
	})
	if err != nil {
		return err
	}
	return pruneGroupState(s)
}

// SetEnabled activates or pauses a task without deleting it (FA-17).
func SetEnabled(s *store.Store, id string, enabled bool) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		idx := indexOf(cfg.Tasks, id)
		if idx < 0 {
			return fmt.Errorf("task %q not found", id)
		}
		cfg.Tasks[idx].Enabled = enabled
		return nil
	})
}

// SetPaused flips the global pause switch: while it is on, the daemon starts no
// run at all (see store.Settings.Paused). Only that one field is touched, so a
// concurrent settings change is not clobbered.
func SetPaused(s *store.Store, paused bool) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		cfg.Settings.Paused = paused
		return nil
	})
}

// SetBackfillUrgency sets the provider urgency above which backfill tasks run
// (see store.Settings.BackfillUrgency). Only that one field is touched, so the
// Dashboard's slider never clobbers a concurrent settings change.
func SetBackfillUrgency(s *store.Store, v float64) error {
	if err := store.CheckBackfillUrgency(v); err != nil {
		return err
	}
	return s.UpdateConfig(func(cfg *store.Config) error {
		cfg.Settings.BackfillUrgency = v
		return nil
	})
}

// Move changes a task's position in the list, which is its priority: index 0 is
// highest (FA-11). The target index is clamped to the valid range.
func Move(s *store.Store, id string, to int) error {
	return moveTask(s, id, nil, to)
}

// MoveToGroup puts a task in a group (empty means ungrouped) and places it at
// index to, both in one write: dragging a row across the queue is one gesture
// and must not be able to half-apply. The group needs no creating — it exists
// as long as a task names it, and disappears with the last one.
func MoveToGroup(s *store.Store, id, group string, to int) error {
	group = strings.TrimSpace(group)
	if err := task.CheckGroup(group); err != nil {
		return err
	}
	if err := moveTask(s, id, &group, to); err != nil {
		return err
	}
	// The group the task just left may have been its last member. Dropping the
	// fold state of a group that no longer exists keeps a name that is used
	// again later from coming back collapsed for no reason.
	return pruneGroupState(s)
}

func moveTask(s *store.Store, id string, group *string, to int) error {
	return s.UpdateConfig(func(cfg *store.Config) error {
		from := indexOf(cfg.Tasks, id)
		if from < 0 {
			return fmt.Errorf("task %q not found", id)
		}
		if group != nil {
			cfg.Tasks[from].Group = *group
		}
		if to < 0 {
			to = 0
		}
		if to > len(cfg.Tasks)-1 {
			to = len(cfg.Tasks) - 1
		}
		moved := cfg.Tasks[from]
		cfg.Tasks = append(cfg.Tasks[:from], cfg.Tasks[from+1:]...)
		cfg.Tasks = append(cfg.Tasks, task.Task{})
		copy(cfg.Tasks[to+1:], cfg.Tasks[to:])
		cfg.Tasks[to] = moved
		cfg.Tasks = GroupedOrder(cfg.Tasks)
		return nil
	})
}

// GroupedOrder puts the tasks of one group together without reordering them
// among themselves: the groups keep the order in which they first appear, and
// so do the tasks inside each one. That is what makes the stored list read like
// the queue looks — the file is the priority order, groups and all.
func GroupedOrder(tasks []task.Task) []task.Task {
	order := blockOrder(tasks)
	out := make([]task.Task, 0, len(tasks))
	for _, g := range order {
		for _, t := range tasks {
			if t.Group == g {
				out = append(out, t)
			}
		}
	}
	return out
}

// RenameGroup gives a group another name, in one write: every task in it moves
// over, and the fold state moves with it. Renaming onto a group that already
// exists merges the two — the tasks join that section, keeping their order.
func RenameGroup(s *store.Store, from, to string) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" {
		return errors.New("group name is required")
	}
	if to == "" {
		return errors.New("new group name is required")
	}
	if err := task.CheckGroup(to); err != nil {
		return err
	}
	if from == to {
		return nil
	}
	merged := false
	err := s.UpdateConfig(func(cfg *store.Config) error {
		order := blockOrder(cfg.Tasks)
		if !slices.Contains(order, from) {
			return fmt.Errorf("group %q not found", from)
		}
		merged = slices.Contains(order, to)
		// A merge moves tasks into a section that is already somewhere in the
		// queue, so that section keeps its place — priority is the order of this
		// list, and a rename must not quietly promote the tasks that were
		// already there. A plain rename keeps the renamed group's own place.
		joined := make([]task.Task, 0, len(cfg.Tasks))
		for _, t := range cfg.Tasks {
			if t.Group == to {
				joined = append(joined, t)
			}
		}
		for i := range cfg.Tasks {
			if cfg.Tasks[i].Group == from {
				cfg.Tasks[i].Group = to
				if merged {
					joined = append(joined, cfg.Tasks[i])
				}
			}
		}
		if merged {
			order = slices.DeleteFunc(order, func(g string) bool { return g == from })
		}
		out := make([]task.Task, 0, len(cfg.Tasks))
		for _, g := range order {
			if g == from || g == to {
				if g == from {
					g = to // the renamed section now answers to the new name
				}
				if merged {
					out = append(out, joined...)
					continue
				}
			}
			for _, t := range cfg.Tasks {
				if t.Group == g {
					out = append(out, t)
				}
			}
		}
		cfg.Tasks = out
		return nil
	})
	if err != nil {
		return err
	}
	// The old name is gone; its fold state goes to the new one, unless that
	// section already existed and has a state of its own. The rename itself is
	// already written, so a failure here says exactly that much.
	if err := s.UpdateState(func(st *store.State) error {
		if was := st.GroupCollapsed(from); was && !merged {
			st.SetGroupCollapsed(to, true)
		}
		st.SetGroupCollapsed(from, false)
		return nil
	}); err != nil {
		return fmt.Errorf("group renamed to %q, but its open/folded state could not be saved: %w", to, err)
	}
	return nil
}

// MoveGroup puts a whole group in front of another one, so the sections can be
// put in the order the work happens in. before names the group to sit in front
// of ("" is the ungrouped section); a nil before moves the group to the end.
// The tasks keep their order inside each group — only the blocks move.
func MoveGroup(s *store.Store, name string, before *string) error {
	if name == "" {
		return errors.New("group name is required")
	}
	if before != nil && *before == name {
		return nil // dropped on itself
	}
	return s.UpdateConfig(func(cfg *store.Config) error {
		order := blockOrder(cfg.Tasks)
		if !slices.Contains(order, name) {
			return fmt.Errorf("group %q not found", name)
		}
		if before != nil && !slices.Contains(order, *before) {
			return fmt.Errorf("group %q not found", *before)
		}
		order = slices.DeleteFunc(order, func(g string) bool { return g == name })
		at := len(order)
		if before != nil {
			at = slices.Index(order, *before)
		}
		order = slices.Insert(order, at, name)
		out := make([]task.Task, 0, len(cfg.Tasks))
		for _, g := range order {
			for _, t := range cfg.Tasks {
				if t.Group == g {
					out = append(out, t)
				}
			}
		}
		cfg.Tasks = out
		return nil
	})
}

// blockOrder lists the groups in the order they first appear, the ungrouped
// section ("") among them.
func blockOrder(tasks []task.Task) []string {
	order, seen := make([]string, 0, 4), map[string]bool{}
	for _, t := range tasks {
		if !seen[t.Group] {
			seen[t.Group] = true
			order = append(order, t.Group)
		}
	}
	return order
}

// SetGroupCollapsed remembers whether a group's section is folded shut in the
// dashboard.
func SetGroupCollapsed(s *store.Store, group string, collapsed bool) error {
	if group == "" {
		return errors.New("group name is required")
	}
	if err := task.CheckGroup(group); err != nil {
		return err
	}
	return s.UpdateState(func(st *store.State) error {
		st.SetGroupCollapsed(group, collapsed)
		return nil
	})
}

// PruneGroups forgets every group no task names any more. Callers that change a
// task's group outside MoveToGroup (an edit, a delete) call it themselves.
func PruneGroups(s *store.Store) error { return pruneGroupState(s) }

// pruneGroupState forgets every group no task names any more.
func pruneGroupState(s *store.Store) error {
	cfg, err := s.LoadConfig()
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, t := range cfg.Tasks {
		if t.Group != "" {
			live[t.Group] = true
		}
	}
	return s.UpdateState(func(st *store.State) error {
		st.KeepGroups(live)
		return nil
	})
}

// MarkRead marks a single run as read (FA-23), and with it the notification
// that announced its outcome.
func MarkRead(s *store.Store, runID string) error {
	if err := s.UpdateState(func(st *store.State) error {
		st.MarkRead(runID)
		return nil
	}); err != nil {
		return err
	}
	carryRead(readRunNotifications(s, map[string]bool{runID: true}))
	return nil
}

// MarkAllRead marks every recorded run as read (FA-24), and every run-outcome
// notification with them.
func MarkAllRead(s *store.Store) error {
	runs, err := s.Runs()
	if err != nil {
		return err
	}
	ids := make([]string, len(runs))
	for i, r := range runs {
		ids[i] = r.RunID
	}
	if err := s.UpdateState(func(st *store.State) error {
		st.MarkAllRead(ids)
		return nil
	}); err != nil {
		return err
	}
	carryRead(readRunNotifications(s, nil))
	return nil
}

func indexOf(tasks []task.Task, id string) int {
	for i, t := range tasks {
		if t.ID == id {
			return i
		}
	}
	return -1
}
