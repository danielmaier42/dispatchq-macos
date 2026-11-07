// Inspecting and editing existing tasks: `dispatchq show` prints a task in full
// (including its whole prompt), `dispatchq edit` changes it — either field by
// field with flags, or interactively as TOML in $EDITOR.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// splitTaskID takes the task id off the front of a command's arguments; the
// rest are flags. Keeping the id positional and first is what makes
// `dispatchq edit ID --prompt-file P` unambiguous.
func splitTaskID(args []string) (string, []string, error) {
	return splitPositional(args, "a task id")
}

// splitPositional takes one positional argument (described by what, for the
// error message) off the front of a command's arguments; the rest are flags.
func splitPositional(args []string, what string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("expected %s", what)
	}
	if strings.HasPrefix(args[0], "-") {
		return "", nil, fmt.Errorf("expected %s before the flags, got %q", what, args[0])
	}
	return args[0], args[1:], nil
}

func findTask(st *store.Store, id string) (task.Task, error) {
	cfg, err := st.LoadConfig()
	if err != nil {
		return task.Task{}, err
	}
	for _, t := range cfg.Tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return task.Task{}, fmt.Errorf("task %q not found", id)
}

func cmdShow(st *store.Store, args []string) error {
	id, rest, err := splitTaskID(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the task as JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	t, err := findTask(st, id)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(t)
	}
	printTask(t)
	return nil
}

func printTask(t task.Task) {
	model := t.Model
	if model == "" {
		model = "(the provider's default model)"
	}
	fmt.Printf("id:                %s\n", t.ID)
	fmt.Printf("name:              %s\n", t.Name)
	fmt.Printf("kind:              %s\n", kindOf(t))
	fmt.Printf("enabled:           %t\n", t.Enabled)
	fmt.Printf("trigger:           %s\n", strings.TrimSpace(string(t.Trigger)+" "+triggerWhen(t)))
	fmt.Printf("working_dir:       %s\n", t.WorkingDir)
	fmt.Printf("group:             %s\n", orDefault(t.Group, "(ungrouped)"))
	fmt.Printf("parallel:          %t\n", t.Parallel)
	// A script job has none of these, and printing them as "(the default
	// provider)" would suggest it runs on one.
	if !t.IsScript() {
		if t.Pool != "" {
			fmt.Printf("pool:              %s\n", t.Pool)
		} else {
			fmt.Printf("provider:          %s\n", orDefault(t.Provider, "(the default provider)"))
		}
		fmt.Printf("model:             %s\n", model)
		fmt.Printf("reasoning_effort:  %s\n", orDefault(t.ReasoningEffort, "(the provider's own)"))
		fmt.Printf("permissions:       %s\n", t.Permissions)
		fmt.Printf("backfill:          %t\n", t.Backfill)
	}
	fmt.Printf("notify_on_result:  %t\n", t.NotifyOnResult)
	fmt.Printf("quiet_history:     %t\n", t.QuietHistory)
	body := "prompt"
	if t.IsScript() {
		body = "script"
	}
	fmt.Printf("\n%s:\n%s\n", body, t.Prompt)
}

// kindOf names a task's kind for display, spelling out the default that an
// empty value stands for.
func kindOf(t task.Task) string {
	if t.IsScript() {
		return string(task.KindScript)
	}
	return string(task.KindAgent)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// cmdEdit changes an existing task. With flags it patches the named fields;
// with no flags it opens the whole task as TOML in $EDITOR.
func cmdEdit(st *store.Store, args []string) error {
	id, rest, err := splitTaskID(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return editTaskInEditor(st, id)
	}

	patch, err := parseTaskPatch(rest, readPromptFile)
	if err != nil {
		return err
	}
	// Only a move to another provider is checked; every other edit goes through
	// whatever state the current provider is in, because that edit may be how the
	// operator is fixing it. A job that is (or becomes) a script has no provider
	// to check — the edit is refused by validation instead.
	if patch.has("provider") || patch.has("pool") {
		cur, err := findTask(st, id)
		if err != nil {
			return err
		}
		if patch.targetKind(cur.Kind) != task.KindScript {
			if err := ensureRunnable(st, patch.provider, patch.pool); err != nil {
				return err
			}
		}
	}
	if err := app.EditTask(st, id, func(t *task.Task) error {
		edited, err := patch.apply(*t)
		if err != nil {
			return err
		}
		if err := edited.Validate(); err != nil {
			return err
		}
		*t = edited
		return nil
	}); err != nil {
		return err
	}
	fmt.Printf("updated task %q\n", id)
	return nil
}

// taskSettings are the per-task settings that `edit`, `queue` and `add` all
// take as the same flags. Each one is applied only when its flag was passed
// (see passedFlags), so an explicit --parallel=false still overrides an
// existing or inherited true.
type taskSettings struct {
	kind         string
	group        string
	provider     string
	pool         string
	model        string
	reasoning    string
	parallel     bool
	skipPerms    bool
	notify       bool
	quietHistory bool
	backfill     bool
}

// register declares the setting flags on fs. dflt names what applies when a
// flag is left out, e.g. "inherited" for queue; it is appended to each help.
func (s *taskSettings) register(fs *flag.FlagSet, dflt string) {
	fs.StringVar(&s.kind, "kind", "", "agent (a prompt for a model) or script (a program, run without a model) (default: "+dflt+")")
	fs.StringVar(&s.group, "group", "", "queue group to file the task under; empty = ungrouped (default: "+dflt+")")
	fs.StringVar(&s.provider, "provider", "", "provider instance to run on; empty = the default provider (default: "+dflt+")")
	fs.StringVar(&s.pool, "pool", "", "provider pool to run on instead of one provider (dispatchq pool list) (default: "+dflt+")")
	fs.StringVar(&s.model, "model", "", "model override; empty = the provider's default model (default: "+dflt+")")
	fs.StringVar(&s.reasoning, "reasoning-effort", "", "how hard the model should think, for providers that take it (default: "+dflt+")")
	fs.BoolVar(&s.parallel, "parallel", false, "allow running alongside other parallel tasks (default: "+dflt+")")
	fs.BoolVar(&s.skipPerms, "skip-permissions", false, "bypass permission prompts (default: "+dflt+")")
	fs.BoolVar(&s.notify, "notify", false, "notify on the run's result, not just failures (default: "+dflt+")")
	fs.BoolVar(&s.quietHistory, "quiet-history", false, "drop successful runs from history (default: "+dflt+")")
	fs.BoolVar(&s.backfill, "backfill", false, "run only while the provider's weekly allowance would otherwise go unused (default: "+dflt+")")
}

// apply copies onto t every setting whose flag was passed, per has.
func (s taskSettings) apply(t *task.Task, has func(string) bool) {
	// Turning a job into a script drops what only a model has. The alternative
	// is refusing the edit over a provider the job no longer uses, which would
	// make `--kind script` fail on every task that ever named one.
	if has("kind") {
		t.Kind = task.Kind(s.kind)
		if t.Kind == task.KindAgent {
			t.Kind = "" // the default is stored as absent
		}
		if t.IsScript() {
			t.Provider, t.Pool, t.Model, t.ReasoningEffort = "", "", "", ""
			t.Permissions = task.PermissionsDefault
			t.Backfill = false
		}
	}
	// Changing the provider without naming a model drops the old provider's
	// model, so the new provider's own default applies (the resolution table in
	// internal/provider). Carrying a Claude model into another harness would
	// silently produce a run that cannot start.
	// A provider and a pool exclude each other: naming one clears the other,
	// so moving a task between them is one flag.
	if has("provider") {
		t.Provider = s.provider
		if !has("pool") {
			t.Pool = ""
		}
		if !has("model") {
			t.Model = ""
		}
	}
	if has("pool") {
		t.Pool = s.pool
		if !has("provider") {
			t.Provider = ""
		}
		if !has("model") {
			t.Model = ""
		}
	}
	if has("model") {
		t.Model = s.model
	}
	if has("group") {
		t.Group = strings.TrimSpace(s.group)
	}
	if has("reasoning-effort") {
		t.ReasoningEffort = s.reasoning
	}
	if has("parallel") {
		t.Parallel = s.parallel
	}
	if has("notify") {
		t.NotifyOnResult = s.notify
	}
	if has("quiet-history") {
		t.QuietHistory = s.quietHistory
	}
	if has("backfill") {
		t.Backfill = s.backfill
	}
	if has("skip-permissions") {
		t.Permissions = task.PermissionsFor(s.skipPerms)
	}
}

// passedFlags reports which flags of a parsed FlagSet were actually given. A
// stray positional argument is an error: for a boolean flag it usually means
// the value was written with a space, which the flag package cannot accept.
func passedFlags(fs *flag.FlagSet) (map[string]bool, error) {
	if fs.NArg() > 0 {
		arg := fs.Arg(0)
		if arg == "true" || arg == "false" {
			return nil, fmt.Errorf("unexpected argument %q (write boolean flags as --flag=%s)", arg, arg)
		}
		return nil, fmt.Errorf("unexpected argument %q", arg)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set, nil
}

// taskPatch is a parsed set of `dispatchq edit` flags: the values plus which of
// them the caller actually passed. Only the passed ones are applied, so editing
// the prompt never resets the schedule.
type taskPatch struct {
	taskSettings
	set     map[string]bool
	name    string
	prompt  string
	dir     string
	trigger string
	at      string
	cron    string
	enabled bool
}

func (p taskPatch) has(name string) bool { return p.set[name] }

// targetKind is the kind the task has once the patch is applied: the one the
// patch names, or the one it already had.
func (p taskPatch) targetKind(current task.Kind) task.Kind {
	if p.has("kind") {
		return task.Kind(p.kind)
	}
	return current
}

// parseTaskPatch parses the edit flags. readFile resolves --prompt-file; it is
// injected so the parsing stays testable without touching the filesystem.
func parseTaskPatch(args []string, readFile func(string) ([]byte, error)) (taskPatch, error) {
	fs := flag.NewFlagSet("edit", flag.ContinueOnError)
	var p taskPatch
	promptFile := fs.String("prompt-file", "", "read the prompt from a file ('-' = stdin)")
	fs.StringVar(&p.name, "name", "", "display name")
	fs.StringVar(&p.prompt, "prompt", "", "prompt sent to Claude Code")
	fs.StringVar(&p.dir, "dir", "", "working directory")
	fs.StringVar(&p.trigger, "trigger", "", "asap|fixed|cron")
	fs.StringVar(&p.at, "at", "", "RFC3339 start time (implies --trigger fixed)")
	fs.StringVar(&p.cron, "cron", "", "crontab expression (implies --trigger cron)")
	fs.BoolVar(&p.enabled, "enabled", false, "enable or pause the task")
	p.register(fs, "unchanged")
	if err := fs.Parse(args); err != nil {
		return taskPatch{}, err
	}
	var err error
	if p.set, err = passedFlags(fs); err != nil {
		return taskPatch{}, err
	}
	if len(p.set) == 0 {
		return taskPatch{}, fmt.Errorf("no changes given")
	}
	if p.has("prompt") && p.has("prompt-file") {
		return taskPatch{}, fmt.Errorf("choose either --prompt or --prompt-file")
	}
	if p.has("prompt-file") {
		data, err := readFile(*promptFile)
		if err != nil {
			return taskPatch{}, fmt.Errorf("read prompt file: %w", err)
		}
		p.prompt = string(data)
		p.set["prompt"] = true
	}
	return p, nil
}

// apply layers the patch onto a task. The trigger is derived from --trigger, or
// implied by --at / --cron; when it changes, the timing fields that no longer
// apply are cleared so the stored task stays consistent.
func (p taskPatch) apply(t task.Task) (task.Task, error) {
	if p.has("name") {
		t.Name = p.name
	}
	if p.has("prompt") {
		t.Prompt = p.prompt
	}
	if p.has("dir") {
		t.WorkingDir = fromCaller(p.dir)
	}
	if p.has("enabled") {
		t.Enabled = p.enabled
	}
	p.taskSettings.apply(&t, p.has)

	trigger := t.Trigger
	switch {
	case p.has("trigger"):
		trigger = task.Trigger(p.trigger)
	case p.has("at"):
		trigger = task.TriggerFixed
	case p.has("cron"):
		trigger = task.TriggerCron
	}
	if trigger != t.Trigger {
		if trigger != task.TriggerFixed {
			t.FixedAt = time.Time{}
		}
		if trigger != task.TriggerCron {
			t.Cron = ""
		}
		t.Trigger = trigger
	}

	if p.has("at") {
		parsed, err := time.Parse(time.RFC3339, p.at)
		if err != nil {
			return task.Task{}, fmt.Errorf("invalid --at time (want RFC3339): %w", err)
		}
		t.FixedAt = parsed
	}
	if p.has("cron") {
		t.Cron = p.cron
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	return t, nil
}

// readPromptFile reads a prompt from a file, or from stdin for "-".
func readPromptFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// taskDoc is the editable TOML view of a task. Every setting is always present,
// and the prompt comes last as a multi-line string so a long brief stays
// readable. It mirrors task.Task rather than reusing it, which keeps the on-disk
// config.toml format untouched and lets fixed_at be an empty-able RFC3339
// string.
type taskDoc struct {
	ID              string `toml:"id"`
	Name            string `toml:"name"`
	Kind            string `toml:"kind"`
	Enabled         bool   `toml:"enabled"`
	WorkingDir      string `toml:"working_dir"`
	Group           string `toml:"group"`
	Trigger         string `toml:"trigger"`
	FixedAt         string `toml:"fixed_at"`
	Cron            string `toml:"cron"`
	Parallel        bool   `toml:"parallel"`
	Provider        string `toml:"provider"`
	Pool            string `toml:"pool"`
	Model           string `toml:"model"`
	ReasoningEffort string `toml:"reasoning_effort"`
	Permissions     string `toml:"permissions"`
	NotifyOnResult  bool   `toml:"notify_on_result"`
	QuietHistory    bool   `toml:"quiet_history"`
	Backfill        bool   `toml:"backfill"`
	Prompt          string `toml:"prompt,multiline"`
}

const taskDocHeader = `# dispatchq task — edit, save, and close this file to apply.
# Leaving it unchanged (or emptying it) cancels the edit.
#
#   id                 read-only; changing it is rejected
#   kind               agent (a prompt for a model) | script (a program, no model)
#                      a script job has no provider, model, reasoning_effort or
#                      permissions: leave them empty
#   group              queue group; empty means the task is ungrouped
#   trigger            asap | fixed | cron
#   fixed_at           RFC3339 start time, for trigger = "fixed"
#   cron               5-field crontab expression, for trigger = "cron"
#   provider           empty = the default provider (dispatchq provider list)
#   pool               a provider pool instead of a provider (dispatchq pool list);
#                      leave provider empty when you set it
#   model              empty = the provider's own default model
#   reasoning_effort   empty = the provider's own; ignored by providers without it
#   permissions        default | skip  (skip bypasses permission prompts)
#   quiet_history      true drops successful runs from history (frequent watcher jobs)
#   backfill           true runs only while the provider's weekly allowance would
#                      otherwise go unused (urgency above dispatchq settings
#                      --backfill-urgency); never for a script job
`

func encodeTaskDoc(t task.Task) ([]byte, error) {
	d := taskDoc{
		ID: t.ID, Name: t.Name, Kind: kindOf(t), Enabled: t.Enabled, WorkingDir: t.WorkingDir, Group: t.Group,
		Trigger: string(t.Trigger), Cron: t.Cron, Parallel: t.Parallel,
		Provider: t.Provider, Pool: t.Pool, Model: t.Model, ReasoningEffort: t.ReasoningEffort,
		Permissions:    string(t.Permissions),
		NotifyOnResult: t.NotifyOnResult, QuietHistory: t.QuietHistory, Backfill: t.Backfill, Prompt: t.Prompt,
	}
	if !t.FixedAt.IsZero() {
		d.FixedAt = t.FixedAt.Local().Format(time.RFC3339)
	}
	body, err := toml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode task: %w", err)
	}
	return append([]byte(taskDocHeader+"\n"), body...), nil
}

// decodeTaskDoc parses an edited task document back into a task. orig supplies
// the identity the result is checked against — a renamed id is rejected rather
// than silently creating or overwriting another task.
func decodeTaskDoc(data []byte, orig task.Task) (task.Task, error) {
	var d taskDoc
	if err := toml.Unmarshal(data, &d); err != nil {
		return task.Task{}, fmt.Errorf("parse task: %w", err)
	}
	if d.ID == "" {
		return task.Task{}, fmt.Errorf("the document has no id line (expected id = %q)", orig.ID)
	}
	if d.ID != orig.ID {
		return task.Task{}, fmt.Errorf("task id cannot be changed (%q -> %q)", orig.ID, d.ID)
	}
	t := task.Task{
		ID: d.ID, Name: d.Name, Kind: task.Kind(d.Kind), Prompt: d.Prompt, WorkingDir: d.WorkingDir,
		Group:   strings.TrimSpace(d.Group),
		Trigger: task.Trigger(d.Trigger), Cron: d.Cron, Parallel: d.Parallel,
		Enabled: d.Enabled, Provider: d.Provider, Pool: d.Pool, Model: d.Model, ReasoningEffort: d.ReasoningEffort,
		Permissions: task.Permissions(d.Permissions), NotifyOnResult: d.NotifyOnResult,
		QuietHistory: d.QuietHistory, Backfill: d.Backfill,
	}
	if t.Permissions == "" {
		t.Permissions = task.PermissionsDefault
	}
	if t.Kind == task.KindAgent {
		t.Kind = "" // the default is stored as absent, so the file stays as it was
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	if strings.TrimSpace(d.FixedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(d.FixedAt))
		if err != nil {
			return task.Task{}, fmt.Errorf("invalid fixed_at (want RFC3339): %w", err)
		}
		t.FixedAt = parsed
	}
	if err := t.Validate(); err != nil {
		return task.Task{}, err
	}
	return t, nil
}

func editTaskInEditor(st *store.Store, id string) error {
	if !isInteractiveTerminal() {
		return fmt.Errorf("no terminal to open an editor in; pass flags instead (dispatchq edit %s --help)", id)
	}
	orig, err := findTask(st, id)
	if err != nil {
		return err
	}
	before, err := encodeTaskDoc(orig)
	if err != nil {
		return err
	}

	dir, err := os.MkdirTemp("", "dispatchq-edit")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "task-"+id+".toml")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	editorErr := runEditor(path)

	after, err := os.ReadFile(path)
	if err != nil {
		if editorErr != nil {
			return editorErr
		}
		return fmt.Errorf("read edited file: %w", err)
	}
	if editorErr != nil {
		// The editor may well have saved before it failed, so hand back the
		// draft rather than discarding work the user already typed.
		if !bytes.Equal(before, after) {
			keep = true
			return fmt.Errorf("%w\nyour edits are kept at %s", editorErr, path)
		}
		return editorErr
	}
	if bytes.Equal(before, after) || len(bytes.TrimSpace(after)) == 0 {
		fmt.Println("no changes")
		return nil
	}
	if err := applyEditedDoc(st, orig, before, after); err != nil {
		keep = true // never throw away what the user just typed
		return fmt.Errorf("%w\nyour edits are kept at %s", err, path)
	}
	fmt.Printf("updated task %q\n", id)
	return nil
}

// applyEditedDoc stores an edited document. before is what the editor was
// handed; if the stored task no longer matches it, someone else (the app, the
// daemon, another CLI call) changed the task while the editor was open, and
// writing would silently drop their change — so the edit is refused instead.
func applyEditedDoc(st *store.Store, orig task.Task, before, after []byte) error {
	edited, err := decodeTaskDoc(after, orig)
	if err != nil {
		return err
	}
	return app.EditTask(st, orig.ID, func(t *task.Task) error {
		current, err := encodeTaskDoc(*t)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, before) {
			return fmt.Errorf("the task changed while your editor was open; re-run the edit")
		}
		*t = edited
		return nil
	})
}

// runEditor opens path in the user's editor and waits for it to close.
func runEditor(path string) error {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), defaultEditor)
	cmd := editorCommand(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q: %w", editor, err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// isInteractiveTerminal reports whether there is a terminal to hand to an
// editor. Both ends must be one: when dispatchq is driven by a script, an agent,
// or the daemon, stdin or stdout is a pipe or socket, and opening an editor
// there would hang or garble the caller's output.
func isInteractiveTerminal() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
