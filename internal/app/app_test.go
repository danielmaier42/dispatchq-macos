package app

import (
	"errors"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func mk(id string) task.Task {
	return task.Task{
		ID: id, Name: id, Prompt: "p", WorkingDir: "/r",
		Trigger: task.TriggerASAP, Enabled: true, Permissions: task.PermissionsDefault,
	}
}

func ids(s *store.Store, t *testing.T) []string {
	t.Helper()
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	out := make([]string, len(cfg.Tasks))
	for i, tk := range cfg.Tasks {
		out[i] = tk.ID
	}
	return out
}

func TestAddRejectsDuplicate(t *testing.T) {
	s := openStore(t)
	if err := AddTask(s, mk("a")); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := AddTask(s, mk("a")); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRemove(t *testing.T) {
	s := openStore(t)
	_ = AddTask(s, mk("a"))
	_ = AddTask(s, mk("b"))
	if err := RemoveTask(s, "a"); err != nil {
		t.Fatalf("RemoveTask: %v", err)
	}
	if got := ids(s, t); len(got) != 1 || got[0] != "b" {
		t.Fatalf("after remove got %v, want [b]", got)
	}
	if err := RemoveTask(s, "missing"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSetEnabled(t *testing.T) {
	s := openStore(t)
	_ = AddTask(s, mk("a"))
	if err := SetEnabled(s, "a", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	cfg, _ := s.LoadConfig()
	if cfg.Tasks[0].Enabled {
		t.Fatal("task should be disabled")
	}
}

func TestSetPausedTouchesOnlyThePauseSwitch(t *testing.T) {
	s := openStore(t)
	_ = AddTask(s, mk("a"))
	if err := s.UpdateConfig(func(cfg *store.Config) error {
		cfg.Settings.SystemPrompt = "be brief"
		return nil
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := SetPaused(s, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	cfg, _ := s.LoadConfig()
	if !cfg.Settings.Paused {
		t.Fatal("pause switch not set")
	}
	if cfg.Settings.SystemPrompt != "be brief" || len(cfg.Tasks) != 1 {
		t.Fatalf("SetPaused disturbed the rest of the config: %+v", cfg)
	}

	if err := SetPaused(s, false); err != nil {
		t.Fatalf("SetPaused off: %v", err)
	}
	if cfg, _ = s.LoadConfig(); cfg.Settings.Paused {
		t.Fatal("pause switch not cleared")
	}
}

func TestMoveReordersPriority(t *testing.T) {
	s := openStore(t)
	_ = AddTask(s, mk("a"))
	_ = AddTask(s, mk("b"))
	_ = AddTask(s, mk("c"))

	// Move c to the top (highest priority).
	if err := Move(s, "c", 0); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got, want := ids(s, t), []string{"c", "a", "b"}; !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	// Move a to the bottom (clamped).
	if err := Move(s, "a", 99); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got, want := ids(s, t), []string{"c", "b", "a"}; !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMarkReadAndAll(t *testing.T) {
	s := openStore(t)
	_ = s.AppendRun(store.Run{RunID: "r1", TaskID: "a", Status: store.StatusSuccess})
	_ = s.AppendRun(store.Run{RunID: "r2", TaskID: "b", Status: store.StatusSuccess})

	if err := MarkRead(s, "r1"); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	st, _ := s.LoadState()
	if !st.IsRead("r1") || st.IsRead("r2") {
		t.Fatal("MarkRead should mark only r1")
	}

	if err := MarkAllRead(s); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	st, _ = s.LoadState()
	if !st.IsRead("r1") || !st.IsRead("r2") {
		t.Fatal("MarkAllRead should mark every run")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEditTask(t *testing.T) {
	s := openStore(t)
	orig := task.Task{
		ID: "one", Name: "One", Prompt: "p", WorkingDir: "/repo",
		Trigger: task.TriggerASAP, Enabled: true, Permissions: task.PermissionsDefault,
	}
	if err := AddTask(s, orig); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	if err := EditTask(s, "one", func(tk *task.Task) error {
		tk.Prompt = "changed"
		return nil
	}); err != nil {
		t.Fatalf("EditTask: %v", err)
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Tasks[0].Prompt != "changed" {
		t.Errorf("prompt = %q", cfg.Tasks[0].Prompt)
	}

	if err := EditTask(s, "missing", func(*task.Task) error { return nil }); err == nil {
		t.Error("expected an error for an unknown task")
	}
	if err := EditTask(s, "one", func(tk *task.Task) error {
		tk.ID = "renamed"
		return nil
	}); err == nil {
		t.Error("expected an error when the id is changed")
	}
	if err := EditTask(s, "one", func(*task.Task) error {
		return errors.New("boom")
	}); err == nil {
		t.Error("expected the apply error to propagate")
	}
	// None of the failures may have touched what is stored.
	after, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(after.Tasks) != 1 || after.Tasks[0].ID != "one" || after.Tasks[0].Prompt != "changed" {
		t.Errorf("store disturbed by failed edits: %+v", after.Tasks)
	}
}

func TestRemoveTaskForgetsSchedulingState(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := AddTask(s, task.Task{ID: "once", Prompt: "p", WorkingDir: "/r", Trigger: task.TriggerASAP, Permissions: task.PermissionsDefault}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateState(func(st *store.State) error {
		st.MarkCompletedOnce("once")
		st.RecordStart("once", time.Now())
		st.MarkCompletedOnce("other")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTask(s, "once"); err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.LastStart("once"); ok || st.IsCompletedOnce("once") {
		t.Errorf("removed task's state survived: %+v", st)
	}
	if !st.IsCompletedOnce("other") {
		t.Error("another task's state was dropped")
	}
	if err := RemoveTask(s, "once"); err == nil {
		t.Error("removing a missing task succeeded")
	}
}

// TestAddTaskUnderAUsedIdStartsClean: a one-shot task leaves the queue when it
// finishes and its state stays behind. A task queued again under that id must
// run, not count as done; a rejected duplicate leaves the queued task's state
// alone.
func TestAddTaskUnderAUsedIdStartsClean(t *testing.T) {
	s := openStore(t)
	if err := s.UpdateState(func(st *store.State) error {
		st.MarkCompletedOnce("again")
		st.RecordStart("again", time.Now())
		st.SetPendingResume("again", "old-session", "claude")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddTask(s, mk("again")); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	_, pending := st.PendingResume("again")
	if _, started := st.LastStart("again"); started || pending || st.IsCompletedOnce("again") {
		t.Fatalf("the new task inherited the old one's state: %+v", st)
	}

	if err := s.UpdateState(func(st *store.State) error {
		st.MarkCompletedOnce("again")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := AddTask(s, mk("again")); err == nil {
		t.Fatal("expected duplicate id error")
	}
	if st, err = s.LoadState(); err != nil {
		t.Fatal(err)
	}
	if !st.IsCompletedOnce("again") {
		t.Fatal("a rejected duplicate wiped the queued task's state")
	}
}
