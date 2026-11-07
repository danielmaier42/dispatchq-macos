package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/cliref"
)

// How the assistant serves each dispatchq command. Every command in the CLI
// reference has an entry, chosen on purpose: a test fails when the CLI grows a
// command nobody decided about.
type routeKind int

const (
	// routeLookup runs the command as a cli lookup: it only reads.
	routeLookup routeKind = iota
	// routeRunCLI proposes the command as a run_cli action the operator
	// confirms.
	routeRunCLI
	// routeTyped is served by an action of its own, which carries the app's
	// own confirmation card (and the task editor for task proposals).
	routeTyped
	// routeHandoff needs the operator: a terminal, a password, an editor.
	routeHandoff
)

type route struct {
	kind routeKind
	// how says what to use instead, for the typed and handed-off commands.
	how string
}

var routes = map[string]route{
	"list":             {routeLookup, ""},
	"show":             {routeLookup, ""},
	"add":              {routeTyped, "add_task, every flag a task field (--id id, --dir working_dir, --at fixed_at, --skip-permissions permissions \"skip\", --notify notify_on_result)"},
	"edit":             {routeTyped, "update_task with only the changing fields (--enabled enabled, --prompt-file: put the text in prompt); without flags it is open_task"},
	"queue":            {routeTyped, "add_task. queue is for a running job and inherits from it; here --depends-on is depends_on, --include-results include_results, and --in becomes fixed_at counted from now"},
	"publish":          {routeRunCLI, ""},
	"artifacts list":   {routeLookup, ""},
	"notify":           {routeRunCLI, ""},
	"export":           {routeRunCLI, ""},
	"import":           {routeRunCLI, ""},
	"rm":               {routeTyped, "delete_task"},
	"enable":           {routeTyped, "enable_task"},
	"disable":          {routeTyped, "disable_task"},
	"move":             {routeRunCLI, ""},
	"run-now":          {routeTyped, "run_now"},
	"status":           {routeLookup, ""},
	"read":             {routeRunCLI, ""},
	"read-all":         {routeRunCLI, ""},
	"settings":         {routeRunCLI, "without change flags it only reads, use it as a cli lookup then; --paused is pause_all / resume_all"},
	"update check":     {routeRunCLI, "it makes the app look for a release and show its prompt"},
	"uninstall":        {routeHandoff, "needs an administrator password: the operator runs it in a terminal, or uses Settings → System → Uninstall"},
	"provider list":    {routeLookup, ""},
	"provider show":    {routeLookup, ""},
	"provider check":   {routeLookup, ""},
	"provider limits":  {routeLookup, ""},
	"provider add":     {routeRunCLI, "without --env; logging the account in is the operator's step (Settings → Providers)"},
	"provider edit":    {routeRunCLI, "without --env"},
	"provider enable":  {routeRunCLI, ""},
	"provider disable": {routeRunCLI, ""},
	"provider default": {routeRunCLI, ""},
	"provider rm":      {routeRunCLI, ""},
	"pool list":        {routeLookup, ""},
	"pool next":        {routeLookup, ""},
	"pool show":        {routeLookup, ""},
	"pool add":         {routeRunCLI, ""},
	"pool edit":        {routeRunCLI, ""},
	"pool rm":          {routeRunCLI, ""},
	"--version":        {routeLookup, ""},
}

// secretHandoff is what the operator hears when a command would carry a
// credential through the conversation.
const secretHandoff = "Credentials never pass through the assistant: enter them in Settings yourself, or run the command in a terminal."

// checkCLI checks a command line the model wrote against the CLI reference and
// against what the assistant may do with it: a lookup only reads, an action
// only runs what has no action of its own.
func checkCLI(argv []string, lookup bool) (cliref.Parsed, error) {
	argv = withoutProgram(argv)
	// A command the assistant serves otherwise says so first, whatever its
	// flags: `edit ID` is update_task even before it has any.
	if c, _, ok := cliref.Lookup(argv); ok {
		switch r := routes[c.Name()]; r.kind {
		case routeHandoff:
			return cliref.Parsed{}, errors.New("dispatchq " + c.Name() + " " + r.how)
		case routeTyped:
			return cliref.Parsed{}, errors.New("use " + r.how + " for dispatchq " + c.Name())
		}
	}
	p, err := cliref.Check(argv, false)
	if errors.Is(err, cliref.ErrSecret) {
		return p, fmt.Errorf("%w. %s", err, secretHandoff)
	}
	if err != nil {
		return p, err
	}
	r := routes[p.Command.Name()]
	settingsRead := p.Command.Name() == "settings" && onlyJSON(p)
	switch {
	case lookup && r.kind != routeLookup && !settingsRead:
		return p, errors.New("dispatchq " + p.Command.Name() + " changes something; propose it as run_cli instead")
	case !lookup && (r.kind == routeLookup || settingsRead):
		return p, errors.New("dispatchq " + p.Command.Name() + " only reads; use it as a cli lookup")
	}
	if p.Command.Name() == "settings" && slices.ContainsFunc(p.Flags, func(f [2]string) bool { return f[0] == "paused" }) {
		return p, errors.New("use pause_all or resume_all to pause or resume every run")
	}
	return p, nil
}

// withoutProgram drops a leading program name the model may have written.
func withoutProgram(argv []string) []string {
	argv = slices.Clone(argv)
	if len(argv) > 0 && (argv[0] == "dispatchq" || strings.HasSuffix(argv[0], "/dispatchq") ||
		strings.HasSuffix(argv[0], `\dispatchq`) || strings.HasSuffix(argv[0], `\dispatchq.exe`)) {
		argv = argv[1:]
	}
	return argv
}

// shownArgv is a command line as it may be shown or repeated: without the
// program name and without any credential in it.
func shownArgv(argv []string) []string { return cliref.Redact(withoutProgram(argv)) }

func onlyJSON(p cliref.Parsed) bool {
	for _, f := range p.Flags {
		if f[0] != "json" {
			return false
		}
	}
	return true
}

// CLI runs a dispatchq command (satisfied by ExecCLI).
type CLI interface {
	Run(ctx context.Context, argv []string) (string, error)
}

// cliTimeout bounds one command; `provider list` probes every provider for up
// to a minute.
const cliTimeout = 90 * time.Second

// maxCLIOutput bounds what a command's output may add to the conversation or
// the card.
const maxCLIOutput = 8000

// ExecCLI runs the dispatchq CLI shipped with the app, as a program with its
// arguments, never through a shell.
type ExecCLI struct {
	// Bin is the absolute path of the CLI; empty when it was not found.
	Bin string
}

// runEnvVars mark a process as part of a run; a command the operator confirmed
// in the assistant belongs to no run.
var runEnvVars = []string{"DISPATCHQ_RUN_ID", "DISPATCHQ_TASK_ID", "DISPATCHQ_PARENT_TASK", "DISPATCHQ_WORKFLOW_ID"}

// Run implements CLI.
func (e ExecCLI) Run(ctx context.Context, argv []string) (string, error) {
	if e.Bin == "" {
		return "", errors.New("the dispatchq command line was not found next to the app, so the assistant cannot run it")
	}
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Bin, argv...) //nolint:gosec // argv was checked against the CLI reference
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(runEnvVars, name)
	})
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	out, err := cmd.CombinedOutput()
	text := clip(strings.TrimSpace(string(out)), maxCLIOutput)
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("dispatchq %s: %w", strings.Join(argv, " "), err)
		}
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

// RunCLI carries out a run_cli action the operator confirmed. It checks the
// command again: the app sends back what the card showed, and nothing else may
// get through.
func (s *Service) RunCLI(ctx context.Context, argv []string) (string, error) {
	p, err := checkCLI(argv, false)
	if err != nil {
		return "", err
	}
	if s.cli == nil {
		return "", errors.New("the assistant cannot run dispatchq commands in this build")
	}
	return s.cli.Run(ctx, p.Argv())
}
