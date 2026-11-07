package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/cliref"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

// StoreSource reads what the assistant knows straight from the store. It
// writes nothing: every change the assistant proposes goes through the API the
// app already uses, after the operator confirmed it.
type StoreSource struct {
	Store *store.Store
	// CLI answers cli lookups; nil says the command line is not available.
	CLI CLI
	// Now is the clock; nil uses time.Now. Its location is the operator's.
	Now func() time.Time
}

// How much of the state every message carries. The overview is a map, not the
// territory: details come through lookups.
const (
	overviewTasks     = 80
	overviewRuns      = 12
	overviewArtifacts = 10
	listLimit         = 20
	maxActions        = 6
)

func (s *StoreSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Overview implements Source.
func (s *StoreSource) Overview(context.Context) (string, error) {
	cfg, err := s.Store.LoadConfig()
	if err != nil {
		return "", err
	}
	runs, err := s.Store.Runs()
	if err != nil {
		return "", err
	}
	arts, err := s.Store.Artifacts()
	if err != nil {
		return "", err
	}
	now := s.now()
	var b strings.Builder
	zone, _ := now.Zone()
	fmt.Fprintf(&b, "Now: %s (UTC%s, %s)\n", now.Format("Mon 2006-01-02 15:04"), now.Format("-07:00"), zone)
	fmt.Fprintf(&b, "All runs paused: %s\n", yesNo(cfg.Settings.Paused))
	if d := cfg.Settings.DefaultWorkingDir; d != "" {
		fmt.Fprintf(&b, "Default working directory: %s\n", d)
	}
	b.WriteString("Providers:")
	for _, p := range cfg.Providers {
		fmt.Fprintf(&b, " %s (%s", p.ID, p.Kind)
		if p.ID == cfg.Settings.DefaultProvider {
			b.WriteString(", default")
		}
		if !p.Enabled {
			b.WriteString(", disabled")
		}
		b.WriteString(");")
	}
	b.WriteString("\n")
	if len(cfg.Pools) > 0 {
		b.WriteString("Pools:")
		for _, p := range cfg.Pools {
			fmt.Fprintf(&b, " %s (%d members);", p.ID, len(p.Members))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "External notification channels: Pushover %s, ntfy %s, webhook %s\n",
		onOff(cfg.Settings.Pushover.Enabled), onOff(cfg.Settings.Ntfy.Enabled), onOff(cfg.Settings.Webhook.Enabled))

	last := lastRunByTask(runs)
	fmt.Fprintf(&b, "\nTasks in the queue (%d):\n", len(cfg.Tasks))
	if len(cfg.Tasks) == 0 {
		b.WriteString("(none)\n")
	}
	for i, t := range cfg.Tasks {
		if i == overviewTasks {
			fmt.Fprintf(&b, "… %d more\n", len(cfg.Tasks)-i)
			break
		}
		b.WriteString("- " + taskLine(t, now))
		if r, ok := last[t.ID]; ok {
			fmt.Fprintf(&b, " · last run %s %s", r.Status, r.StartedAt.In(now.Location()).Format("2006-01-02 15:04"))
		}
		b.WriteString("\n")
	}

	b.WriteString("\nRecent runs (newest first):\n")
	if len(runs) == 0 {
		b.WriteString("(none)\n")
	}
	for i := len(runs) - 1; i >= 0 && len(runs)-i <= overviewRuns; i-- {
		b.WriteString("- " + runLine(runs[i], now) + "\n")
	}

	b.WriteString("\nRecent artifacts (newest first):\n")
	if len(arts) == 0 {
		b.WriteString("(none)\n")
	}
	for i := len(arts) - 1; i >= 0 && len(arts)-i <= overviewArtifacts; i-- {
		b.WriteString("- " + artifactLine(arts[i], now) + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func taskLine(t task.Task, now time.Time) string {
	parts := []string{fmt.Sprintf("task `%s` %q", t.ID, t.Name)}
	switch t.Trigger {
	case task.TriggerCron:
		s := fmt.Sprintf("cron %q", t.Cron)
		if next, err := task.CronNext(t.Cron, now, 1); err == nil && len(next) == 1 {
			s += " next " + next[0].In(now.Location()).Format("2006-01-02 15:04")
		}
		parts = append(parts, s)
	case task.TriggerFixed:
		parts = append(parts, "once at "+t.FixedAt.In(now.Location()).Format("2006-01-02 15:04"))
	default:
		parts = append(parts, string(t.Trigger))
	}
	if !t.Enabled {
		parts = append(parts, "disabled")
	}
	if t.IsScript() {
		parts = append(parts, "script")
	}
	if t.Group != "" {
		parts = append(parts, "group "+strconv.Quote(t.Group))
	}
	if t.Provider != "" {
		parts = append(parts, "provider "+t.Provider)
	}
	if t.Pool != "" {
		parts = append(parts, "pool "+t.Pool)
	}
	if t.Model != "" {
		parts = append(parts, "model "+t.Model)
	}
	if t.Permissions == task.PermissionsSkip {
		parts = append(parts, "skip permissions")
	}
	parts = append(parts, "dir "+t.WorkingDir)
	return strings.Join(parts, " · ")
}

func runLine(r store.Run, now time.Time) string {
	parts := []string{
		fmt.Sprintf("run `%s` of %q (task `%s`)", r.RunID, r.TaskName, r.TaskID),
		string(r.Status),
		"started " + r.StartedAt.In(now.Location()).Format("2006-01-02 15:04"),
	}
	if r.FinishedAt != nil {
		parts = append(parts, "took "+r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String())
	}
	if r.Provider.ID != "" {
		parts = append(parts, "on "+r.Provider.ID)
	}
	if r.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", r.CostUSD))
	}
	if r.Error != "" {
		parts = append(parts, "error: "+oneLine(r.Error, 160))
	}
	return strings.Join(parts, " · ")
}

func artifactLine(a store.Artifact, now time.Time) string {
	parts := []string{fmt.Sprintf("artifact `%s` %q", a.ID, a.Title)}
	if a.TaskName != "" {
		parts = append(parts, "from "+strconv.Quote(a.TaskName))
	}
	if a.RunID != "" {
		parts = append(parts, "run `"+a.RunID+"`")
	}
	parts = append(parts, a.FileName, a.PublishedAt.In(now.Location()).Format("2006-01-02 15:04"))
	if a.Description != "" {
		parts = append(parts, oneLine(a.Description, 140))
	}
	return strings.Join(parts, " · ")
}

func lastRunByTask(runs []store.Run) map[string]store.Run {
	out := map[string]store.Run{}
	for _, r := range runs {
		out[r.TaskID] = r
	}
	return out
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Lookup implements Source.
func (s *StoreSource) Lookup(ctx context.Context, l Lookup) (string, string) {
	arg := oneLine(l.Arg, 200)
	switch l.Tool {
	case LookupTask:
		return "Read task " + arg, s.lookupTask(arg)
	case LookupRun:
		return "Read run " + arg, s.lookupRun(arg)
	case LookupRuns:
		return "Listed runs " + quoteOrAll(arg), s.lookupRuns(arg)
	case LookupArtifact:
		return "Read artifact " + arg, s.lookupArtifact(arg)
	case LookupArtifacts:
		return "Listed artifacts " + quoteOrAll(arg), s.lookupArtifacts(arg)
	case LookupUsage:
		return "Checked usage", s.lookupUsage(arg)
	case LookupCLI:
		return s.lookupCLI(ctx, l.Argv)
	}
	return "Unknown lookup " + l.Tool, "There is no lookup called " + strconv.Quote(l.Tool) + "."
}

func (s *StoreSource) lookupCLI(ctx context.Context, argv []string) (string, string) {
	p, err := checkCLI(argv, true)
	label := "Ran dispatchq " + oneLine(strings.Join(argv, " "), 120)
	if err != nil {
		return label, "Not run: " + err.Error()
	}
	label = "Ran " + p.String()
	if s.CLI == nil {
		return label, "The dispatchq command line is not available in this build."
	}
	out, err := s.CLI.Run(ctx, p.Argv())
	if err != nil {
		return label, "The command failed: " + err.Error()
	}
	if out == "" {
		return label, "(no output)"
	}
	return label, out
}

func quoteOrAll(s string) string {
	if s == "" {
		return "(newest)"
	}
	return strconv.Quote(s)
}

func (s *StoreSource) lookupTask(id string) string {
	cfg, err := s.Store.LoadConfig()
	if err != nil {
		return "Could not read the queue: " + err.Error()
	}
	t, ok := findTask(cfg.Tasks, id)
	if !ok {
		return "No task with id " + strconv.Quote(id) + " is in the queue."
	}
	now := s.now()
	var b strings.Builder
	b.WriteString(taskLine(t, now) + "\n")
	if len(t.DependsOn) > 0 {
		b.WriteString("Waits for: " + strings.Join(t.DependsOn, ", ") + "\n")
	}
	what := "Prompt"
	if t.IsScript() {
		what = "Script"
	}
	fmt.Fprintf(&b, "%s:\n%s\n", what, clip(t.Prompt, 4000))
	if runs, err := s.Store.Runs(); err == nil {
		var mine []store.Run
		for _, r := range runs {
			if r.TaskID == t.ID {
				mine = append(mine, r)
			}
		}
		b.WriteString("\nRecent runs:\n")
		if len(mine) == 0 {
			b.WriteString("(none)\n")
		}
		for i := len(mine) - 1; i >= 0 && len(mine)-i <= 5; i-- {
			b.WriteString("- " + runLine(mine[i], now) + "\n")
		}
	}
	return b.String()
}

func (s *StoreSource) lookupRun(id string) string {
	runs, err := s.Store.Runs()
	if err != nil {
		return "Could not read the run history: " + err.Error()
	}
	r, ok := findRun(runs, id)
	if !ok {
		return "No run with id " + strconv.Quote(id) + " is in the history."
	}
	var b strings.Builder
	b.WriteString(runLine(r, s.now()) + "\n")
	if r.NumTurns > 0 {
		fmt.Fprintf(&b, "Turns: %d · tokens in %d, out %d\n", r.NumTurns, r.InputTokens, r.OutputTokens)
	}
	if r.ResumeAt != nil {
		b.WriteString("Resumes at " + r.ResumeAt.In(s.now().Location()).Format("2006-01-02 15:04") + "\n")
	}
	if r.FinalOutput != "" {
		fmt.Fprintf(&b, "\nFinal output:\n%s\n", clip(r.FinalOutput, 3000))
	}
	if tail := logTail(s.Store.LogPath(r.RunID), 2500); tail != "" {
		fmt.Fprintf(&b, "\nEnd of the log:\n%s\n", tail)
	}
	if arts, err := s.Store.Artifacts(); err == nil {
		var mine []string
		for _, a := range arts {
			if a.RunID == r.RunID {
				mine = append(mine, artifactLine(a, s.now()))
			}
		}
		if len(mine) > 0 {
			b.WriteString("\nArtifacts it published:\n- " + strings.Join(mine, "\n- ") + "\n")
		}
	}
	return b.String()
}

func (s *StoreSource) lookupRuns(q string) string {
	runs, err := s.Store.Runs()
	if err != nil {
		return "Could not read the run history: " + err.Error()
	}
	lq := strings.ToLower(q)
	var b strings.Builder
	n := 0
	for i := len(runs) - 1; i >= 0 && n < listLimit; i-- {
		r := runs[i]
		if q != "" && string(r.Status) != lq && r.TaskID != q && !strings.Contains(strings.ToLower(r.TaskName), lq) {
			continue
		}
		b.WriteString("- " + runLine(r, s.now()) + "\n")
		n++
	}
	if n == 0 {
		return "No run matches " + strconv.Quote(q) + "."
	}
	return b.String()
}

func (s *StoreSource) lookupArtifact(id string) string {
	arts, err := s.Store.Artifacts()
	if err != nil {
		return "Could not read the artifacts: " + err.Error()
	}
	i := slices.IndexFunc(arts, func(a store.Artifact) bool { return a.ID == id })
	if i < 0 {
		return "No artifact with id " + strconv.Quote(id) + "."
	}
	a := arts[i]
	var b strings.Builder
	b.WriteString(artifactLine(a, s.now()) + "\n")
	fmt.Fprintf(&b, "Type: %s · %d bytes\n", a.ContentType, a.Size)
	if !isText(a.ContentType) {
		b.WriteString("Not a text file; its content cannot be read here.\n")
		return b.String()
	}
	text, err := readHead(s.Store.ArtifactContentPath(a), 64<<10)
	if err != nil {
		return b.String() + "The file could not be read: " + err.Error() + "\n"
	}
	if strings.Contains(a.ContentType, "html") {
		text = htmlText(text)
	}
	fmt.Fprintf(&b, "\nContent (beginning):\n%s\n", clip(strings.TrimSpace(text), 3000))
	return b.String()
}

func (s *StoreSource) lookupArtifacts(q string) string {
	arts, err := s.Store.Artifacts()
	if err != nil {
		return "Could not read the artifacts: " + err.Error()
	}
	lq := strings.ToLower(q)
	var b strings.Builder
	n := 0
	for i := len(arts) - 1; i >= 0 && n < listLimit; i-- {
		a := arts[i]
		hay := strings.ToLower(strings.Join([]string{a.Title, a.Description, a.FileName, a.TaskName, a.Group, a.OriginName}, " "))
		if q != "" && !strings.Contains(hay, lq) {
			continue
		}
		b.WriteString("- " + artifactLine(a, s.now()) + "\n")
		n++
	}
	if n == 0 {
		return "No artifact matches " + strconv.Quote(q) + "."
	}
	return b.String()
}

func (s *StoreSource) lookupUsage(arg string) string {
	days, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || days <= 0 {
		days = 7
	}
	days = min(days, 90)
	runs, err := s.Store.Runs()
	if err != nil {
		return "Could not read the run history: " + err.Error()
	}
	type tally struct {
		runs   int
		cost   float64
		status map[store.RunStatus]int
	}
	since := s.now().AddDate(0, 0, -days)
	per := map[string]*tally{}
	for _, r := range runs {
		if r.StartedAt.Before(since) {
			continue
		}
		id := r.Provider.ID
		if id == "" {
			id = "(unknown)"
		}
		t := per[id]
		if t == nil {
			t = &tally{status: map[store.RunStatus]int{}}
			per[id] = t
		}
		t.runs++
		t.cost += r.CostUSD
		t.status[r.Status]++
	}
	if len(per) == 0 {
		return fmt.Sprintf("No runs in the last %d days.", days)
	}
	ids := make([]string, 0, len(per))
	for id := range per {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	fmt.Fprintf(&b, "Last %d days:\n", days)
	for _, id := range ids {
		t := per[id]
		var st []string
		for k, v := range t.status {
			st = append(st, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(st)
		fmt.Fprintf(&b, "- %s: %d runs, $%.2f, %s\n", id, t.runs, t.cost, strings.Join(st, ", "))
	}
	return b.String()
}

func findTask(tasks []task.Task, id string) (task.Task, bool) {
	i := slices.IndexFunc(tasks, func(t task.Task) bool { return t.ID == id })
	if i < 0 {
		return task.Task{}, false
	}
	return tasks[i], true
}

func findRun(runs []store.Run, id string) (store.Run, bool) {
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].RunID == id {
			return runs[i], true
		}
	}
	return store.Run{}, false
}

func isText(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "markdown") || strings.Contains(ct, "yaml")
}

func readHead(path string, n int64) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a path the store resolved for an artifact it holds
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, n))
	return string(data), err
}

var (
	htmlDrop = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)>`)
	htmlTag  = regexp.MustCompile(`(?s)<[^>]*>`)
	blank    = regexp.MustCompile(`\n\s*\n+`)
)

// htmlText reduces a page to its readable text: enough for the model to say
// what a report is about, nowhere near a renderer.
func htmlText(s string) string {
	s = htmlDrop.ReplaceAllString(s, " ")
	s = htmlTag.ReplaceAllString(s, "\n")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(s)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	return blank.ReplaceAllString(strings.Join(lines, "\n"), "\n")
}

// logTail is the readable end of a run's log. Harness logs are JSON event
// streams; what a person (or the model) wants from them is the text the agent
// wrote and the tools it called, so that is what is kept. A line that is not
// JSON (a script's output) is kept as it is.
func logTail(path string, n int) string {
	f, err := os.Open(path) //nolint:gosec // the store's own log for a run it recorded
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	const window = 128 << 10
	seeked := false
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(-window, io.SeekEnd); err != nil {
			return ""
		}
		seeked = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	// A seek lands inside some event; that fragment is noise, not text.
	if seeked {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var v any
		if json.Unmarshal([]byte(line), &v) != nil {
			out = append(out, line)
			continue
		}
		out = append(out, eventText(v)...)
	}
	text := strings.Join(out, "\n")
	if r := []rune(text); len(r) > n {
		text = "…" + string(r[len(r)-n:])
	}
	return text
}

// eventText collects the "text" and tool names an event carries, wherever the
// harness nests them.
func eventText(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["type"].(string); ok && t == "tool_use" {
				if name, ok := x["name"].(string); ok {
					out = append(out, "[tool] "+name)
				}
			}
			if t, ok := x["text"].(string); ok && strings.TrimSpace(t) != "" {
				out = append(out, strings.TrimSpace(t))
			}
			for k, c := range x {
				if k != "text" {
					walk(c)
				}
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(v)
	return out
}

// Check implements Source.
func (s *StoreSource) Check(_ context.Context, actions []Action) ([]Action, error) {
	cfg, err := s.Store.LoadConfig()
	if err != nil {
		return nil, err
	}
	runs, err := s.Store.Runs()
	if err != nil {
		return nil, err
	}
	arts, err := s.Store.Artifacts()
	if err != nil {
		return nil, err
	}
	loc := s.now().Location()

	var out []Action
	seen := map[string]bool{}
	for _, a := range actions {
		a.Label, a.Problem, a.Command, a.Danger = "", "", "", false
		a.ID = strings.TrimSpace(a.ID)
		keep := true
		switch a.Type {
		case ActionOpenView:
			keep = slices.Contains(Views, a.View)
			a.ID, a.Task = "", nil
		case ActionOpenRun, ActionCancelRun:
			r, ok := findRun(runs, a.ID)
			keep = ok
			a.Label = r.TaskName + " · " + r.StartedAt.In(loc).Format("2006-01-02 15:04")
			// A run waiting out a usage limit can be canceled too: it is
			// dropped instead of resumed.
			if ok && a.Type == ActionCancelRun && r.Status != store.StatusRunning && r.Status != store.StatusRateLimited {
				a.Problem = "This run is not running."
			}
			a.View, a.Task = "", nil
		case ActionOpenArtifact:
			i := slices.IndexFunc(arts, func(x store.Artifact) bool { return x.ID == a.ID })
			keep = i >= 0
			if keep {
				a.Label = arts[i].Title
			}
			a.View, a.Task = "", nil
		case ActionOpenTask, ActionRunNow, ActionEnableTask, ActionDisableTask, ActionDeleteTask:
			t, ok := findTask(cfg.Tasks, a.ID)
			keep = ok
			a.Label = t.Name
			switch {
			case !ok:
			case a.Type == ActionEnableTask && t.Enabled:
				a.Problem = "This task is already enabled."
			case a.Type == ActionDisableTask && !t.Enabled:
				a.Problem = "This task is already disabled."
			}
			a.View, a.Task = "", nil
		case ActionUpdateTask:
			t, ok := findTask(cfg.Tasks, a.ID)
			keep = ok && a.Task != nil && !a.Task.empty()
			if keep {
				a.Label = t.Name
				a.Problem = a.Task.applyTo(&t, cfg, loc)
			}
			a.View = ""
		case ActionAddTask:
			keep = a.Task != nil
			if keep {
				t := task.Task{ID: "draft", Enabled: true, Permissions: task.PermissionsDefault}
				if a.Task.WorkingDir == "" {
					a.Task.WorkingDir = cfg.Settings.DefaultWorkingDir
				}
				// The card says which kind of job it files, so an agent
				// job is never mistaken for a script or the other way round.
				if a.Task.Kind == "" {
					a.Task.Kind = string(task.KindAgent)
				}
				a.Problem = a.Task.applyTo(&t, cfg, loc)
				a.Label = t.Name
			}
			a.ID, a.View = "", ""
		case ActionRunCLI:
			a.ID, a.View, a.Task = "", "", nil
			a.Command = "dispatchq " + strings.Join(a.Argv, " ")
			p, err := checkCLI(a.Argv, false)
			keep = len(a.Argv) > 0
			if err != nil {
				a.Problem = err.Error()
				break
			}
			a.Argv, a.Command = p.Argv(), p.String()
			a.Danger = p.Command.Access == cliref.Destructive
			a.Label = p.Command.Help
		case ActionPauseAll:
			if cfg.Settings.Paused {
				a.Problem = "All runs are already paused."
			}
			a.ID, a.View, a.Task = "", "", nil
		case ActionResumeAll:
			if !cfg.Settings.Paused {
				a.Problem = "Runs are not paused."
			}
			a.ID, a.View, a.Task = "", "", nil
		default:
			keep = false
		}
		if !keep {
			continue
		}
		key := a.Type + "\x00" + a.ID + "\x00" + a.View + "\x00" + strings.Join(a.Argv, "\x00")
		if a.Task == nil && seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
		if len(out) == maxActions {
			break
		}
	}
	return out, nil
}

// applyTo writes the draft's fields into t, normalizes the draft to what was
// applied (so the app files exactly what was checked), and reports why the
// result could not be filed, "" when it can. It follows `dispatchq edit`:
// turning a job into a script drops what only a model has, and naming a
// provider leaves a pool and the other way round.
func (d *TaskDraft) applyTo(t *task.Task, cfg store.Config, loc *time.Location) string {
	isNew := t.ID == "draft"
	d.ID = strings.TrimSpace(d.ID)
	d.Name = oneLine(d.Name, 80)
	d.WorkingDir = strings.TrimSpace(d.WorkingDir)
	d.Cron = oneLine(d.Cron, 100)
	d.Group = oneLine(d.Group, 80)
	if d.ID != "" {
		if !isNew {
			return "A task's id cannot be changed."
		}
		if err := task.CheckID(d.ID); err != nil {
			return err.Error()
		}
		if _, taken := findTask(cfg.Tasks, d.ID); taken {
			return "There already is a task " + strconv.Quote(d.ID) + "."
		}
		t.ID = d.ID
	}
	if d.Name != "" {
		t.Name = d.Name
	}
	if d.Kind != "" {
		t.Kind = task.Kind(d.Kind)
		if t.IsScript() {
			t.Provider, t.Pool, t.Model, t.ReasoningEffort = "", "", "", ""
			t.Permissions, t.Backfill = task.PermissionsDefault, false
		}
	}
	if strings.TrimSpace(d.Prompt) != "" {
		t.Prompt = d.Prompt
	}
	if d.WorkingDir != "" {
		t.WorkingDir = d.WorkingDir
	}
	if d.Group != "" {
		t.Group = d.Group
	}
	if d.Model != "" {
		t.Model = d.Model
	}
	if d.ReasoningEffort != "" {
		t.ReasoningEffort = d.ReasoningEffort
	}
	if d.Permissions != "" {
		t.Permissions = task.Permissions(d.Permissions)
	}
	if d.Provider != "" && d.Pool != "" {
		return "A task runs on a provider or on a pool, not both."
	}
	if d.Provider != "" {
		if !slices.ContainsFunc(cfg.Providers, func(p store.Provider) bool { return p.ID == d.Provider }) {
			return "There is no provider " + strconv.Quote(d.Provider) + "."
		}
		t.Provider, t.Pool = d.Provider, ""
	}
	if d.Pool != "" {
		if !slices.ContainsFunc(cfg.Pools, func(p store.Pool) bool { return p.ID == d.Pool }) {
			return "There is no pool " + strconv.Quote(d.Pool) + "."
		}
		t.Pool, t.Provider = d.Pool, ""
	}
	for _, sw := range []struct {
		from *bool
		to   *bool
	}{
		{d.Parallel, &t.Parallel}, {d.Enabled, &t.Enabled}, {d.NotifyOnResult, &t.NotifyOnResult},
		{d.QuietHistory, &t.QuietHistory}, {d.Backfill, &t.Backfill}, {d.IncludeResults, &t.IncludeResults},
	} {
		if sw.from != nil {
			*sw.to = *sw.from
		}
	}
	if len(d.DependsOn) > 0 {
		if !isNew && !slices.Equal(d.DependsOn, t.DependsOn) {
			return "Dependencies are fixed when a task is queued."
		}
		for _, dep := range d.DependsOn {
			if _, ok := findTask(cfg.Tasks, dep); !ok {
				return "There is no job " + strconv.Quote(dep) + " to wait for."
			}
		}
		t.DependsOn = d.DependsOn
	}
	if t.IncludeResults && len(t.DependsOn) == 0 {
		return "include_results needs depends_on."
	}
	// A schedule says which trigger it belongs to, so the model does not have
	// to repeat it.
	if d.Trigger == "" {
		switch {
		case d.Cron != "":
			d.Trigger = string(task.TriggerCron)
		case d.FixedAt != "":
			d.Trigger = string(task.TriggerFixed)
		case t.Trigger == "":
			d.Trigger = string(task.TriggerASAP)
		}
	}
	if d.Trigger != "" {
		t.Trigger = task.Trigger(d.Trigger)
	}
	if d.Cron != "" {
		t.Cron = d.Cron
	}
	if d.FixedAt != "" {
		at, ok := parseTime(d.FixedAt, loc)
		if !ok {
			return strconv.Quote(d.FixedAt) + " is not a date and time."
		}
		t.FixedAt = at
		d.FixedAt = at.Format(time.RFC3339)
	}
	if t.Name == "" {
		t.Name = "New task"
		d.Name = t.Name
	}
	if err := t.Validate(); err != nil {
		return strings.TrimPrefix(err.Error(), task.ErrInvalidTask.Error()+": ")
	}
	if !filepath.IsAbs(t.WorkingDir) {
		return "The working directory must be an absolute path."
	}
	if info, err := os.Stat(t.WorkingDir); err != nil || !info.IsDir() {
		return "The folder " + t.WorkingDir + " does not exist on this computer."
	}
	return ""
}

func parseTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
