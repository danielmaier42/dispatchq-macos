package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// promptCheckingAsker answers with a canned reply, but only to a model that
// was told what it needs: it fails the test when the system prompt lacks a
// fact the answer relies on, which is how the assistant used to end up asking
// the operator for the CLI's syntax.
type promptCheckingAsker struct {
	t      *testing.T
	needs  []string
	answer string
}

func (a *promptCheckingAsker) Ask(_ context.Context, _ provider.Instance, req provider.AsideRequest) (provider.Aside, error) {
	for _, n := range a.needs {
		if !strings.Contains(req.System, n) {
			a.t.Errorf("the model was not told %q", n)
		}
	}
	return provider.Aside{Structured: json.RawMessage(a.answer)}, nil
}

// buildCLI builds the real dispatchq command line into a directory whose path
// has a space and non-ASCII letters in it, the way an app bundle can.
func buildCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script jobs run with /bin/sh")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain to build the CLI with")
	}
	bin := filepath.Join(t.TempDir(), "Dispatch Q Prüf.app", "Contents", "MacOS", "dispatchq")
	cmd := exec.Command(goBin, "build", "-o", bin, "github.com/danielmaier42/claudeq/cmd/dispatchq")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the CLI: %v\n%s", err, out)
	}
	return bin
}

// The operator asks for a script that sends "test <date and time>" every
// minute through the DispatchQ CLI. The assistant proposes it from what it was
// told, the daemon checks the proposal, the confirmed task is filed, and its
// script is run the way the queue runs it: with the real CLI, writing to a
// disposable data directory, so no notification leaves the machine and no
// model is asked anything.
func TestMinuteNotificationScriptEndToEnd(t *testing.T) {
	bin := buildCLI(t)
	home := filepath.Join(t.TempDir(), "data")
	st, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "Mein Ordner ü")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.DefaultWorkingDir = work
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	script := `"$DISPATCHQ_BIN" notify --title 'test' --message "test $(date '+%Y-%m-%d %H:%M:%S')"`
	draft, _ := json.Marshal(map[string]any{
		"reply": "Ein Skript, das jede Minute die Benachrichtigung sendet.",
		"actions": []any{map[string]any{"type": "add_task", "task": map[string]any{
			"name": "Minütliche Test-Notification", "kind": "script", "prompt": script, "cron": "* * * * *",
		}}},
	})
	rt := PlatformRuntime("darwin", "15.6", true)
	rt.CLIPath, rt.DataDir = bin, home
	ask := &promptCheckingAsker{t: t, answer: string(draft), needs: []string{
		"dispatchq notify [--title T] [--message M] [--url U]",
		bin,
		`"$DISPATCHQ_BIN" notify`,
		"/bin/sh",
		"A script job runs no model",
	}}
	svc := New(ask, &StoreSource{Store: st}, nil, rt)
	reply, err := svc.Turn(context.Background(), claude, "",
		Request{Text: "Leg ein Skript an, das jede Minute über die DispatchQ CLI die Notification \"test <dateTime>\" sendet."})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(reply.Actions) != 1 {
		t.Fatalf("actions = %+v", reply.Actions)
	}
	a := reply.Actions[0]
	if a.Type != ActionAddTask || a.Problem != "" {
		t.Fatalf("proposal = %+v", a)
	}
	d := a.Task
	if d.Kind != "script" || d.Trigger != "cron" || d.Cron != "* * * * *" || d.WorkingDir != work || d.Provider != "" || d.Model != "" || d.Permissions != "" {
		t.Fatalf("draft = %+v, want a minute cron script in the default folder, nothing else set", d)
	}

	// Confirming files the draft as the app sends it (actions.ts: draftInput
	// with permissions "default"), through the same validation the API runs.
	tk := task.Task{ID: "minute-test", Name: d.Name, Kind: task.Kind(d.Kind), Prompt: d.Prompt, WorkingDir: d.WorkingDir,
		Trigger: task.Trigger(d.Trigger), Cron: d.Cron, Enabled: true, Permissions: task.PermissionsDefault}
	if err := tk.Validate(); err != nil {
		t.Fatalf("the confirmed task is invalid: %v", err)
	}

	var log bytes.Buffer
	ex := &executor.Executor{Home: home, QueueBin: bin}
	res, err := ex.Run(context.Background(), executor.Request{Task: tk, RunID: "run-1", Log: &log, IdleTimeout: time.Minute})
	if err != nil {
		t.Fatalf("run the script: %v", err)
	}
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %s, log:\n%s", res.Status, log.String())
	}
	sent, err := st.PendingNotifications()
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("notifications = %+v, log:\n%s", sent, log.String())
	}
	n := sent[0]
	if n.Title != "test" || !regexp.MustCompile(`^test \d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`).MatchString(n.Message) {
		t.Fatalf("notification = %q / %q", n.Title, n.Message)
	}
	if n.TaskID != "minute-test" || n.RunID != "run-1" {
		t.Fatalf("notification not attributed to the job: %+v", n)
	}
}
