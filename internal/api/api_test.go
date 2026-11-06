package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/claudecode"
	"github.com/danielmaier42/claudeq/internal/schedule"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

func newServer(t *testing.T, runner RunNower) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, Runner: runner}))
	t.Cleanup(srv.Close)
	return srv, st
}

type resp struct {
	Status int
	Body   []byte
}

func (r resp) into(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode response: %v (%s)", err, string(r.Body))
	}
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) resp {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = r.Body.Close() }()
	data, _ := io.ReadAll(r.Body)
	return resp{Status: r.StatusCode, Body: data}
}

func sampleTask(id string) task.Task {
	return task.Task{ID: id, Name: id, Prompt: "p", WorkingDir: "/r", Trigger: task.TriggerASAP, Enabled: true, Permissions: task.PermissionsDefault}
}

func TestAddAndListTasks(t *testing.T) {
	srv, _ := newServer(t, nil)

	if r := do(t, srv, "POST", "/api/tasks", sampleTask("a")); r.Status != http.StatusCreated {
		t.Fatalf("add status = %d", r.Status)
	}
	var tasks []task.Task
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].ID != "a" {
		t.Fatalf("expected [a], got %+v", tasks)
	}
}

func TestAddTaskRejectsUnsafeID(t *testing.T) {
	srv, st := newServer(t, nil)
	bad := sampleTask("team/nightly")
	if r := do(t, srv, "POST", "/api/tasks", bad); r.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.Status)
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 0 {
		t.Errorf("unsafe id was stored: %+v", cfg.Tasks)
	}
}

func TestWarmFileAccessOnAddAndUpdate(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	warmed := make(chan []string, 4)
	srv := httptest.NewServer(handler(Deps{
		Store:          st,
		WarmFileAccess: func(dirs []string) { warmed <- dirs },
	}))
	t.Cleanup(srv.Close)

	add := sampleTask("a")
	add.WorkingDir = "/tmp/added"
	if r := do(t, srv, "POST", "/api/tasks", add); r.Status != http.StatusCreated {
		t.Fatalf("add status = %d", r.Status)
	}
	if dirs := waitWarm(t, warmed); len(dirs) != 1 || dirs[0] != "/tmp/added" {
		t.Fatalf("warm on add = %v, want [/tmp/added]", dirs)
	}

	upd := sampleTask("a")
	upd.WorkingDir = "/tmp/changed"
	if r := do(t, srv, "PUT", "/api/tasks/a", upd); r.Status != http.StatusOK {
		t.Fatalf("update status = %d", r.Status)
	}
	if dirs := waitWarm(t, warmed); len(dirs) != 1 || dirs[0] != "/tmp/changed" {
		t.Fatalf("warm on update = %v, want [/tmp/changed]", dirs)
	}
}

// waitWarm returns the next WarmFileAccess call's dirs, failing if none arrives
// (the hook fires in a goroutine, so the test must wait for it).
func waitWarm(t *testing.T, ch <-chan []string) []string {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("WarmFileAccess was not called")
		return nil
	}
}

// assertNoWarm fails if WarmFileAccess is called within a short window. The hook
// fires in a goroutine, so we give it a moment to (wrongly) arrive.
func assertNoWarm(t *testing.T, ch <-chan []string) {
	t.Helper()
	select {
	case d := <-ch:
		t.Fatalf("WarmFileAccess should not have been called, got %v", d)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestUpdateWarmsOnlyWhenDirChanges verifies the folder-change guard: editing a
// task without touching its working directory does not re-probe (the folder is
// already authorised), while a genuine folder change warms — even for a disabled
// task, since its scheduled run will still need the grant.
func TestUpdateWarmsOnlyWhenDirChanges(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	warmed := make(chan []string, 4)
	srv := httptest.NewServer(handler(Deps{
		Store:          st,
		WarmFileAccess: func(dirs []string) { warmed <- dirs },
	}))
	t.Cleanup(srv.Close)

	add := sampleTask("a")
	add.WorkingDir = "/tmp/dir"
	if r := do(t, srv, "POST", "/api/tasks", add); r.Status != http.StatusCreated {
		t.Fatalf("add status = %d", r.Status)
	}
	waitWarm(t, warmed) // drain the add-time warm

	// Edit only the prompt, same folder → no warm.
	same := sampleTask("a")
	same.WorkingDir = "/tmp/dir"
	same.Prompt = "different prompt"
	if r := do(t, srv, "PUT", "/api/tasks/a", same); r.Status != http.StatusOK {
		t.Fatalf("update status = %d", r.Status)
	}
	assertNoWarm(t, warmed)

	// Change the folder on a now-disabled task → still warms the new folder.
	moved := sampleTask("a")
	moved.WorkingDir = "/tmp/moved"
	moved.Enabled = false
	if r := do(t, srv, "PUT", "/api/tasks/a", moved); r.Status != http.StatusOK {
		t.Fatalf("update status = %d", r.Status)
	}
	if dirs := waitWarm(t, warmed); len(dirs) != 1 || dirs[0] != "/tmp/moved" {
		t.Fatalf("warm on dir change = %v, want [/tmp/moved]", dirs)
	}
}

func TestWarmNowWarmsEnabledTaskFolders(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Seed directly through the store (not the API) so no per-task warm fires and
	// the only hook call we observe is the explicit /api/fs/warm one.
	seed := func(id, dir string, enabled bool) {
		tk := sampleTask(id)
		tk.WorkingDir = dir
		tk.Enabled = enabled
		if err := app.AddTask(st, tk); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("a", "/tmp/a", true)
	seed("b", "/tmp/b", false) // disabled → must be excluded
	seed("c", "/tmp/c", true)

	warmed := make(chan []string, 4)
	srv := httptest.NewServer(handler(Deps{
		Store:          st,
		WarmFileAccess: func(dirs []string) { warmed <- dirs },
	}))
	t.Cleanup(srv.Close)

	if r := do(t, srv, "POST", "/api/fs/warm", nil); r.Status != http.StatusAccepted {
		t.Fatalf("warm status = %d, want 202", r.Status)
	}
	dirs := waitWarm(t, warmed)
	if len(dirs) != 2 || dirs[0] != "/tmp/a" || dirs[1] != "/tmp/c" {
		t.Fatalf("warmed dirs = %v, want [/tmp/a /tmp/c] (enabled only)", dirs)
	}
}

func TestWarmNowNoHookIsNoContent(t *testing.T) {
	srv, _ := newServer(t, nil) // Deps without WarmFileAccess
	if r := do(t, srv, "POST", "/api/fs/warm", nil); r.Status != http.StatusNoContent {
		t.Fatalf("warm status = %d, want 204 when no hook wired", r.Status)
	}
}

func TestListTasksHidesRunning(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, ActiveTasks: func() []string { return []string{"a"} }}))
	t.Cleanup(srv.Close)
	do(t, srv, "POST", "/api/tasks", sampleTask("a"))
	do(t, srv, "POST", "/api/tasks", sampleTask("b"))

	var tasks []task.Task
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].ID != "b" {
		t.Fatalf("running task 'a' should be hidden from the queue, got %v", tasks)
	}
}

func TestListTasksKeepsRunningCron(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, ActiveTasks: func() []string { return []string{"c"} }}))
	t.Cleanup(srv.Close)
	cron := sampleTask("c")
	cron.Trigger = task.TriggerCron
	cron.Cron = "0 20 * * *"
	do(t, srv, "POST", "/api/tasks", cron)

	var tasks []struct {
		task.Task
		Running bool `json:"running"`
	}
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].ID != "c" {
		t.Fatalf("running cron task should stay in the queue, got %v", tasks)
	}
	if !tasks[0].Running {
		t.Fatal("running cron task should be flagged running")
	}
}

func TestListTasksReportsCronNextRun(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	cron := sampleTask("c")
	cron.Trigger = task.TriggerCron
	cron.Cron = "0 20 * * *"
	do(t, srv, "POST", "/api/tasks", cron)

	asap := sampleTask("a") // default trigger, no schedule
	do(t, srv, "POST", "/api/tasks", asap)

	var tasks []struct {
		ID      string     `json:"id"`
		NextRun *time.Time `json:"next_run"`
	}
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)

	byID := map[string]*time.Time{}
	for _, tk := range tasks {
		byID[tk.ID] = tk.NextRun
	}
	if byID["c"] == nil {
		t.Fatal("cron task should report a next_run")
	}
	if !byID["c"].After(time.Now()) {
		t.Fatalf("cron next_run should be in the future, got %v", byID["c"])
	}
	if byID["a"] != nil {
		t.Fatalf("non-cron task should not report next_run, got %v", byID["a"])
	}
}

func TestListTasksReportsCronLastRun(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	cron := sampleTask("c")
	cron.Trigger = task.TriggerCron
	cron.Cron = "0 20 * * *"
	do(t, srv, "POST", "/api/tasks", cron)

	var tasks []struct {
		ID      string     `json:"id"`
		LastRun *time.Time `json:"last_run"`
	}
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].LastRun != nil {
		t.Fatalf("a cron task that never ran should report no last_run, got %v", tasks)
	}

	ran := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	if err := st.UpdateState(func(cur *store.State) error {
		// The scheduling anchor alone must not count as an execution.
		cur.RecordStart("c", time.Now())
		cur.RecordRun("c", ran)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	tasks = nil
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].LastRun == nil {
		t.Fatalf("cron task should report a last_run, got %v", tasks)
	}
	if !tasks[0].LastRun.Equal(ran) {
		t.Fatalf("last_run = %v, want %v", tasks[0].LastRun, ran)
	}
}

func TestHealthReportsWakeError(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, WakeError: func() string { return "pmset: sudo password required" }}))
	t.Cleanup(srv.Close)

	var got map[string]any
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if got["wake_error"] != "pmset: sudo password required" {
		t.Fatalf("wake_error = %q, want the dep's value", got["wake_error"])
	}
}

func TestHealthReportsThePlatform(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for dep, want := range map[string]string{"": "darwin", "darwin": "darwin", "linux": "linux"} {
		srv := httptest.NewServer(handler(Deps{Store: st, Platform: dep}))
		var got map[string]any
		do(t, srv, "GET", "/api/health", nil).into(t, &got)
		srv.Close()
		if got["platform"] != want {
			t.Errorf("Platform %q: platform = %v, want %q", dep, got["platform"], want)
		}
	}
}

func TestHealthReportsNotifyStatus(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, NotifyStatus: func() string { return "denied" }}))
	t.Cleanup(srv.Close)

	var got map[string]any
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if got["notify_status"] != "denied" {
		t.Fatalf("notify_status = %q, want the dep's value", got["notify_status"])
	}
}

// Without the dep (a build that cannot ask macOS) health must still answer, with
// an empty status rather than a made-up one.
func TestHealthWithoutNotifyStatusDep(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)

	var got map[string]any
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if got["notify_status"] != "" {
		t.Fatalf("notify_status = %q, want empty", got["notify_status"])
	}
}

func TestAddInvalidTaskRejected(t *testing.T) {
	srv, _ := newServer(t, nil)
	bad := sampleTask("x")
	bad.Prompt = ""
	if r := do(t, srv, "POST", "/api/tasks", bad); r.Status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", r.Status)
	}
}

func TestUpdateTask(t *testing.T) {
	srv, st := newServer(t, nil)
	do(t, srv, "POST", "/api/tasks", sampleTask("a"))

	edited := sampleTask("a")
	edited.Name = "Renamed"
	edited.Prompt = "new prompt"
	if r := do(t, srv, "PUT", "/api/tasks/a", edited); r.Status != http.StatusOK {
		t.Fatalf("update status = %d (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 1 || cfg.Tasks[0].Name != "Renamed" || cfg.Tasks[0].Prompt != "new prompt" {
		t.Fatalf("task not updated: %+v", cfg.Tasks)
	}

	// Updating a missing task fails.
	if r := do(t, srv, "PUT", "/api/tasks/missing", sampleTask("missing")); r.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 updating missing task, got %d", r.Status)
	}
}

func TestEnableDisableMoveDelete(t *testing.T) {
	srv, st := newServer(t, nil)
	do(t, srv, "POST", "/api/tasks", sampleTask("a"))
	do(t, srv, "POST", "/api/tasks", sampleTask("b"))

	if r := do(t, srv, "POST", "/api/tasks/a/disable", nil); r.Status != http.StatusNoContent {
		t.Fatalf("disable status = %d", r.Status)
	}
	if r := do(t, srv, "POST", "/api/tasks/b/move?to=0", nil); r.Status != http.StatusNoContent {
		t.Fatalf("move status = %d", r.Status)
	}
	cfg, _ := st.LoadConfig()
	if cfg.Tasks[0].ID != "b" {
		t.Fatalf("move failed, order = %s,%s", cfg.Tasks[0].ID, cfg.Tasks[1].ID)
	}
	if cfg.Tasks[1].Enabled {
		t.Fatal("task a should be disabled")
	}
	if r := do(t, srv, "DELETE", "/api/tasks/a", nil); r.Status != http.StatusNoContent {
		t.Fatalf("delete status = %d", r.Status)
	}
}

func TestRunsAndReadAll(t *testing.T) {
	srv, st := newServer(t, nil)
	_ = st.AppendRun(store.Run{RunID: "r1", TaskID: "a", TaskName: "a", StartedAt: time.Now(), Status: store.StatusSuccess})

	var views []runView
	do(t, srv, "GET", "/api/runs", nil).into(t, &views)
	if len(views) != 1 || !views[0].Unread {
		t.Fatalf("expected 1 unread run, got %+v", views)
	}

	if r := do(t, srv, "POST", "/api/runs/read-all", nil); r.Status != http.StatusNoContent {
		t.Fatalf("read-all status = %d", r.Status)
	}
	do(t, srv, "GET", "/api/runs", nil).into(t, &views)
	if views[0].Unread {
		t.Fatal("run should be read after read-all")
	}
}

// The run list is polled by several views every few seconds, so it carries no
// prompts: they are the bulk of a long history and no row shows them.
func TestListRunsOmitsPromptButKeepsTheTaskSnapshot(t *testing.T) {
	srv, st := newServer(t, nil)
	snap := sampleTask("a")
	snap.Prompt = "the whole job, in full"
	_ = st.AppendRun(store.Run{RunID: "r1", TaskID: "a", TaskName: "a", StartedAt: time.Now(),
		Status: store.StatusSuccess, Task: &snap})

	var views []runView
	do(t, srv, "GET", "/api/runs", nil).into(t, &views)
	if len(views) != 1 || views[0].Task == nil {
		t.Fatalf("expected one run with its task snapshot, got %+v", views)
	}
	if views[0].Task.Prompt != "" {
		t.Fatalf("the list carries the prompt: %q", views[0].Task.Prompt)
	}
	if views[0].Task.WorkingDir != "/r" {
		t.Fatalf("working dir = %q, want the snapshot's — the list still needs it", views[0].Task.WorkingDir)
	}
	if strings.Contains(string(do(t, srv, "GET", "/api/runs", nil).Body), "the whole job") {
		t.Fatal("the prompt text is in the list response")
	}
}

// Replay and the log sheet need the prompt, so one run can be asked for in full.
func TestGetRunCarriesThePrompt(t *testing.T) {
	srv, st := newServer(t, nil)
	snap := sampleTask("a")
	snap.Prompt = "the whole job, in full"
	_ = st.AppendRun(store.Run{RunID: "r1", TaskID: "a", TaskName: "a", StartedAt: time.Now(),
		Status: store.StatusSuccess, Task: &snap})

	var view runView
	r := do(t, srv, "GET", "/api/runs/r1", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &view)
	if view.Task == nil || view.Task.Prompt != "the whole job, in full" {
		t.Fatalf("run %+v does not carry its prompt", view)
	}
	if view.RunID != "r1" || !view.Unread {
		t.Fatalf("run = %+v, want r1 and unread", view)
	}
}

func TestGetRunNotFound(t *testing.T) {
	srv, _ := newServer(t, nil)
	if r := do(t, srv, "GET", "/api/runs/nope", nil); r.Status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", r.Status)
	}
}

// The app window asks /api/ping before it decides to start a daemon, so it
// answers without a store, a provider or a dependency of any kind.
func TestPingAnswersWithoutDependencies(t *testing.T) {
	srv := httptest.NewServer(handler(Deps{}))
	t.Cleanup(srv.Close)
	if r := do(t, srv, "GET", "/api/ping", nil); r.Status != http.StatusNoContent {
		t.Fatalf("ping status = %d (%s)", r.Status, r.Body)
	}
}

func TestRunLogNotFound(t *testing.T) {
	srv, _ := newServer(t, nil)
	if r := do(t, srv, "GET", "/api/runs/nope/log", nil); r.Status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", r.Status)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	srv, _ := newServer(t, nil)
	in := store.Settings{SystemPrompt: "be brief", HeartbeatMinutes: 30, ArtifactNotifyDisabled: true}
	if r := do(t, srv, "PUT", "/api/settings", in); r.Status != http.StatusOK {
		t.Fatalf("put status = %d", r.Status)
	}
	var out store.Settings
	do(t, srv, "GET", "/api/settings", nil).into(t, &out)
	if out.SystemPrompt != "be brief" || out.HeartbeatMinutes != 30 || !out.ArtifactNotifyDisabled {
		t.Fatalf("settings round-trip mismatch: %+v", out)
	}
}

func TestPutSettingsRefusesAnEnabledChannelThatCannotDeliver(t *testing.T) {
	srv, st := newServer(t, nil)
	body := map[string]any{"webhook": map[string]any{"enabled": true, "url": ""}}
	r := do(t, srv, "PUT", "/api/settings", body)
	if r.Status != http.StatusBadRequest {
		t.Fatalf("put status = %d, want 400 (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if cfg.Settings.Webhook.Enabled {
		t.Fatal("a rejected settings payload must not be persisted")
	}
}

func TestPutSettingsAppearance(t *testing.T) {
	srv, st := newServer(t, nil)
	if r := do(t, srv, "PUT", "/api/settings", map[string]any{"appearance": "dark"}); r.Status != http.StatusOK {
		t.Fatalf("put dark status = %d (%s)", r.Status, r.Body)
	}
	if cfg, _ := st.LoadConfig(); cfg.Settings.Appearance != store.AppearanceDark {
		t.Fatalf("appearance = %q, want dark", cfg.Settings.Appearance)
	}
	if r := do(t, srv, "PUT", "/api/settings", map[string]any{"appearance": "sepia"}); r.Status != http.StatusBadRequest {
		t.Fatalf("put sepia status = %d, want 400 (%s)", r.Status, r.Body)
	}
	if cfg, _ := st.LoadConfig(); cfg.Settings.Appearance != store.AppearanceDark {
		t.Fatal("a rejected appearance must not replace the stored one")
	}
}

func TestPutSettingsAccent(t *testing.T) {
	srv, st := newServer(t, nil)
	if r := do(t, srv, "PUT", "/api/settings", map[string]any{"accent": "#FF9F0A"}); r.Status != http.StatusOK {
		t.Fatalf("put accent status = %d (%s)", r.Status, r.Body)
	}
	if cfg, _ := st.LoadConfig(); cfg.Settings.Accent != "#ff9f0a" {
		t.Fatalf("accent = %q, want it stored lower-case", cfg.Settings.Accent)
	}
	if r := do(t, srv, "PUT", "/api/settings", map[string]any{"accent": "orange"}); r.Status != http.StatusBadRequest {
		t.Fatalf("put named colour status = %d, want 400 (%s)", r.Status, r.Body)
	}
	if cfg, _ := st.LoadConfig(); cfg.Settings.Accent != "#ff9f0a" {
		t.Fatal("a rejected accent must not replace the stored one")
	}
	if r := do(t, srv, "PUT", "/api/settings", map[string]any{"accent": ""}); r.Status != http.StatusOK {
		t.Fatalf("put empty accent status = %d (%s)", r.Status, r.Body)
	}
	if cfg, _ := st.LoadConfig(); cfg.Settings.Accent != "" {
		t.Fatalf("accent = %q, want empty (follow macOS)", cfg.Settings.Accent)
	}
}

type stubRunner struct{ done chan string }

func (s *stubRunner) RunTaskNow(_ context.Context, id string) error {
	s.done <- id
	return nil
}

func TestRunNowInvokesRunner(t *testing.T) {
	sr := &stubRunner{done: make(chan string, 1)}
	srv, _ := newServer(t, sr)
	do(t, srv, "POST", "/api/tasks", sampleTask("a"))

	if r := do(t, srv, "POST", "/api/tasks/a/run-now", nil); r.Status != http.StatusAccepted {
		t.Fatalf("run-now status = %d", r.Status)
	}
	select {
	case id := <-sr.done:
		if id != "a" {
			t.Fatalf("runner called with %q, want a", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner was not invoked")
	}
}

type stubCanceler struct {
	got string
	err error
}

func (s *stubCanceler) CancelRun(runID string) error { s.got = runID; return s.err }

func TestCancelRunEndpoint(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	sc := &stubCanceler{}
	srv := httptest.NewServer(handler(Deps{Store: st, Canceler: sc}))
	t.Cleanup(srv.Close)

	if r := do(t, srv, "POST", "/api/runs/run-1/cancel", nil); r.Status != http.StatusNoContent {
		t.Fatalf("cancel status = %d (%s)", r.Status, r.Body)
	}
	if sc.got != "run-1" {
		t.Fatalf("canceler called with %q, want run-1", sc.got)
	}

	// A run that is not in flight yields 409.
	sc.err = errors.New("run \"run-1\" is not running")
	if r := do(t, srv, "POST", "/api/runs/run-1/cancel", nil); r.Status != http.StatusConflict {
		t.Fatalf("cancel of finished run status = %d", r.Status)
	}
}

// continueFixture seeds a store with one run and returns a server whose
// TerminalOpener records its invocation. The run's fields are shaped by
// mutate, the stored settings by mutateSettings (both optional).
func continueFixture(t *testing.T, mutate func(*store.Run), mutateSettings func(*store.Settings)) (*httptest.Server, *store.Store, *struct {
	dir  string
	argv []string
}) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// A fixed binary path keeps the expected argv deterministic (no host detection).
	cfg, _ := st.LoadConfig()
	cfg.Providers[0].BinaryPath = "/opt/claude"
	if mutateSettings != nil {
		mutateSettings(&cfg.Settings)
	}
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	tk := sampleTask("a")
	tk.WorkingDir = t.TempDir() // must exist on disk for the endpoint's check
	run := store.Run{RunID: "r1", TaskID: "a", TaskName: "a", StartedAt: time.Now(),
		Status: store.StatusSuccess, SessionID: "sess-1", Task: &tk}
	if mutate != nil {
		mutate(&run)
	}
	if err := st.AppendRun(run); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}
	got := &struct {
		dir  string
		argv []string
	}{}
	opener := func(_ context.Context, dir string, argv []string) error {
		got.dir, got.argv = dir, argv
		return nil
	}
	srv := httptest.NewServer(handler(Deps{Store: st, OpenTerminal: opener}))
	t.Cleanup(srv.Close)
	return srv, st, got
}

func TestContinueRunOpensTerminal(t *testing.T) {
	srv, _, got := continueFixture(t, nil, nil)
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	if got.dir == "" || !strings.HasPrefix(got.dir, "/") {
		t.Fatalf("opener dir = %q, want the task's working dir", got.dir)
	}
	want := []string{"/opt/claude", "--resume", "sess-1"}
	if len(got.argv) != len(want) || got.argv[0] != want[0] || got.argv[1] != want[1] || got.argv[2] != want[2] {
		t.Fatalf("opener argv = %v, want %v", got.argv, want)
	}
}

// TestContinueRunFollowsTheRunsOwnProvider: a task moved to another provider
// left its finished conversation where it was. Continuing it has to go back to
// the harness that holds it, not to whatever the task points at today.
func TestContinueRunFollowsTheRunsOwnProvider(t *testing.T) {
	srv, srvStore, got := continueFixture(t, func(r *store.Run) {
		r.Provider = store.RunProvider{ID: "claude-work", Kind: store.DefaultProviderKind, Name: "Claude (work)"}
		tk := *r.Task
		tk.Provider = store.DefaultProviderID // the task has since been moved back
		r.Task = &tk
	}, nil)
	// The instance the run used exists, with a binary of its own.
	if err := srvStore.UpdateConfig(func(cfg *store.Config) error {
		cfg.Providers = append(cfg.Providers, store.Provider{
			ID: "claude-work", Kind: store.DefaultProviderKind, Name: "Claude (work)",
			BinaryPath: "/opt/claude-work", Enabled: true,
		})
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	if got.argv[0] != "/opt/claude-work" {
		t.Fatalf("argv = %v, want the binary of the provider that owns the session", got.argv)
	}
}

// TestContinueRunUsesTheProvidersAccount: a Claude Code instance is its config
// directory. A run that went to a fallback account has to reopen under that
// account, not under the login a plain shell would pick.
func TestContinueRunUsesTheProvidersAccount(t *testing.T) {
	stubSrv, srvStore, got := continueFixture(t, func(r *store.Run) {
		r.Provider = store.RunProvider{ID: "claude-max", Kind: store.DefaultProviderKind, Name: "Claude Max"}
	}, nil)
	stubSrv.Close()
	// The real adapter, because it is the one that turns the config directory
	// into the environment the resume needs.
	reg := provider.NewRegistry(claudecode.New())
	srv := httptest.NewServer(Handler(Deps{Store: srvStore, Registry: reg, Providers: provider.NewChecker(reg),
		OpenTerminal: func(_ context.Context, dir string, argv []string) error {
			got.dir, got.argv = dir, argv
			return nil
		}}))
	t.Cleanup(srv.Close)
	if err := srvStore.UpdateConfig(func(cfg *store.Config) error {
		cfg.Providers = append(cfg.Providers, store.Provider{
			ID: "claude-max", Kind: store.DefaultProviderKind, Name: "Claude Max",
			BinaryPath: "/opt/claude", ConfigDir: "/Users/x/.claude-max", Enabled: true,
		})
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	want := []string{"/usr/bin/env", "CLAUDE_CONFIG_DIR=/Users/x/.claude-max", "/opt/claude", "--resume", "sess-1"}
	if strings.Join(got.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", got.argv, want)
	}
}

// TestContinueRunKeepsTheKeyOffTheCommandLine: an instance that runs on an API
// key must reopen on that key, but Terminal prints the command and the shell
// keeps it in its history, so the key travels in a file the shell deletes.
func TestContinueRunKeepsTheKeyOffTheCommandLine(t *testing.T) {
	stubSrv, srvStore, got := continueFixture(t, func(r *store.Run) {
		r.Provider = store.RunProvider{ID: "claude-api", Kind: store.DefaultProviderKind, Name: "Claude API"}
	}, nil)
	stubSrv.Close()
	reg := provider.NewRegistry(claudecode.New())
	srv := httptest.NewServer(Handler(Deps{Store: srvStore, Registry: reg, Providers: provider.NewChecker(reg),
		OpenTerminal: func(_ context.Context, dir string, argv []string) error {
			got.dir, got.argv = dir, argv
			return nil
		}}))
	t.Cleanup(srv.Close)
	if err := srvStore.UpdateConfig(func(cfg *store.Config) error {
		cfg.Providers = append(cfg.Providers, store.Provider{
			ID: "claude-api", Kind: store.DefaultProviderKind, Name: "Claude API",
			BinaryPath: "/opt/claude", ConfigDir: "/Users/x/.claude-api", Enabled: true,
			Env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-it's-secret"},
		})
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	if strings.Contains(strings.Join(got.argv, " "), "sk-ant") {
		t.Fatalf("argv = %v, want the key kept out of the command line", got.argv)
	}
	if len(got.argv) < 5 || got.argv[0] != "/bin/sh" {
		t.Fatalf("argv = %v, want the shell wrapper", got.argv)
	}
	file := got.argv[3]
	t.Cleanup(func() { _ = os.Remove(file) })
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read the env file: %v", err)
	}
	if want := "export ANTHROPIC_API_KEY='sk-ant-it'\\''s-secret'\n"; string(data) != want {
		t.Fatalf("env file = %q, want %q", data, want)
	}
	if want := "/usr/bin/env CLAUDE_CONFIG_DIR=/Users/x/.claude-api /opt/claude --resume sess-1"; strings.Join(got.argv[4:], " ") != want {
		t.Fatalf("command = %q, want %q", strings.Join(got.argv[4:], " "), want)
	}
	// What the wrapper does with the file: export, delete, exec.
	out, err := exec.Command(got.argv[0], append(got.argv[1:4], "/usr/bin/env")...).Output() // #nosec G204 — the argv built above, with env as the command
	if err != nil {
		t.Fatalf("run the wrapper: %v", err)
	}
	if !strings.Contains(string(out), "ANTHROPIC_API_KEY=sk-ant-it's-secret\n") {
		t.Fatalf("the wrapped process did not receive the key")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the env file still exists after the wrapper ran: %v", err)
	}
}

// A terminal that opens but never runs its shell must not leave the key in a
// temporary file.
func TestContinueRunRemovesAnUnreadKeyFile(t *testing.T) {
	prev := secretEnvTTL
	secretEnvTTL = 20 * time.Millisecond
	t.Cleanup(func() { secretEnvTTL = prev })

	stubSrv, srvStore, got := continueFixture(t, func(r *store.Run) {
		r.Provider = store.RunProvider{ID: "claude-api", Kind: store.DefaultProviderKind, Name: "Claude API"}
	}, nil)
	stubSrv.Close()
	reg := provider.NewRegistry(claudecode.New())
	srv := httptest.NewServer(Handler(Deps{Store: srvStore, Registry: reg, Providers: provider.NewChecker(reg),
		OpenTerminal: func(_ context.Context, dir string, argv []string) error {
			got.dir, got.argv = dir, argv
			return nil // reported as opened, but its shell never runs
		}}))
	t.Cleanup(srv.Close)
	if err := srvStore.UpdateConfig(func(cfg *store.Config) error {
		cfg.Providers = append(cfg.Providers, store.Provider{
			ID: "claude-api", Kind: store.DefaultProviderKind, Name: "Claude API",
			BinaryPath: "/opt/claude", Enabled: true, Env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"},
		})
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	file := got.argv[3]
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			_ = os.Remove(file)
			t.Fatal("the unread key file is still there")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestContinueRunKeepsTheAuthorityItHad: raising a task's permissions later must
// not retroactively hand a finished conversation more authority.
func TestContinueRunKeepsTheAuthorityItHad(t *testing.T) {
	srv, _, got := continueFixture(t, func(r *store.Run) {
		r.Provider = store.RunProvider{ID: store.DefaultProviderID, AccessMode: string(provider.AccessProviderDefault)}
		tk := *r.Task
		tk.Permissions = task.PermissionsSkip // changed after the run
		r.Task = &tk
	}, nil)
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	for _, a := range got.argv {
		if a == "--dangerously-skip-permissions" {
			t.Fatalf("argv %v carries authority the run never had", got.argv)
		}
	}
}

// TestContinueRunPermissionFlag verifies the resume carries the same permission
// mode the unattended run had, which is the task's own setting.
func TestContinueRunPermissionFlag(t *testing.T) {
	const flag = "--dangerously-skip-permissions"
	cases := []struct {
		name  string
		perms task.Permissions
		want  bool
	}{
		{"task skips", task.PermissionsSkip, true},
		{"task keeps prompts", task.PermissionsDefault, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, got := continueFixture(t,
				func(r *store.Run) {
					tk := *r.Task
					tk.Permissions = c.perms
					r.Task = &tk
				}, nil)
			if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
				t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
			}
			has := false
			for _, a := range got.argv {
				if a == flag {
					has = true
				}
			}
			if has != c.want {
				t.Fatalf("argv %v: %s present = %t, want %t", got.argv, flag, has, c.want)
			}
		})
	}
}

func TestContinueRunGuards(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*store.Run)
		status int
	}{
		{"running run", func(r *store.Run) { r.Status = store.StatusRunning }, http.StatusConflict},
		{"rate-limited run auto-resumes", func(r *store.Run) { r.Status = store.StatusRateLimited }, http.StatusConflict},
		{"no session recorded", func(r *store.Run) { r.SessionID = "" }, http.StatusConflict},
		{"no task snapshot", func(r *store.Run) { r.Task = nil }, http.StatusConflict},
		{"working dir gone", func(r *store.Run) {
			tk := *r.Task
			tk.WorkingDir = "/nonexistent/dispatchq-test"
			r.Task = &tk
		}, http.StatusConflict},
		{"failed run is continuable", func(r *store.Run) { r.Status = store.StatusFailed }, http.StatusNoContent},
		{"canceled run is continuable", func(r *store.Run) { r.Status = store.StatusCanceled }, http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _ := continueFixture(t, c.mutate, nil)
			if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != c.status {
				t.Fatalf("status = %d (%s), want %d", r.Status, r.Body, c.status)
			}
		})
	}
}

// stubTakeover stands in for the engine: it records which run it stopped and
// answers with the record the run finished as.
type stubTakeover struct {
	got    string
	rec    store.Run
	err    error
	before func() // runs before answering, e.g. to let the run end on its own
}

func (s *stubTakeover) TakeOverRun(_ context.Context, runID string) (store.Run, error) {
	s.got = runID
	if s.before != nil {
		s.before()
	}
	return s.rec, s.err
}

// takeoverFixture is continueFixture with a running run and a take-over wired.
func takeoverFixture(t *testing.T, to *stubTakeover) (*httptest.Server, *store.Store, *struct {
	dir  string
	argv []string
}) {
	t.Helper()
	stubSrv, st, got := continueFixture(t, func(r *store.Run) { r.Status = store.StatusRunning }, nil)
	stubSrv.Close()
	srv := httptest.NewServer(handler(Deps{Store: st, TakeOver: to,
		OpenTerminal: func(_ context.Context, dir string, argv []string) error {
			got.dir, got.argv = dir, argv
			return nil
		}}))
	t.Cleanup(srv.Close)
	return srv, st, got
}

// TestContinueRunTakesOverARunningRun: a running run is stopped first, and the
// terminal reopens the session the run finally reported.
func TestContinueRunTakesOverARunningRun(t *testing.T) {
	to := &stubTakeover{rec: store.Run{RunID: "r1", Status: store.StatusCanceled, SessionID: "sess-final"}}
	srv, _, got := takeoverFixture(t, to)
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	if to.got != "r1" {
		t.Fatalf("take-over got run %q, want r1", to.got)
	}
	if want := "/opt/claude --resume sess-final"; strings.Join(got.argv, " ") != want {
		t.Fatalf("argv = %v, want %q", got.argv, want)
	}
}

// TestContinueRunTakeOverFailureOpensNothing: a run that could not be stopped,
// or stopped without a session, never gets a terminal of its own.
func TestContinueRunTakeOverFailureOpensNothing(t *testing.T) {
	cases := []struct {
		name string
		to   *stubTakeover
	}{
		{"run already gone", &stubTakeover{err: errors.New("run \"r1\" is not running")}},
		{"no session reported", &stubTakeover{rec: store.Run{RunID: "r1", Status: store.StatusCanceled}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _, got := takeoverFixture(t, c.to)
			if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusConflict {
				t.Fatalf("status = %d (%s), want 409", r.Status, r.Body)
			}
			if got.argv != nil {
				t.Fatalf("terminal opened with %v", got.argv)
			}
		})
	}
}

// TestContinueRunTakeOverOfARunThatJustEnded: a run that finished on its own
// between being read and being stopped is continued as the finished run it is.
func TestContinueRunTakeOverOfARunThatJustEnded(t *testing.T) {
	to := &stubTakeover{err: errors.New("run \"r1\" is not running")}
	srv, st, got := takeoverFixture(t, to)
	to.before = func() {
		runs, _ := st.Runs()
		done := runs[0]
		done.Status, done.SessionID = store.StatusSuccess, "sess-done"
		if err := st.AppendRun(done); err != nil {
			t.Errorf("AppendRun: %v", err)
		}
	}
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	if want := "/opt/claude --resume sess-done"; strings.Join(got.argv, " ") != want {
		t.Fatalf("argv = %v, want %q", got.argv, want)
	}
}

func TestContinueRunNotFound(t *testing.T) {
	srv, _, _ := continueFixture(t, nil, nil)
	if r := do(t, srv, "POST", "/api/runs/nope/continue", nil); r.Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", r.Status)
	}
}

func TestContinueRunUnavailable(t *testing.T) {
	srv, _ := newServer(t, nil) // no TerminalOpener wired
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("continue without opener status = %d", r.Status)
	}
}

func TestCancelRunUnavailable(t *testing.T) {
	srv, _ := newServer(t, nil) // no Canceler wired
	if r := do(t, srv, "POST", "/api/runs/run-1/cancel", nil); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("cancel without canceler status = %d", r.Status)
	}
}

func TestAddTaskGeneratesID(t *testing.T) {
	srv, _ := newServer(t, nil)
	// No id supplied — the server must generate one.
	body := map[string]any{"name": "My Nightly Build", "prompt": "p", "working_dir": "/r", "trigger": "asap"}
	if r := do(t, srv, "POST", "/api/tasks", body); r.Status != http.StatusCreated {
		t.Fatalf("add status = %d (%s)", r.Status, r.Body)
	}
	var tasks []task.Task
	do(t, srv, "GET", "/api/tasks", nil).into(t, &tasks)
	if len(tasks) != 1 || tasks[0].ID == "" {
		t.Fatalf("expected a task with a generated id, got %+v", tasks)
	}
	if !strings.HasPrefix(tasks[0].ID, "my-nightly-build-") {
		t.Fatalf("generated id %q should derive from the name", tasks[0].ID)
	}
	// A new task must be enabled by default even if the client omits the flag,
	// otherwise the scheduler would skip it (only "run now" would work).
	if !tasks[0].Enabled {
		t.Fatal("newly added task should be enabled by default")
	}
}

func TestChooseFolder(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chooser := func(_ context.Context, _ string) (string, bool, error) { return "/Users/me/proj", true, nil }
	srv := httptest.NewServer(handler(Deps{Store: st, ChooseFolder: chooser}))
	t.Cleanup(srv.Close)

	var res struct {
		Path string `json:"path"`
	}
	do(t, srv, "POST", "/api/fs/choose", nil).into(t, &res)
	if res.Path != "/Users/me/proj" {
		t.Fatalf("path = %q, want /Users/me/proj", res.Path)
	}

	// Cancellation -> 204.
	cancelSrv := httptest.NewServer(handler(Deps{Store: st, ChooseFolder: func(_ context.Context, _ string) (string, bool, error) { return "", false, nil }}))
	t.Cleanup(cancelSrv.Close)
	if r := do(t, cancelSrv, "POST", "/api/fs/choose", nil); r.Status != http.StatusNoContent {
		t.Fatalf("cancel should be 204, got %d", r.Status)
	}
}

func TestGetStatsEndpoint(t *testing.T) {
	srv, st := newServer(t, nil)
	_ = st.AppendRun(store.Run{RunID: "r1", TaskName: "a", StartedAt: time.Now(), Status: store.StatusSuccess, CostUSD: 0.2, InputTokens: 10, OutputTokens: 5})
	var s Stats
	do(t, srv, "GET", "/api/stats", nil).into(t, &s)
	if s.Totals.Runs != 1 || s.Totals.Success != 1 {
		t.Fatalf("unexpected stats: %+v", s.Totals)
	}
}

// TestListModels: the catalog belongs to a provider, so the endpoint asks the
// adapter of the instance named in the query — or of the default one.
func TestListModels(t *testing.T) {
	srv, _ := newServer(t, nil)
	for _, path := range []string{"/api/models", "/api/models?provider=claude"} {
		var models []provider.Model
		do(t, srv, "GET", path, nil).into(t, &models)
		if len(models) == 0 {
			t.Fatalf("%s: expected at least one model", path)
		}
		for _, m := range models {
			if m.ID == "" || m.Label == "" {
				t.Fatalf("%s: model missing id/label: %+v", path, m)
			}
		}
	}
	if r := do(t, srv, "GET", "/api/models?provider=nope", nil); r.Status != http.StatusNotFound {
		t.Fatalf("an unknown provider = %d, want 404", r.Status)
	}
}

// uiFS stands in for a built UI: the page, and one hashed bundle it loads.
var uiFS = fstest.MapFS{
	"index.html":             {Data: []byte(`<!doctype html><title>DispatchQ</title><script type="module" src="/assets/index-abc123.js"></script>`)},
	"assets/index-abc123.js": {Data: []byte("console.log('dispatchq')")},
	"logo.svg":               {Data: []byte("<svg/>")},
}

func newUIServer(t *testing.T, web fs.FS) *httptest.Server {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return newServerDeps(t, Deps{Store: st, Web: web})
}

func TestServesDashboard(t *testing.T) {
	srv := newUIServer(t, uiFS)
	r := do(t, srv, "GET", "/", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("dashboard status = %d", r.Status)
	}
	if !bytes.Contains(r.Body, []byte("/assets/index-abc123.js")) {
		t.Fatalf("dashboard HTML = %q, want the built page", r.Body)
	}
	for _, path := range []string{"/assets/index-abc123.js", "/logo.svg"} {
		if r := do(t, srv, "GET", path, nil); r.Status != http.StatusOK || len(r.Body) == 0 {
			t.Fatalf("GET %s = %d (%d bytes), want the file", path, r.Status, len(r.Body))
		}
	}
}

// A daemon compiled before the UI was built must say so instead of serving an
// empty directory listing into the app window.
func TestServesNotBuiltPage(t *testing.T) {
	srv := newUIServer(t, fstest.MapFS{".gitkeep": {}})
	r := do(t, srv, "GET", "/", nil)
	if r.Status != http.StatusServiceUnavailable || !bytes.Contains(r.Body, []byte("make web")) {
		t.Fatalf("GET / without a build = %d %q, want 503 with build instructions", r.Status, r.Body)
	}
}

// Hashed bundles never change under the same name, so they may be cached for
// good; the page that names them must not be, or a new build would never load.
func TestAssetsAreCachedPageIsNot(t *testing.T) {
	srv := newUIServer(t, uiFS)
	for path, want := range map[string]string{
		"/":                       "no-cache, no-store, must-revalidate",
		"/assets/index-abc123.js": "public, max-age=31536000, immutable",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Cache-Control"); got != want {
			t.Fatalf("GET %s Cache-Control = %q, want %q", path, got, want)
		}
	}
}

func TestPauseEndpointTogglesOnlyThatSetting(t *testing.T) {
	srv, st := newServer(t, nil)
	if err := st.SaveConfig(store.Config{Settings: store.Settings{
		SystemPrompt: "be brief", HeartbeatMinutes: 30,
		Pushover: store.Pushover{Enabled: true, Token: "tok", UserKey: "usr"},
	}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	r := do(t, srv, "POST", "/api/pause", map[string]bool{"paused": true})
	if r.Status != http.StatusOK {
		t.Fatalf("pause status = %d (%s)", r.Status, r.Body)
	}
	var got map[string]bool
	r.into(t, &got)
	if !got["paused"] {
		t.Fatalf("response = %v, want paused:true", got)
	}
	cfg, _ := st.LoadConfig()
	if !cfg.Settings.Paused {
		t.Fatal("pause was not persisted")
	}
	// The switch travels alone: everything else must survive it.
	if cfg.Settings.SystemPrompt != "be brief" || cfg.Settings.HeartbeatMinutes != 30 || cfg.Settings.Pushover.Token != "tok" {
		t.Fatalf("pause clobbered other settings: %+v", cfg.Settings)
	}

	if r := do(t, srv, "POST", "/api/pause", map[string]bool{"paused": false}); r.Status != http.StatusOK {
		t.Fatalf("resume status = %d (%s)", r.Status, r.Body)
	}
	cfg, _ = st.LoadConfig()
	if cfg.Settings.Paused {
		t.Fatal("resume was not persisted")
	}
}

func TestPutSettingsCannotClobberThePauseSwitch(t *testing.T) {
	srv, st := newServer(t, nil)
	if r := do(t, srv, "POST", "/api/pause", map[string]bool{"paused": true}); r.Status != http.StatusOK {
		t.Fatalf("pause status = %d", r.Status)
	}
	// A settings form filled in before the pause was flipped carries paused:false.
	body := map[string]any{"system_prompt": "be brief", "paused": false}
	if r := do(t, srv, "PUT", "/api/settings", body); r.Status != http.StatusOK {
		t.Fatalf("put settings = %d", r.Status)
	}
	cfg, _ := st.LoadConfig()
	if !cfg.Settings.Paused {
		t.Fatal("a stale settings payload resumed the queue")
	}
	if cfg.Settings.SystemPrompt != "be brief" {
		t.Fatalf("the rest of the payload was not applied: %+v", cfg.Settings)
	}
}

func TestSettingsExposePausedState(t *testing.T) {
	srv, st := newServer(t, nil)
	if err := st.SaveConfig(store.Config{Settings: store.Settings{Paused: true}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	var s store.Settings
	do(t, srv, "GET", "/api/settings", nil).into(t, &s)
	if !s.Paused {
		t.Fatal("GET /api/settings did not report the pause state")
	}
}

func TestRunNowRefusedWhilePaused(t *testing.T) {
	sr := &stubRunner{done: make(chan string, 1)}
	srv, _ := newServer(t, sr)
	do(t, srv, "POST", "/api/tasks", sampleTask("a"))
	if r := do(t, srv, "POST", "/api/pause", map[string]bool{"paused": true}); r.Status != http.StatusOK {
		t.Fatalf("pause status = %d", r.Status)
	}

	r := do(t, srv, "POST", "/api/tasks/a/run-now", nil)
	if r.Status != http.StatusConflict {
		t.Fatalf("run-now while paused = %d, want 409 (%s)", r.Status, r.Body)
	}
	if !strings.Contains(string(r.Body), "paused") {
		t.Fatalf("409 body does not say why: %s", r.Body)
	}
	select {
	case id := <-sr.done:
		t.Fatalf("runner was invoked for %q while paused", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// A rate-limited run is only "rescheduled" while the daemon still holds its
// session for a resume — that is what the dashboard labels and offers to cancel.
func TestRunsReportPendingResume(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 17, 22, 0, 0, 0, time.UTC)
	resume := start.Add(time.Hour)
	waiting := store.Run{
		RunID: "run-1", TaskID: "a", TaskName: "a", StartedAt: start,
		Status: store.StatusRateLimited, SessionID: "sess-1", ResumeAt: &resume,
	}
	stale := store.Run{
		RunID: "run-0", TaskID: "b", TaskName: "b", StartedAt: start,
		Status: store.StatusRateLimited, SessionID: "sess-0",
	}
	for _, r := range []store.Run{stale, waiting} {
		if err := st.AppendRun(r); err != nil {
			t.Fatalf("AppendRun: %v", err)
		}
	}
	if err := st.UpdateState(func(s *store.State) error {
		s.SetPendingResume("a", "sess-1", "claude")
		return nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	active := []string{}
	srv := httptest.NewServer(handler(Deps{Store: st, ActiveTasks: func() []string { return active }}))
	t.Cleanup(srv.Close)

	get := func() map[string]runView {
		var views []runView
		do(t, srv, "GET", "/api/runs", nil).into(t, &views)
		byID := map[string]runView{}
		for _, v := range views {
			byID[v.RunID] = v
		}
		return byID
	}

	got := get()
	if !got["run-1"].ResumePending {
		t.Fatal("the run whose session is queued should report resume_pending")
	}
	if got["run-1"].ResumeAt == nil || !got["run-1"].ResumeAt.Equal(resume) {
		t.Fatalf("resume_at = %v, want %v", got["run-1"].ResumeAt, resume)
	}
	if got["run-0"].ResumePending {
		t.Fatal("a rate-limited run without a pending session must not report resume_pending")
	}

	// Once the task is running again the resume has happened; there is nothing
	// left to cancel on the old run.
	active = []string{"a"}
	if get()["run-1"].ResumePending {
		t.Fatal("a task that is running again must not report a pending resume")
	}
}

func TestTasksReportWaitingForLimit(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	waiting := task.Task{
		ID: "a", Name: "a", Prompt: "p", WorkingDir: "/repo",
		Trigger: task.TriggerCron, Cron: "*/30 * * * *", Enabled: true,
		Permissions: task.PermissionsDefault,
	}
	idle := waiting
	idle.ID, idle.Name = "b", "b"
	if err := st.SaveConfig(store.Config{Tasks: []task.Task{waiting, idle}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := st.UpdateState(func(s *store.State) error {
		s.SetPendingResume("a", "sess-1", "claude")
		return nil
	}); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)

	var views []taskView
	do(t, srv, "GET", "/api/tasks", nil).into(t, &views)
	if len(views) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(views))
	}
	for _, v := range views {
		if want := v.ID == "a"; v.WaitingForLimit != want {
			t.Fatalf("task %q waiting_for_limit = %t, want %t", v.ID, v.WaitingForLimit, want)
		}
	}
}

func TestHealthReportsLimitedUntil(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	blocked := until
	srv := httptest.NewServer(handler(Deps{Store: st, LimitedUntil: func() time.Time { return blocked }}))
	t.Cleanup(srv.Close)

	var got map[string]any
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if got["limited_until"] != until.Format(time.RFC3339) {
		t.Fatalf("limited_until = %q, want %q", got["limited_until"], until.Format(time.RFC3339))
	}

	// An open gate reports nothing rather than a zero timestamp.
	blocked = time.Time{}
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if got["limited_until"] != "" {
		t.Fatalf("limited_until = %q, want empty while the gate is open", got["limited_until"])
	}
}

func TestHealthReportsLimitedProviderNames(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	srv := httptest.NewServer(handler(Deps{Store: st, BlockedProviders: func() map[string]time.Time {
		return map[string]time.Time{store.DefaultProviderID: until}
	}}))
	t.Cleanup(srv.Close)

	var got struct {
		LimitedProviders []struct {
			Name  string `json:"name"`
			Until string `json:"until"`
		} `json:"limited_providers"`
	}
	do(t, srv, "GET", "/api/health", nil).into(t, &got)
	if len(got.LimitedProviders) != 1 || got.LimitedProviders[0].Name != store.DefaultProviderName {
		t.Fatalf("limited_providers = %+v, want one entry named %q", got.LimitedProviders, store.DefaultProviderName)
	}
	if got.LimitedProviders[0].Until != until.Format(time.RFC3339) {
		t.Fatalf("until = %q, want %q", got.LimitedProviders[0].Until, until.Format(time.RFC3339))
	}
}

func TestUpdateTaskTakesTheProviderFromThePayload(t *testing.T) {
	// PUT replaces the task, so the provider follows the payload like every
	// other field: the dashboard sends back the one it loaded (see
	// taskProvider in index.html), and an explicitly empty one means the
	// default provider.
	srv, st := newServer(t, nil)
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	second := cfg.Providers[0]
	second.ID, second.Name = "claude-secondary", "Second account"
	cfg.Providers = append(cfg.Providers, second)
	tk := sampleTask("a")
	tk.Provider = "claude-secondary"
	cfg.Tasks = []task.Task{tk}
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	kept := tk
	kept.Name = "renamed"
	if r := do(t, srv, "PUT", "/api/tasks/a", kept); r.Status != http.StatusOK {
		t.Fatalf("update = %d (%s)", r.Status, r.Body)
	}
	cfg, err = st.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Tasks[0].Provider != "claude-secondary" || cfg.Tasks[0].Name != "renamed" {
		t.Fatalf("task = %+v, want the provider kept and the name changed", cfg.Tasks[0])
	}

	cleared := kept
	cleared.Provider = ""
	if r := do(t, srv, "PUT", "/api/tasks/a", cleared); r.Status != http.StatusOK {
		t.Fatalf("update = %d (%s)", r.Status, r.Body)
	}
	cfg, err = st.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Tasks[0].Provider != "" {
		t.Fatalf("provider = %q, want it cleared back to the default", cfg.Tasks[0].Provider)
	}
}

// TestContinueRunUsesTheOwningProvider: the session belongs to the instance the
// task runs on, and its adapter says how that harness reopens one — so a task
// that skipped permission prompts resumes with the same authority.
func TestContinueRunUsesTheOwningProvider(t *testing.T) {
	srv, _, got := continueFixture(t, func(r *store.Run) { r.Task.Permissions = task.PermissionsSkip }, nil)
	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusNoContent {
		t.Fatalf("continue status = %d (%s)", r.Status, r.Body)
	}
	want := []string{"/opt/claude", "--resume", "sess-1", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got.argv, want) {
		t.Fatalf("argv = %q, want %q", got.argv, want)
	}
}

// TestContinueRunRefusesAHarnessThatCannot: dispatchq never guesses at another
// binary when the one that owns the session cannot be reopened.
func TestContinueRunRefusesAHarnessThatCannot(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	tk := sampleTask("a")
	tk.WorkingDir = t.TempDir()
	if err := st.AppendRun(store.Run{
		RunID: "r1", TaskID: "a", TaskName: "a", StartedAt: time.Now(),
		Status: store.StatusSuccess, SessionID: "sess-1", Task: &tk,
	}); err != nil {
		t.Fatalf("AppendRun: %v", err)
	}
	// An adapter that claims no interactive resume, and no binary either.
	reg := provider.NewRegistry(stubAdapter{health: provider.Health{State: provider.HealthReady}})
	srv := httptest.NewServer(Handler(Deps{
		Store: st, Registry: reg, Providers: provider.NewChecker(reg),
		OpenTerminal: func(context.Context, string, []string) error { return nil },
	}))
	t.Cleanup(srv.Close)

	if r := do(t, srv, "POST", "/api/runs/r1/continue", nil); r.Status != http.StatusConflict {
		t.Fatalf("continue = %d (%s), want it refused", r.Status, r.Body)
	}
}

// scriptTask is a job the daemon runs as a program, with no provider behind it.
func scriptTask(id string) task.Task {
	t := sampleTask(id)
	t.Kind = task.KindScript
	t.Prompt = "echo hi"
	return t
}

func TestScriptTaskIsFiledWithoutAReadyProvider(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// No harness on this machine: an agent job is refused here, a script job is
	// not — it needs none.
	reg := provider.NewRegistry(stubAdapter{health: provider.Health{
		State: provider.HealthNotInstalled, Reason: "no CLI here",
	}})
	srv := httptest.NewServer(Handler(Deps{Store: st, Registry: reg, Providers: provider.NewChecker(reg)}))
	t.Cleanup(srv.Close)

	if r := do(t, srv, "POST", "/api/tasks", sampleTask("agent")); r.Status != http.StatusBadRequest {
		t.Fatalf("agent job status = %d, want 400 while the harness is missing", r.Status)
	}
	if r := do(t, srv, "POST", "/api/tasks", scriptTask("watch")); r.Status != http.StatusCreated {
		t.Fatalf("script job status = %d (%s), want 201", r.Status, r.Body)
	}

	// And it is not reported as blocked by a provider it does not use.
	var listed []taskView
	do(t, srv, "GET", "/api/tasks", nil).into(t, &listed)
	if len(listed) != 1 {
		t.Fatalf("expected the script job in the queue, got %+v", listed)
	}
	if listed[0].BlockedReason != "" {
		t.Fatalf("blocked reason = %q, want none for a script job", listed[0].BlockedReason)
	}
	if !listed[0].IsScript() {
		t.Fatal("the stored job lost its kind")
	}
}

func TestScriptTaskCannotNameAProvider(t *testing.T) {
	srv, _ := newServer(t, nil)
	bad := scriptTask("watch")
	bad.Provider = "claude"
	if r := do(t, srv, "POST", "/api/tasks", bad); r.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a script job with a provider", r.Status)
	}
}

func TestTaskBecomesAScriptOnEdit(t *testing.T) {
	srv, st := newServer(t, nil)
	if r := do(t, srv, "POST", "/api/tasks", sampleTask("a")); r.Status != http.StatusCreated {
		t.Fatalf("add status = %d", r.Status)
	}
	edited := scriptTask("a")
	if r := do(t, srv, "PUT", "/api/tasks/a", edited); r.Status != http.StatusOK {
		t.Fatalf("edit status = %d (%s)", r.Status, r.Body)
	}
	cfg, _ := st.LoadConfig()
	if len(cfg.Tasks) != 1 || !cfg.Tasks[0].IsScript() {
		t.Fatalf("stored task = %+v, want a script job", cfg.Tasks)
	}
}

// A WKWebView that reused a cached answer would show the queue as it was on an
// earlier request, so every read of the daemon, data and assets alike, forbids it.
func TestResponsesAreNeverCached(t *testing.T) {
	srv, _ := newServer(t, nil)
	for _, path := range []string{"/api/tasks", "/api/runs", "/api/artifacts", "/api/stats"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Errorf("GET %s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

func TestQueueShowsTheSchedulersHold(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.SaveConfig(store.Config{Tasks: []task.Task{sampleTask("held"), sampleTask("free")}}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	since := time.Date(2026, 10, 5, 15, 41, 0, 0, time.UTC)
	reg := provider.NewRegistry(stubAdapter{health: provider.Health{State: provider.HealthReady}})
	srv := httptest.NewServer(Handler(Deps{
		Store: st, Registry: reg, Providers: provider.NewChecker(reg),
		Holds: func() map[string]schedule.Hold {
			return map[string]schedule.Hold{"held": {Reason: "Runs are paused.", Since: since}}
		},
	}))
	t.Cleanup(srv.Close)

	var listed []taskView
	do(t, srv, "GET", "/api/tasks", nil).into(t, &listed)
	byID := map[string]taskView{}
	for _, v := range listed {
		byID[v.ID] = v
	}
	if h := byID["held"].Hold; h == nil || h.Reason != "Runs are paused." || !h.Since.Equal(since) {
		t.Fatalf("hold of held = %+v", h)
	}
	if byID["free"].Hold != nil {
		t.Fatalf("hold of free = %+v, want none", byID["free"].Hold)
	}
}
