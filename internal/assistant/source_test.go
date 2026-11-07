package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

var berlin = time.FixedZone("CEST", 2*3600)

// fixture is a store with one task, two runs (one with a log) and an artifact.
func fixture(t *testing.T) (*StoreSource, string) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	dir := t.TempDir()
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tasks = []task.Task{{
		ID: "nightly-abc", Name: "Nightly report", Prompt: "Write the report.", WorkingDir: dir,
		Trigger: task.TriggerCron, Cron: "0 2 * * *", Enabled: true, Permissions: task.PermissionsDefault,
	}}
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 19, 2, 0, 0, 0, berlin)
	end := start.Add(3 * time.Minute)
	for _, r := range []store.Run{
		{RunID: "run-1", TaskID: "nightly-abc", TaskName: "Nightly report", StartedAt: start.Add(-24 * time.Hour),
			Status: store.StatusFailed, Error: "exit status 1", Provider: store.RunProvider{ID: "claude"}},
		{RunID: "run-2", TaskID: "nightly-abc", TaskName: "Nightly report", StartedAt: start, FinishedAt: &end,
			Status: store.StatusSuccess, FinalOutput: "Report written.", CostUSD: 0.42, Provider: store.RunProvider{ID: "claude"}},
	} {
		if err := st.AppendRun(r); err != nil {
			t.Fatal(err)
		}
	}
	log := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the repo."},{"type":"tool_use","name":"Bash"}]}}` + "\nplain line\n"
	if err := os.WriteFile(st.LogPath("run-2"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.ArtifactDir("art-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	page := "<html><head><style>x{}</style></head><body><h1>Weekly</h1><p>All green &amp; fine.</p></body></html>"
	if err := os.WriteFile(filepath.Join(st.ArtifactDir("art-1"), "report.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	err = st.UpdateArtifacts(func(a *[]store.Artifact) error {
		*a = append(*a, store.Artifact{
			ID: "art-1", Title: "Weekly report", FileName: "report.html", RelPath: "art-1/report.html",
			ContentType: "text/html; charset=utf-8", TaskID: "nightly-abc", TaskName: "Nightly report",
			RunID: "run-2", PublishedAt: end,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 14, 3, 0, 0, berlin)
	return &StoreSource{Store: st, Now: func() time.Time { return now }}, dir
}

func TestOverviewNamesTasksRunsAndArtifacts(t *testing.T) {
	src, _ := fixture(t)
	got, err := src.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Now: Mon 2026-10-19 14:03 (UTC+02:00",
		"All runs paused: no",
		"claude (claude-code, default)",
		"task `nightly-abc` \"Nightly report\" · cron \"0 2 * * *\" next 2026-10-20 02:00",
		"last run success 2026-10-19 02:00",
		"run `run-2` of \"Nightly report\" (task `nightly-abc`) · success · started 2026-10-19 02:00 · took 3m0s · on claude · $0.42",
		"error: exit status 1",
		"artifact `art-1` \"Weekly report\" · from \"Nightly report\" · run `run-2`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("overview lacks %q:\n%s", want, got)
		}
	}
	// The newest run comes first.
	if strings.Index(got, "run-2") > strings.Index(got, "run-1") {
		t.Errorf("runs not newest first:\n%s", got)
	}
	// Prompts stay out of the overview.
	if strings.Contains(got, "Write the report.") {
		t.Errorf("overview carries a prompt:\n%s", got)
	}
}

func TestLookups(t *testing.T) {
	src, _ := fixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		l          Lookup
		label      string
		want, lack []string
	}{
		{Lookup{Tool: LookupTask, Arg: "nightly-abc"}, "Read task nightly-abc", []string{"Prompt:\nWrite the report.", "run `run-2`", "run `run-1`"}, nil},
		{Lookup{Tool: LookupTask, Arg: "nope"}, "Read task nope", []string{"No task with id \"nope\""}, nil},
		{Lookup{Tool: LookupRun, Arg: "run-2"}, "Read run run-2", []string{"Final output:\nReport written.", "Reading the repo.", "[tool] Bash", "plain line", "artifact `art-1`"}, nil},
		{Lookup{Tool: LookupRuns, Arg: "failed"}, "Listed runs \"failed\"", []string{"run-1"}, []string{"run-2"}},
		{Lookup{Tool: LookupRuns, Arg: "nightly"}, "Listed runs \"nightly\"", []string{"run-1", "run-2"}, nil},
		{Lookup{Tool: LookupRuns, Arg: "zzz"}, "Listed runs \"zzz\"", []string{"No run matches"}, nil},
		{Lookup{Tool: LookupArtifact, Arg: "art-1"}, "Read artifact art-1", []string{"Weekly", "All green & fine."}, []string{"<h1>", "x{}"}},
		{Lookup{Tool: LookupArtifacts, Arg: "weekly"}, "Listed artifacts \"weekly\"", []string{"art-1"}, nil},
		{Lookup{Tool: LookupArtifacts, Arg: ""}, "Listed artifacts (newest)", []string{"art-1"}, nil},
		{Lookup{Tool: LookupUsage, Arg: "7"}, "Checked usage", []string{"claude: 2 runs, $0.42", "failed 1", "success 1"}, nil},
		{Lookup{Tool: "shell", Arg: "rm"}, "Unknown lookup shell", []string{"no lookup called"}, nil},
	} {
		label, text := src.Lookup(ctx, tc.l)
		if label != tc.label {
			t.Errorf("%v: label %q, want %q", tc.l, label, tc.label)
		}
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Errorf("%v lacks %q:\n%s", tc.l, w, text)
			}
		}
		for _, w := range tc.lack {
			if strings.Contains(text, w) {
				t.Errorf("%v carries %q:\n%s", tc.l, w, text)
			}
		}
	}
}

func TestCheckLabelsAndDropsActions(t *testing.T) {
	src, _ := fixture(t)
	got, _ := src.Check(context.Background(), []Action{
		{Type: ActionOpenView, View: "artifacts", ID: "junk"},
		{Type: ActionOpenView, View: "artifacts"}, // duplicate
		{Type: ActionOpenView, View: "terminal"},  // no such view
		{Type: ActionOpenArtifact, ID: "art-1"},
		{Type: ActionOpenArtifact, ID: "invented"},
		{Type: ActionOpenRun, ID: "run-2"},
		{Type: ActionCancelRun, ID: "run-2"},
		{Type: ActionCancelRun, ID: "run-1"},
		{Type: ActionEnableTask, ID: "nightly-abc"},
		{Type: "format_disk"},
	})
	want := []Action{
		{Type: ActionOpenView, View: "artifacts"},
		{Type: ActionOpenArtifact, ID: "art-1", Label: "Weekly report"},
		{Type: ActionOpenRun, ID: "run-2", Label: "Nightly report · 2026-10-19 02:00"},
		{Type: ActionCancelRun, ID: "run-2", Label: "Nightly report · 2026-10-19 02:00", Problem: "This run is not running."},
		{Type: ActionCancelRun, ID: "run-1", Label: "Nightly report · 2026-10-18 02:00", Problem: "This run is not running."},
		{Type: ActionEnableTask, ID: "nightly-abc", Label: "Nightly report", Problem: "This task is already enabled."},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d actions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Type != want[i].Type || got[i].ID != want[i].ID || got[i].View != want[i].View ||
			got[i].Label != want[i].Label || got[i].Problem != want[i].Problem {
			t.Errorf("action %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCheckLetsARunWaitingForTheLimitBeCanceled(t *testing.T) {
	src, _ := fixture(t)
	r := store.Run{RunID: "run-3", TaskID: "nightly-abc", TaskName: "Nightly report",
		StartedAt: time.Date(2026, 10, 19, 13, 0, 0, 0, berlin), Status: store.StatusRateLimited}
	if err := src.Store.AppendRun(r); err != nil {
		t.Fatal(err)
	}
	got, err := src.Check(context.Background(), []Action{{Type: ActionCancelRun, ID: "run-3"}})
	if err != nil || len(got) != 1 || got[0].Problem != "" {
		t.Fatalf("got %+v, %v; want a cancel without a problem", got, err)
	}
}

func TestCheckAddTask(t *testing.T) {
	src, dir := fixture(t)
	ctx := context.Background()

	got, _ := src.Check(ctx, []Action{{Type: ActionAddTask, Task: &TaskDraft{
		Name: "Tonight", Prompt: "Do it.", WorkingDir: dir, FixedAt: "2026-10-20T02:00",
	}}})
	if len(got) != 1 || got[0].Problem != "" {
		t.Fatalf("got %+v, want a clean proposal", got)
	}
	d := got[0].Task
	if d.Trigger != "fixed" || d.FixedAt != "2026-10-20T02:00:00+02:00" || got[0].Label != "Tonight" {
		t.Fatalf("draft = %+v, want the trigger inferred and the time normalized to the local zone", d)
	}

	for _, tc := range []struct {
		draft TaskDraft
		want  string
	}{
		{TaskDraft{Prompt: "x", WorkingDir: "/does/not/exist"}, "does not exist"},
		{TaskDraft{Prompt: "x", WorkingDir: "relative"}, "absolute path"},
		{TaskDraft{Prompt: "x"}, "missing working_dir"},
		{TaskDraft{WorkingDir: dir}, "missing prompt"},
		{TaskDraft{Prompt: "x", WorkingDir: dir, Cron: "every night"}, "cron"},
		{TaskDraft{Prompt: "x", WorkingDir: dir, FixedAt: "tonight"}, "not a date and time"},
		{TaskDraft{Prompt: "x", WorkingDir: dir, Provider: "ghost"}, "no provider \"ghost\""},
	} {
		d := tc.draft
		got, _ := src.Check(ctx, []Action{{Type: ActionAddTask, Task: &d}})
		if len(got) != 1 || !strings.Contains(got[0].Problem, tc.want) {
			t.Errorf("%+v -> %+v, want a problem mentioning %q", tc.draft, got, tc.want)
		}
	}
}

func TestCheckAddTaskFallsBackToTheDefaultWorkingDir(t *testing.T) {
	src, dir := fixture(t)
	if err := src.Store.UpdateConfig(func(c *store.Config) error {
		c.Settings.DefaultWorkingDir = dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := src.Check(context.Background(), []Action{{Type: ActionAddTask, Task: &TaskDraft{Prompt: "x"}}})
	if len(got) != 1 || got[0].Problem != "" || got[0].Task.WorkingDir != dir || got[0].Task.Trigger != "asap" || got[0].Label != "New task" {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckUpdateTask(t *testing.T) {
	src, _ := fixture(t)
	ctx := context.Background()
	got, _ := src.Check(ctx, []Action{
		{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{Cron: "0 3 * * *"}},
		{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{Cron: "nonsense"}},
		{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{}},
		{Type: ActionUpdateTask, ID: "ghost", Task: &TaskDraft{Name: "x"}},
	})
	if len(got) != 2 {
		t.Fatalf("got %+v, want the empty and unknown updates dropped", got)
	}
	if got[0].Problem != "" || got[0].Task.Trigger != "cron" || got[0].Label != "Nightly report" {
		t.Fatalf("first = %+v (%+v)", got[0], got[0].Task)
	}
	if got[1].Problem == "" {
		t.Fatalf("an invalid cron passed: %+v", got[1])
	}
}

func TestCheckPauseSwitches(t *testing.T) {
	src, _ := fixture(t)
	got, _ := src.Check(context.Background(), []Action{{Type: ActionPauseAll, ID: "x"}, {Type: ActionResumeAll}})
	if len(got) != 2 || got[0].Problem != "" || got[0].ID != "" || got[1].Problem == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestLogTailKeepsTheEnd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	var b strings.Builder
	for range 500 {
		b.WriteString(`{"type":"assistant","message":{"content":[{"type":"text","text":"step"}]}}` + "\n")
	}
	b.WriteString(`{"type":"result","text":"the end"}` + "\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := logTail(p, 100)
	if !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "the end") || len([]rune(got)) != 101 {
		t.Fatalf("tail = %q", got)
	}
	if logTail(filepath.Join(t.TempDir(), "missing"), 100) != "" {
		t.Fatal("a missing log produced text")
	}
}
