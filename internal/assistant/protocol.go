package assistant

import (
	"encoding/json"
	"strings"
)

// Lookup is a detail the model asks for before it answers. The daemon answers
// it read-only from the store; nothing a lookup does changes anything.
type Lookup struct {
	Tool string `json:"tool"`
	Arg  string `json:"arg"`
	// Argv is the dispatchq command line a cli lookup runs, without the
	// program name: ["provider", "limits", "--json"].
	Argv []string `json:"argv,omitempty"`
}

// The lookups the model may ask for.
const (
	LookupTask      = "task"      // arg: task id; the whole task with its prompt and recent runs
	LookupRun       = "run"       // arg: run id; outcome, final output, error, log tail, artifacts
	LookupRuns      = "runs"      // arg: task id, a name fragment or a status; up to 20 runs
	LookupArtifact  = "artifact"  // arg: artifact id; metadata and the start of a text file
	LookupArtifacts = "artifacts" // arg: a search text ("" = newest); up to 20 artifacts
	LookupUsage     = "usage"     // arg: days, default 7; runs, cost and outcomes per provider
	LookupCLI       = "cli"       // argv: a read-only dispatchq command; its output
)

var lookupTools = []string{LookupTask, LookupRun, LookupRuns, LookupArtifact, LookupArtifacts, LookupUsage, LookupCLI}

// Action is one thing the answer does in the app.
//
// The open_* actions move the app and happen as soon as the answer arrives:
// they only show what is already there. Every other action changes the queue,
// so the app presents it as a card and nothing happens until the operator
// confirms it there.
type Action struct {
	Type string `json:"type"`
	// View is the view open_view opens.
	View string `json:"view,omitempty"`
	// ID names the task, run or artifact the action is about.
	ID string `json:"id,omitempty"`
	// Task is the job add_task files, or the fields update_task changes.
	Task *TaskDraft `json:"task,omitempty"`
	// Argv is the dispatchq command line run_cli runs, without the program
	// name. The daemon rewrites it to the form the CLI parses.
	Argv []string `json:"argv,omitempty"`
	// Command is Argv as a person would type it, filled in by the daemon for
	// the card.
	Command string `json:"command,omitempty"`
	// Danger marks a run_cli that removes something, filled in by the daemon.
	Danger bool `json:"danger,omitempty"`
	// Label names what ID refers to, filled in by the daemon for the card.
	Label string `json:"label,omitempty"`
	// Problem says why the action cannot be confirmed as it stands, filled in
	// by the daemon. The app still offers to open it in the task editor.
	Problem string `json:"problem,omitempty"`
}

// The actions.
const (
	ActionOpenView     = "open_view"
	ActionOpenRun      = "open_run"
	ActionOpenArtifact = "open_artifact"
	ActionOpenTask     = "open_task"
	ActionAddTask      = "add_task"
	ActionUpdateTask   = "update_task"
	ActionRunNow       = "run_now"
	ActionEnableTask   = "enable_task"
	ActionDisableTask  = "disable_task"
	ActionDeleteTask   = "delete_task"
	ActionCancelRun    = "cancel_run"
	ActionPauseAll     = "pause_all"
	ActionResumeAll    = "resume_all"
	// ActionRunCLI runs one dispatchq command, checked against the CLI
	// reference, once the operator confirmed it.
	ActionRunCLI = "run_cli"
)

var actionTypes = []string{
	ActionOpenView, ActionOpenRun, ActionOpenArtifact, ActionOpenTask,
	ActionAddTask, ActionUpdateTask, ActionRunNow, ActionEnableTask, ActionDisableTask,
	ActionDeleteTask, ActionCancelRun, ActionPauseAll, ActionResumeAll, ActionRunCLI,
}

// Views are the app views open_view may open.
var Views = []string{"dashboard", "queue", "log", "artifacts", "notifications", "usage", "feedback", "settings"}

// TaskDraft is a job as the assistant writes it: every setting `dispatchq add`,
// `edit` and `queue` take, under the names the task API uses. For update_task
// only the fields that change are set; a nil switch leaves it as it is.
type TaskDraft struct {
	ID              string   `json:"id,omitempty"`
	Name            string   `json:"name,omitempty"`
	Kind            string   `json:"kind,omitempty"`
	Prompt          string   `json:"prompt,omitempty"`
	WorkingDir      string   `json:"working_dir,omitempty"`
	Trigger         string   `json:"trigger,omitempty"`
	FixedAt         string   `json:"fixed_at,omitempty"`
	Cron            string   `json:"cron,omitempty"`
	Group           string   `json:"group,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Pool            string   `json:"pool,omitempty"`
	Model           string   `json:"model,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Permissions     string   `json:"permissions,omitempty"`
	Parallel        *bool    `json:"parallel,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
	NotifyOnResult  *bool    `json:"notify_on_result,omitempty"`
	QuietHistory    *bool    `json:"quiet_history,omitempty"`
	Backfill        *bool    `json:"backfill,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
	IncludeResults  *bool    `json:"include_results,omitempty"`
}

// empty reports whether the draft sets nothing.
func (d TaskDraft) empty() bool {
	b, _ := json.Marshal(d)
	return string(b) == "{}"
}

func enum(values []string) string {
	return `{"type":"string","enum":["` + strings.Join(values, `","`) + `"]}`
}

const stringList = `{"type":"array","items":{"type":"string"}}`

// schema constrains every model turn to the answer shape.
var schema = `{"type":"object","properties":{` +
	`"reply":{"type":"string"},` +
	`"lookups":{"type":"array","items":{"type":"object","properties":{` +
	`"tool":` + enum(lookupTools) + `,"arg":{"type":"string"},"argv":` + stringList + `},"required":["tool","arg"]}},` +
	`"actions":{"type":"array","items":{"type":"object","properties":{` +
	`"type":` + enum(actionTypes) + `,` +
	`"view":` + enum(Views) + `,` +
	`"id":{"type":"string"},` +
	`"argv":` + stringList + `,` +
	`"task":{"type":"object","properties":{` +
	`"id":{"type":"string"},` +
	`"name":{"type":"string"},` +
	`"kind":{"type":"string","enum":["agent","script"]},` +
	`"prompt":{"type":"string"},` +
	`"working_dir":{"type":"string"},` +
	`"trigger":{"type":"string","enum":["asap","fixed","cron"]},` +
	`"fixed_at":{"type":"string"},` +
	`"cron":{"type":"string"},` +
	`"group":{"type":"string"},` +
	`"provider":{"type":"string"},` +
	`"pool":{"type":"string"},` +
	`"model":{"type":"string"},` +
	`"reasoning_effort":{"type":"string"},` +
	`"permissions":{"type":"string","enum":["default","skip"]},` +
	`"parallel":{"type":"boolean"},` +
	`"enabled":{"type":"boolean"},` +
	`"notify_on_result":{"type":"boolean"},` +
	`"quiet_history":{"type":"boolean"},` +
	`"backfill":{"type":"boolean"},` +
	`"depends_on":` + stringList + `,` +
	`"include_results":{"type":"boolean"}}}},` +
	`"required":["type"]}}},` +
	`"required":["reply"]}`
