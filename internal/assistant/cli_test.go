package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/cliref"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// Every command of the CLI is served somehow, and decided on purpose: a new
// command without a route fails here instead of silently missing from the
// assistant.
func TestEveryCLICommandHasARoute(t *testing.T) {
	names := map[string]bool{}
	for _, c := range cliref.Commands() {
		names[c.Name()] = true
		r, ok := routes[c.Name()]
		if !ok {
			t.Errorf("dispatchq %s has no assistant route", c.Name())
			continue
		}
		if (r.kind == routeTyped || r.kind == routeHandoff) && r.how == "" {
			t.Errorf("dispatchq %s: a typed or handed-off route must say what to use", c.Name())
		}
		if c.Access == cliref.Interactive && r.kind != routeHandoff {
			t.Errorf("dispatchq %s needs a terminal but is not handed off", c.Name())
		}
		if r.kind == routeLookup && c.Access != cliref.Read {
			t.Errorf("dispatchq %s changes something but is served as a lookup", c.Name())
		}
	}
	for n := range routes {
		if !names[n] {
			t.Errorf("route for %q, which the CLI does not have", n)
		}
	}
}

// taskFlagFields maps every flag of the task commands to the draft field that
// carries it. "" marks a flag that has no field, with the reason beside it.
var taskFlagFields = map[string]string{
	"id": "id", "name": "name", "prompt": "prompt", "dir": "working_dir", "kind": "kind",
	"group": "group", "trigger": "trigger", "at": "fixed_at", "cron": "cron",
	"provider": "provider", "pool": "pool", "model": "model", "reasoning-effort": "reasoning_effort",
	"parallel": "parallel", "skip-permissions": "permissions", "notify": "notify_on_result",
	"quiet-history": "quiet_history", "backfill": "backfill", "enabled": "enabled",
	"depends-on": "depends_on", "include-results": "include_results",
	"prompt-file": "prompt",   // the assistant writes the text itself
	"in":          "fixed_at", // counted from now
	"json":        "",         // output format only
}

func draftFields() map[string]bool {
	out := map[string]bool{}
	ty := reflect.TypeFor[TaskDraft]()
	for i := range ty.NumField() {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		out[name] = true
	}
	return out
}

func TestTaskDraftCarriesEveryTaskFlag(t *testing.T) {
	fields := draftFields()
	for _, name := range []string{"add", "edit", "queue"} {
		c, _, _ := cliref.Lookup([]string{name})
		for _, f := range c.Flags {
			field, ok := taskFlagFields[f.Name]
			if !ok {
				t.Errorf("dispatchq %s --%s: no task draft field decided", name, f.Name)
				continue
			}
			if field != "" && !fields[field] {
				t.Errorf("dispatchq %s --%s maps to %q, which TaskDraft lacks", name, f.Name, field)
			}
		}
	}
}

// The answer schema lets the model set every draft field: a field the schema
// omits is one a constrained model can never fill.
func TestSchemaOffersEveryDraftField(t *testing.T) {
	var s struct {
		Properties struct {
			Actions struct {
				Items struct {
					Properties struct {
						Type struct {
							Enum []string `json:"enum"`
						} `json:"type"`
						Task struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"task"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"actions"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	props := s.Properties.Actions.Items.Properties
	for f := range draftFields() {
		if _, ok := props.Task.Properties[f]; !ok {
			t.Errorf("the schema does not offer task.%s", f)
		}
	}
	if !slices.Contains(props.Type.Enum, ActionRunCLI) {
		t.Error("the schema does not offer run_cli")
	}
}

func TestSystemPromptCarriesTheWholeCLI(t *testing.T) {
	rt := PlatformRuntime("darwin", "15.6")
	rt.CLIPath = "/Applications/DispatchQ.app/Contents/MacOS/dispatchq"
	p := systemPrompt(rt)
	for _, c := range cliref.Commands() {
		if !strings.Contains(p, "dispatchq "+c.Synopsis()) {
			t.Errorf("the prompt lacks dispatchq %s", c.Synopsis())
		}
		for _, f := range c.Flags {
			if f.OldName == "" && !strings.Contains(p, "--"+f.Name+":") {
				t.Errorf("the prompt does not explain dispatchq %s --%s", c.Name(), f.Name)
			}
		}
	}
	for _, want := range []string{
		"dispatchq notify [--title T] [--message M] [--url U]",
		"/Applications/DispatchQ.app/Contents/MacOS/dispatchq",
		`"$DISPATCHQ_BIN" notify --title 'Test' --message "Test $(date '+%Y-%m-%d %H:%M:%S')"`,
		"/bin/sh",
		"macOS Notification Center",
		"never estimate one",
		"Never set permissions \"skip\" on your own",
		"runs it under permissions \"default\" only if the provider's allow rules already permit it",
		"an allow rule in the provider's own settings",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if strings.Contains(p, "Mac's local time") {
		t.Error("the prompt still assumes a Mac in its general part")
	}
}

func TestSystemPromptFollowsThePlatform(t *testing.T) {
	win := PlatformRuntime("windows", "11")
	win.CLIPath = `C:\Program Files\DispatchQ\dispatchq.exe`
	p := systemPrompt(win)
	for _, want := range []string{
		"Windows 11", "PowerShell, never Bash", `& $env:DISPATCHQ_BIN notify`,
		`& 'C:\Program Files\DispatchQ\dispatchq.exe'`, "cannot post to this system's own notification center",
		"never suggest macOS tools such as osascript", "never through a system tool of your own choosing",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("Windows prompt lacks %q", want)
		}
	}
	for _, lack := range []string{"/bin/sh", "macOS Notification Center", `"$DISPATCHQ_BIN"`} {
		if strings.Contains(p, lack) {
			t.Errorf("Windows prompt says %q", lack)
		}
	}

	mac := PlatformRuntime("darwin", "15.6")
	mac.CLIPath = "/Users/a b/Dispatch Q.app/Contents/MacOS/dispatchq"
	if p := systemPrompt(mac); !strings.Contains(p, `'/Users/a b/Dispatch Q.app/Contents/MacOS/dispatchq'`) {
		t.Error("a CLI path with spaces is not quoted for the shell")
	}
	if p := systemPrompt(Runtime{Shell: ShellPOSIX}); !strings.Contains(p, "was not found next to the app") {
		t.Error("a missing CLI is not admitted")
	}
}

func TestCheckCLI(t *testing.T) {
	for _, tc := range []struct {
		argv   []string
		lookup bool
		want   []string // normalized argv; nil = refused
		err    string
	}{
		{[]string{"provider", "limits", "--json"}, true, []string{"provider", "limits", "--json=true"}, ""},
		{[]string{"dispatchq", "pool", "next", "--json", "night"}, true, []string{"pool", "next", "night", "--json=true"}, ""},
		{[]string{"settings", "--json"}, true, []string{"settings", "--json=true"}, ""},
		{[]string{"notify", "--title", "test", "--message", "test 'ü' \"x\""}, false, []string{"notify", "--title=test", "--message=test 'ü' \"x\""}, ""},
		{[]string{"provider", "rm", "old"}, false, []string{"provider", "rm", "old"}, ""},
		{[]string{"settings", "--ntfy=true", "--ntfy-topic", "alerts"}, false, []string{"settings", "--ntfy=true", "--ntfy-topic=alerts"}, ""},
		{[]string{"settings", "--pushover-token", "abc"}, false, nil, "Credentials never pass"},
		{[]string{"provider", "edit", "claude", "--env", "ANTHROPIC_API_KEY=x"}, false, nil, "Credentials never pass"},
		{[]string{"provider", "rm", "old"}, true, nil, "propose it as run_cli"},
		{[]string{"status"}, false, nil, "use it as a cli lookup"},
		{[]string{"settings"}, false, nil, "use it as a cli lookup"},
		{[]string{"add", "--prompt", "x"}, false, nil, "use add_task"},
		{[]string{"edit", "t1"}, false, nil, "use update_task"},
		{[]string{"uninstall", "--yes"}, false, nil, "administrator password"},
		{[]string{"settings", "--paused=true"}, false, nil, "pause_all"},
		{[]string{"notify", "--colour", "red"}, false, nil, "no flag --colour"},
		{[]string{"notifyx"}, false, nil, "no command"},
		{[]string{"publish", "--file", "report.html"}, false, nil, "absolute path"},
		{[]string{"provider", "show"}, true, nil, "takes ID"},
		{[]string{"provider", "enable", "a", "b"}, false, nil, "takes ID"},
		{[]string{"read-all", "--x"}, false, nil, "no flag"},
		{[]string{"export", "t1", "--force=maybe"}, false, nil, "true or false"},
	} {
		p, err := checkCLI(tc.argv, tc.lookup)
		if tc.want == nil {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v (lookup %v): err = %v, want %q", tc.argv, tc.lookup, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", tc.argv, err)
			continue
		}
		if got := p.Argv(); !slices.Equal(got, tc.want) {
			t.Errorf("%v: argv = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

// fakeCLI records what it was asked to run.
type fakeCLI struct {
	runs [][]string
	out  string
	err  error
}

func (f *fakeCLI) Run(_ context.Context, argv []string) (string, error) {
	f.runs = append(f.runs, argv)
	return f.out, f.err
}

func TestRunCLIRunsOnlyCheckedCommands(t *testing.T) {
	cli := &fakeCLI{out: "provider \"codex\" disabled"}
	svc := New(&stubAsker{}, &stubSource{}, cli, Runtime{})
	out, err := svc.RunCLI(context.Background(), []string{"provider", "disable", "codex"})
	if err != nil || out != cli.out {
		t.Fatalf("RunCLI = %q, %v", out, err)
	}
	for _, bad := range [][]string{
		{"settings", "--webhook-url", "https://hooks.example/secret"},
		{"rm", "t1"},
		{"sh", "-c", "rm -rf /"},
		{"notify", "--title", "x", "--message", "y", ";", "rm"},
	} {
		if _, err := svc.RunCLI(context.Background(), bad); err == nil {
			t.Errorf("RunCLI(%q) ran", bad)
		}
	}
	if len(cli.runs) != 1 {
		t.Fatalf("ran %d commands, want only the checked one: %q", len(cli.runs), cli.runs)
	}
	if _, err := New(&stubAsker{}, &stubSource{}, nil, Runtime{}).RunCLI(context.Background(), []string{"read-all"}); err == nil {
		t.Fatal("RunCLI without a CLI did not fail")
	}
}

func TestExecCLIWithoutBinarySaysSo(t *testing.T) {
	_, err := ExecCLI{}.Run(context.Background(), []string{"list"})
	if err == nil || !strings.Contains(err.Error(), "not found next to the app") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLILookupRunsReadsOnly(t *testing.T) {
	src, _ := fixture(t)
	cli := &fakeCLI{out: `[{"id":"claude","ready":true}]`}
	src.CLI = cli
	ctx := context.Background()

	label, text := src.Lookup(ctx, Lookup{Tool: LookupCLI, Argv: []string{"provider", "list", "--json"}})
	if label != "Ran dispatchq provider list --json=true" || text != cli.out {
		t.Fatalf("lookup = %q, %q", label, text)
	}
	if _, text := src.Lookup(ctx, Lookup{Tool: LookupCLI, Argv: []string{"provider", "rm", "claude"}}); !strings.Contains(text, "Not run") {
		t.Fatalf("a removing command ran as a lookup: %q", text)
	}
	cli.err = errors.New("no daemon")
	if _, text := src.Lookup(ctx, Lookup{Tool: LookupCLI, Argv: []string{"pool", "next", "night"}}); !strings.Contains(text, "failed: no daemon") {
		t.Fatalf("failure not reported: %q", text)
	}
	if len(cli.runs) != 2 {
		t.Fatalf("runs = %q", cli.runs)
	}
}

func TestCheckPreparesCLICards(t *testing.T) {
	src, _ := fixture(t)
	out, err := src.Check(context.Background(), []Action{
		{Type: ActionRunCLI, Argv: []string{"provider", "rm", "old"}},
		{Type: ActionRunCLI, Argv: []string{"notify", "--title", "Build", "--message", "It's done"}},
		{Type: ActionRunCLI, Argv: []string{"settings", "--ntfy-token", "tk_secret"}},
		{Type: ActionRunCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("cards = %+v, want the empty command dropped", out)
	}
	if rm := out[0]; !rm.Danger || rm.Problem != "" || rm.Command != "dispatchq provider rm old" || rm.Label != "remove a provider" {
		t.Errorf("rm card = %+v", rm)
	}
	if n := out[1]; n.Danger || n.Problem != "" || n.Command != `dispatchq notify --title=Build '--message=It'\''s done'` {
		t.Errorf("notify card = %+v", n)
	}
	if s := out[2]; !strings.Contains(s.Problem, "Credentials never pass") || strings.Contains(s.Command+strings.Join(s.Argv, " "), "tk_secret") {
		t.Errorf("secret card = %+v", s)
	}
}

func TestCheckAppliesEveryTaskField(t *testing.T) {
	src, dir := fixture(t)
	cfg, _ := src.Store.LoadConfig()
	cfg.Pools = append(cfg.Pools, store.Pool{ID: "night", Members: []store.PoolMember{{Provider: "claude"}}})
	cfg.Tasks = append(cfg.Tasks, task.Task{ID: "once-abc", Name: "Once", Prompt: "p", WorkingDir: dir,
		Trigger: task.TriggerASAP, Enabled: true, Permissions: task.PermissionsDefault})
	if err := src.Store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	full := &TaskDraft{
		ID: "follow-up", Name: "Follow-up", Kind: "agent", Prompt: "p", WorkingDir: dir, FixedAt: "2026-11-08T03:00:00+01:00",
		Group: "Night", Pool: "night", Model: "opus", ReasoningEffort: "high", Permissions: "skip",
		Parallel: &yes, NotifyOnResult: &yes, QuietHistory: &no, Backfill: &yes,
		DependsOn: []string{"once-abc"}, IncludeResults: &yes,
	}
	for _, tc := range []struct {
		name    string
		a       Action
		problem string
	}{
		{"every field", Action{Type: ActionAddTask, Task: full}, ""},
		{"unknown pool", Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, Pool: "day"}}, `no pool "day"`},
		{"provider and pool", Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, Pool: "night", Provider: "claude"}}, "not both"},
		{"script with a model", Action{Type: ActionAddTask, Task: &TaskDraft{Kind: "script", Prompt: "echo", WorkingDir: dir, Model: "opus"}}, "cannot set model"},
		{"script with skip", Action{Type: ActionAddTask, Task: &TaskDraft{Kind: "script", Prompt: "echo", WorkingDir: dir, Permissions: "skip"}}, "cannot skip"},
		{"missing dependency", Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, DependsOn: []string{"gone"}}}, `unknown job "gone"`},
		{"results without dependency", Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, IncludeResults: &yes}}, "needs depends_on"},
		{"taken id", Action{Type: ActionAddTask, Task: &TaskDraft{ID: "nightly-abc", Prompt: "p", WorkingDir: dir}}, "already is a task"},
		{"disable by update", Action{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{Enabled: &no}}, ""},
		{"script by update", Action{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{Kind: "script", Prompt: "echo hi"}}, ""},
		{"rename by update", Action{Type: ActionUpdateTask, ID: "nightly-abc", Task: &TaskDraft{ID: "other"}}, "cannot be changed"},
	} {
		out, err := src.Check(context.Background(), []Action{tc.a})
		if err != nil || len(out) != 1 {
			t.Fatalf("%s: %+v, %v", tc.name, out, err)
		}
		if got := out[0].Problem; (tc.problem == "") != (got == "") || !strings.Contains(got, tc.problem) {
			t.Errorf("%s: problem = %q, want %q", tc.name, got, tc.problem)
		}
	}
	out, _ := src.Check(context.Background(), []Action{{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir}}})
	if out[0].Task.Kind != "agent" {
		t.Errorf("a draft without a kind is not shown as an agent job: %+v", out[0].Task)
	}
}

// Review findings: what a refused or looked-up command shows never carries a
// credential, whatever form the model wrote it in.
func TestCredentialsNeverShown(t *testing.T) {
	src, _ := fixture(t)
	src.CLI = &fakeCLI{}
	out, err := src.Check(context.Background(), []Action{
		{Type: ActionRunCLI, Argv: []string{"dispatchq", "settings", "--pushover-token=SECRET1"}},
		{Type: ActionRunCLI, Argv: []string{"/Applications/DispatchQ.app/Contents/MacOS/dispatchq", "provider", "edit", "claude", "--env", "KEY=SECRET2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range out {
		if shown := a.Command + strings.Join(a.Argv, " ") + a.Problem; strings.Contains(shown, "SECRET") {
			t.Errorf("card shows a credential: %+v", a)
		}
	}
	label, text := src.Lookup(context.Background(), Lookup{Tool: LookupCLI, Argv: []string{"dispatchq", "settings", "--ntfy-token", "SECRET3"}})
	if strings.Contains(label+text, "SECRET") {
		t.Errorf("lookup shows a credential: %q / %q", label, text)
	}

	ask := &stubAsker{answers: []provider.Aside{
		validated(`{"reply":"","lookups":[{"tool":"cli","arg":"","argv":["settings","--webhook-url","https://h/SECRET4"]}]}`),
		validated(`{"reply":"ok"}`),
	}}
	r, err := New(ask, src, nil, Runtime{}).Turn(context.Background(), claude, "", Request{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(r.Steps, " ")+ask.reqs[1].Text, "SECRET") {
		t.Errorf("a credential went back into the steps or the conversation: %q / %q", r.Steps, ask.reqs[1].Text)
	}
}

func TestTaskDraftFollowsDispatchqEdit(t *testing.T) {
	src, dir := fixture(t)
	cfg, _ := src.Store.LoadConfig()
	cfg.Tasks[0].Model, cfg.Tasks[0].Group, cfg.Tasks[0].Provider = "opus", "Night", "claude"
	cfg.Tasks = append(cfg.Tasks, task.Task{ID: "done-once", Name: "Once", Prompt: "p", WorkingDir: dir,
		Trigger: task.TriggerASAP, Enabled: true, Permissions: task.PermissionsDefault})
	if err := src.Store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// A finished one-shot job, gone from the queue, is found in the history.
	if err := src.Store.AppendRun(store.Run{RunID: "r-done", TaskID: "finished-job", Status: store.StatusSuccess}); err != nil {
		t.Fatal(err)
	}
	no := false
	var applied task.Task
	check := func(a Action) Action {
		t.Helper()
		out, err := src.Check(context.Background(), []Action{a})
		if err != nil || len(out) != 1 {
			t.Fatalf("Check: %+v, %v", out, err)
		}
		return out[0]
	}

	d := &TaskDraft{Provider: "claude"}
	applied, _ = findTask(cfg.Tasks, "nightly-abc")
	if p := d.applyTo(&applied, cfg, berlin); p != "" || applied.Model != "" {
		t.Errorf("a new provider kept the old model: %q, %+v", p, applied)
	}
	d = &TaskDraft{Clear: []string{"group", "model"}}
	applied, _ = findTask(cfg.Tasks, "nightly-abc")
	if p := d.applyTo(&applied, cfg, berlin); p != "" || applied.Group != "" || applied.Model != "" {
		t.Errorf("clear left settings: %q, %+v", p, applied)
	}

	if a := check(Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, Enabled: &no}}); !strings.Contains(a.Problem, "always queued enabled") {
		t.Errorf("a disabled new task was not refused: %q", a.Problem)
	}
	if a := check(Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, DependsOn: []string{"finished-job"}}}); a.Problem != "" {
		t.Errorf("a finished job in the history could not be waited for: %q", a.Problem)
	}
	if a := check(Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, DependsOn: []string{"nightly-abc"}}}); !strings.Contains(a.Problem, "schedule") {
		t.Errorf("waiting for a recurring job was not refused: %q", a.Problem)
	}
	if a := check(Action{Type: ActionAddTask, Task: &TaskDraft{Prompt: "p", WorkingDir: dir, DependsOn: []string{"gone"}}}); a.Problem == "" {
		t.Error("waiting for an unknown job was not refused")
	}
}
