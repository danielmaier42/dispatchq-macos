// Package cliref is the reference of the dispatchq command line: every command,
// its arguments and its flags, with what running it does to DispatchQ.
//
// It is the single description of the CLI that other parts of DispatchQ read
// instead of keeping their own copy: the built-in assistant learns the commands
// from it and checks every command it proposes against it. A test in
// cmd/dispatchq holds it to the real parser, so a command or flag added to the
// CLI and not described here fails the build's tests.
package cliref

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Access says what running a command does.
type Access int

// The kinds of access.
const (
	// Read changes nothing.
	Read Access = iota
	// Write changes DispatchQ's queue, settings or data.
	Write
	// Destructive removes something that cannot be brought back.
	Destructive
	// Interactive needs a person at a terminal: an editor, a password.
	Interactive
)

func (a Access) String() string {
	switch a {
	case Read:
		return "read"
	case Write:
		return "write"
	case Destructive:
		return "destructive"
	default:
		return "interactive"
	}
}

// Flag is one option of a command.
type Flag struct {
	Name string
	// Value names the value in the synopsis; empty for a boolean flag.
	Value string
	// Bool flags take no value, or an explicit =true / =false.
	Bool bool
	// Repeat flags may be given several times.
	Repeat bool
	// Secret flags carry a credential.
	Secret bool
	// Path values are file system paths.
	Path bool
	// OldName is set on a flag kept only for scripts written against an older
	// release; it names the flag to use instead.
	OldName string
	Help    string
}

// Arg is one positional argument.
type Arg struct {
	Name     string
	Optional bool
	Path     bool
}

// Command is one command of the CLI.
type Command struct {
	// Path is the command words, e.g. ["provider", "edit"].
	Path   []string
	Args   []Arg
	Flags  []Flag
	Access Access
	Help   string
	// MinFlags is how many flags the command needs to do what Access says.
	// `edit ID` without flags opens an editor instead.
	MinFlags int
	// Daemon marks commands that ask the running app rather than the store.
	Daemon bool
}

// Name is the command words joined by spaces.
func (c Command) Name() string { return strings.Join(c.Path, " ") }

// Flag returns the flag called name.
func (c Command) Flag(name string) (Flag, bool) {
	i := slices.IndexFunc(c.Flags, func(f Flag) bool { return f.Name == name })
	if i < 0 {
		return Flag{}, false
	}
	return c.Flags[i], true
}

// Synopsis is the usage line, without the program name.
func (c Command) Synopsis() string {
	parts := []string{c.Name()}
	for _, a := range c.Args {
		if a.Optional {
			parts = append(parts, "["+a.Name+"]")
		} else {
			parts = append(parts, a.Name)
		}
	}
	for _, f := range c.Flags {
		s := "--" + f.Name
		if f.Bool {
			s += "[=BOOL]"
		} else {
			s += " " + f.Value
		}
		if f.Repeat {
			s += "..."
		}
		parts = append(parts, "["+s+"]")
	}
	return strings.Join(parts, " ")
}

func flag(name, value, help string) Flag { return Flag{Name: name, Value: value, Help: help} }
func boolFlag(name, help string) Flag    { return Flag{Name: name, Bool: true, Help: help} }
func pathFlag(name, help string) Flag    { return Flag{Name: name, Value: "PATH", Path: true, Help: help} }
func secret(name, value, help string) Flag {
	return Flag{Name: name, Value: value, Secret: true, Help: help}
}
func arg(name string) Arg { return Arg{Name: name} }

var jsonFlag = boolFlag("json", "print JSON")

// taskFlags are the settings add, edit and queue share.
func taskFlags() []Flag {
	return []Flag{
		flag("name", "N", "display name"),
		flag("prompt", "P", "the agent prompt, or the program text of a script job"),
		pathFlag("dir", "working directory"),
		flag("kind", "agent|script", "agent: send the prompt to a model; script: run it as a program, no model"),
		flag("group", "G", "queue group"),
		flag("at", "RFC3339", "run once at this time"),
		flag("cron", "EXPR", "5-field cron expression in local time"),
		flag("provider", "ID", "provider instance (not together with --pool)"),
		flag("pool", "ID", "provider pool (not together with --provider)"),
		flag("model", "M", "model name"),
		flag("reasoning-effort", "E", "reasoning effort the harness supports"),
		boolFlag("parallel", "may run alongside other parallel tasks"),
		boolFlag("skip-permissions", "run with permissions skip: may do anything without asking"),
		boolFlag("notify", "send a notification with the outcome"),
		boolFlag("quiet-history", "keep successful runs out of the history"),
		boolFlag("backfill", "run only on spare allowance"),
	}
}

func with(base []Flag, extra ...Flag) []Flag { return append(base, extra...) }

var commands = []Command{
	{Path: []string{"list"}, Flags: []Flag{jsonFlag}, Access: Read, Help: "the queued tasks"},
	{Path: []string{"show"}, Args: []Arg{arg("ID")}, Flags: []Flag{jsonFlag}, Access: Read, Help: "one task in full, prompt included"},
	{Path: []string{"add"}, Flags: with([]Flag{flag("id", "ID", "unique task id")}, with(taskFlags(), flag("trigger", "asap|fixed|cron", "when it runs"))...), Access: Write, Help: "add a task"},
	{Path: []string{"edit"}, Args: []Arg{arg("ID")}, Flags: with(taskFlags(),
		flag("trigger", "asap|fixed|cron", "when it runs"),
		pathFlag("prompt-file", "read the prompt from a file"),
		boolFlag("enabled", "enable or disable the task"),
	), Access: Write, MinFlags: 1, Help: "change a task; only the flags given change (without flags it opens $EDITOR)"},
	{Path: []string{"queue"}, Flags: with(taskFlags(),
		flag("in", "DUR", "run after a delay such as 90m"),
		Flag{Name: "depends-on", Value: "JOBID", Repeat: true, Help: "wait for an existing job; repeatable"},
		boolFlag("include-results", "put the awaited jobs' answers in front of the prompt (a script's stdin)"),
		jsonFlag,
	), Access: Write, Help: "queue a follow-up task; settings not given are inherited from the calling task"},
	{Path: []string{"publish"}, Flags: []Flag{
		pathFlag("file", "the file to publish"),
		flag("title", "T", "short title"),
		flag("description", "D", "one-line summary"),
	}, Access: Write, Help: "publish a file as an artifact"},
	{Path: []string{"artifacts", "list"}, Flags: []Flag{
		jsonFlag,
		flag("origin", "ID", "only one parent job's artifacts"),
		flag("since", "TS|DUR", "published after an RFC3339 time or within a duration"),
	}, Access: Read, Help: "published artifacts, oldest first"},
	{Path: []string{"notify"}, Flags: []Flag{
		flag("title", "T", "notification title"),
		flag("message", "M", "notification text"),
		flag("url", "U", "http(s) link the notification opens"),
	}, Access: Write, Help: "send a notification over the configured channels"},
	{Path: []string{"export"}, Args: []Arg{arg("ID")}, Flags: []Flag{
		pathFlag("out", "where to write the .dispatchq file"),
		boolFlag("force", "overwrite an existing file"),
	}, Access: Write, Help: "write a task to a shareable .dispatchq file"},
	{Path: []string{"import"}, Args: []Arg{{Name: "PATH", Path: true}}, Flags: []Flag{
		flag("id", "ID", "id for the imported task"),
		flag("provider", "ID", "provider to run it on"),
		flag("model", "NAME", "model to run it with"),
	}, Access: Write, Help: "add a task from a .dispatchq file"},
	{Path: []string{"rm"}, Args: []Arg{arg("ID")}, Access: Destructive, Help: "remove a task"},
	{Path: []string{"enable"}, Args: []Arg{arg("ID")}, Access: Write, Help: "enable a task"},
	{Path: []string{"disable"}, Args: []Arg{arg("ID")}, Access: Write, Help: "disable a task"},
	{Path: []string{"move"}, Args: []Arg{arg("ID"), arg("INDEX")}, Access: Write, Help: "move a task to a queue position (0 = highest priority)"},
	{Path: []string{"run-now"}, Args: []Arg{arg("ID")}, Access: Write, Help: "run a task once, now"},
	{Path: []string{"status"}, Flags: []Flag{boolFlag("all", "every run, not the last 20"), jsonFlag}, Access: Read, Help: "recent runs"},
	{Path: []string{"read"}, Args: []Arg{arg("RUNID")}, Access: Write, Help: "mark a run read"},
	{Path: []string{"read-all"}, Access: Write, Help: "mark every run read"},
	{Path: []string{"settings"}, Flags: []Flag{
		jsonFlag,
		flag("default-provider", "ID", "provider for tasks that name none"),
		pathFlag("default-working-dir", "working directory new tasks start in"),
		flag("heartbeat-minutes", "N", "safety-net wake interval"),
		flag("idle-timeout-minutes", "N", "kill a run silent this long (negative = never)"),
		flag("max-run-history", "N", "runs kept (negative = all)"),
		flag("system-prompt", "S", "custom system prompt for every run"),
		pathFlag("system-prompt-file", "read the custom system prompt from a file"),
		boolFlag("paused", "stop or resume every run globally"),
		flag("backfill-urgency", "X", "provider urgency above which backfill tasks run"),
		boolFlag("prompt-review", "review prompts before they are queued"),
		flag("prompt-review-provider", "ID", "provider that reviews prompts"),
		flag("prompt-review-model", "M", "model that reviews prompts"),
		flag("assistant-provider", "ID", "provider the assistant answers on"),
		flag("assistant-model", "M", "model the assistant answers with"),
		{Name: "feedback-provider", Value: "ID", OldName: "assistant-provider", Help: "old name of --assistant-provider"},
		{Name: "feedback-model", Value: "M", OldName: "assistant-model", Help: "old name of --assistant-model"},
		boolFlag("artifact-notify", "notify when an artifact is published"),
		boolFlag("pushover", "Pushover channel on or off"),
		secret("pushover-token", "T", "Pushover application token"),
		secret("pushover-user", "U", "Pushover user key"),
		boolFlag("ntfy", "ntfy channel on or off"),
		flag("ntfy-server", "S", "ntfy server"),
		flag("ntfy-topic", "T", "ntfy topic"),
		secret("ntfy-token", "T", "ntfy access token"),
		boolFlag("webhook", "webhook channel on or off"),
		secret("webhook-url", "U", "webhook URL (may carry a token)"),
		flag("webhook-template", "J", "JSON body template"),
	}, Access: Write, Help: "show the settings (credentials masked) or change the ones given"},
	{Path: []string{"update", "check"}, Flags: []Flag{jsonFlag}, Access: Read, Daemon: true, Help: "the running app checks for a newer release and shows its prompt"},
	{Path: []string{"uninstall"}, Flags: []Flag{
		boolFlag("purge", "also delete tasks, history and settings"),
		boolFlag("yes", "do not ask before removing"),
	}, Access: Interactive, Help: "run the uninstaller (sudo asks for a password)"},
	{Path: []string{"provider", "list"}, Flags: []Flag{jsonFlag}, Access: Read, Help: "provider instances with readiness (probes each)"},
	{Path: []string{"provider", "show"}, Args: []Arg{arg("ID")}, Flags: []Flag{jsonFlag}, Access: Read, Help: "one provider (environment variable names only)"},
	{Path: []string{"provider", "check"}, Args: []Arg{arg("ID")}, Flags: []Flag{jsonFlag}, Access: Read, Help: "probe one provider now"},
	{Path: []string{"provider", "limits"}, Args: []Arg{{Name: "ID", Optional: true}}, Flags: []Flag{jsonFlag}, Access: Read, Help: "how much of each allowance is used"},
	{Path: []string{"provider", "add"}, Args: []Arg{arg("ID")}, Flags: []Flag{
		flag("kind", "KIND", "harness kind"),
		flag("name", "N", "display name"),
		pathFlag("path", "the harness executable"),
		pathFlag("config-dir", "the account's configuration directory"),
		flag("default-model", "MODEL", "model for tasks that name none"),
		flag("fallback", "ID", "provider that takes over when this one is limited"),
		flag("fallback-model", "MODEL", "model on the fallback"),
		{Name: "env", Value: "NAME=VALUE", Repeat: true, Secret: true, Help: "environment variable for the harness, such as an API key"},
		flag("wsl-distro", "DISTRO", "Windows only: run the harness inside this WSL distribution (paths are then Linux paths; fixed once added)"),
		flag("wsl-user", "USER", "with --wsl-distro: the Linux user to run as (empty = the distribution's default user)"),
	}, Access: Write, Help: "add a provider instance"},
	{Path: []string{"provider", "edit"}, Args: []Arg{arg("ID")}, Flags: []Flag{
		flag("name", "N", "display name"),
		pathFlag("path", "the harness executable"),
		pathFlag("config-dir", "the account's configuration directory"),
		flag("default-model", "MODEL", "model for tasks that name none"),
		flag("fallback", "ID|none", "provider that takes over when this one is limited"),
		flag("fallback-model", "MODEL|none", "model on the fallback"),
		{Name: "env", Value: "NAME=VALUE", Repeat: true, Secret: true, Help: "add or replace an environment variable, such as an API key"},
		{Name: "unset-env", Value: "NAME", Repeat: true, Help: "remove an environment variable"},
	}, Access: Write, MinFlags: 1, Help: "change a provider instance"},
	{Path: []string{"provider", "enable"}, Args: []Arg{arg("ID")}, Access: Write, Help: "enable a provider"},
	{Path: []string{"provider", "disable"}, Args: []Arg{arg("ID")}, Access: Write, Help: "disable a provider"},
	{Path: []string{"provider", "default"}, Args: []Arg{arg("ID")}, Access: Write, Help: "run tasks that name no provider on it"},
	{Path: []string{"provider", "rm"}, Args: []Arg{arg("ID")}, Access: Destructive, Help: "remove a provider"},
	{Path: []string{"pool", "list"}, Flags: []Flag{jsonFlag}, Access: Read, Help: "provider pools"},
	{Path: []string{"pool", "next"}, Args: []Arg{arg("ID")}, Flags: []Flag{jsonFlag}, Access: Read, Daemon: true, Help: "the member the next run goes to, and why"},
	{Path: []string{"pool", "show"}, Args: []Arg{arg("ID")}, Flags: []Flag{jsonFlag}, Access: Read, Daemon: true, Help: "the members, ranked"},
	{Path: []string{"pool", "add"}, Args: []Arg{arg("ID")}, Flags: []Flag{
		{Name: "member", Value: "PROVIDER[=WEIGHT]", Repeat: true, Help: "a member, optionally weighted by plan size"},
		flag("name", "N", "display name"),
	}, Access: Write, Help: "add a pool"},
	{Path: []string{"pool", "edit"}, Args: []Arg{arg("ID")}, Flags: []Flag{
		flag("name", "N", "display name"),
		{Name: "member", Value: "PROVIDER[=WEIGHT]", Repeat: true, Help: "the new member list (replaces the current one)"},
	}, Access: Write, MinFlags: 1, Help: "change a pool"},
	{Path: []string{"pool", "rm"}, Args: []Arg{arg("ID")}, Access: Destructive, Help: "remove a pool"},
	{Path: []string{"--version"}, Access: Read, Help: "the version"},
}

// Commands returns every command, in reference order.
func Commands() []Command {
	out := make([]Command, len(commands))
	copy(out, commands)
	return out
}

// Lookup returns the command named by the leading words of argv and the
// arguments after them.
func Lookup(argv []string) (Command, []string, bool) {
	best := -1
	for i, c := range commands {
		if len(argv) >= len(c.Path) && slices.Equal(argv[:len(c.Path)], c.Path) &&
			(best < 0 || len(c.Path) > len(commands[best].Path)) {
			best = i
		}
	}
	if best < 0 {
		return Command{}, nil, false
	}
	c := commands[best]
	return c, argv[len(c.Path):], true
}

// ErrSecret reports a credential in a command that must not carry one.
var ErrSecret = errors.New("carries a credential")

// Parsed is a command line checked against the reference.
type Parsed struct {
	Command Command
	Args    []string
	// Flags holds each flag given, in order, as name and value ("true" or
	// "false" for a boolean flag given without a value).
	Flags [][2]string
}

// Check parses argv the way the CLI does and refuses anything the reference
// does not describe: an unknown command or flag, a missing or extra argument,
// a relative path, a value for a boolean flag that is not a boolean. With
// allowSecrets false it also refuses a flag that carries a credential (ErrSecret).
func Check(argv []string, allowSecrets bool) (Parsed, error) {
	c, rest, ok := Lookup(argv)
	if !ok {
		if len(argv) == 0 {
			return Parsed{}, errors.New("no command")
		}
		return Parsed{}, fmt.Errorf("dispatchq has no command %q", strings.Join(argv, " "))
	}
	p := Parsed{Command: c}
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if a == "--" {
			p.Args = append(p.Args, rest[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			p.Args = append(p.Args, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f, ok := c.Flag(name)
		if !ok {
			return Parsed{}, fmt.Errorf("%s has no flag --%s", c.Name(), name)
		}
		if f.Secret && !allowSecrets {
			return Parsed{}, fmt.Errorf("--%s %w", name, ErrSecret)
		}
		if seen[name] && !f.Repeat {
			return Parsed{}, fmt.Errorf("--%s is given twice", name)
		}
		seen[name] = true
		switch {
		case f.Bool && hasValue:
			b, err := strconv.ParseBool(value)
			if err != nil {
				return Parsed{}, fmt.Errorf("--%s takes true or false, not %q", name, value)
			}
			value = strconv.FormatBool(b)
		case f.Bool:
			value = "true"
		case !hasValue:
			if i+1 >= len(rest) {
				return Parsed{}, fmt.Errorf("--%s needs a value", name)
			}
			i++
			value = rest[i]
		}
		if f.Path && !filepath.IsAbs(value) {
			return Parsed{}, fmt.Errorf("--%s must be an absolute path, not %q", name, value)
		}
		p.Flags = append(p.Flags, [2]string{name, value})
	}
	required := 0
	for _, a := range c.Args {
		if !a.Optional {
			required++
		}
	}
	if len(p.Args) < required || len(p.Args) > len(c.Args) {
		return Parsed{}, fmt.Errorf("%s takes %s", c.Name(), argText(c.Args))
	}
	for i, v := range p.Args {
		// Argv puts the arguments before the flags without a "--", so one
		// that looks like a flag would be read as one.
		if strings.HasPrefix(v, "-") {
			return Parsed{}, fmt.Errorf("%s cannot start with -: %q", c.Args[i].Name, v)
		}
		if c.Args[i].Path && !filepath.IsAbs(v) {
			return Parsed{}, fmt.Errorf("%s must be an absolute path, not %q", c.Args[i].Name, v)
		}
	}
	if len(p.Flags) < c.MinFlags {
		return Parsed{}, fmt.Errorf("%s needs at least one flag", c.Name())
	}
	return p, nil
}

func argText(args []Arg) string {
	if len(args) == 0 {
		return "no arguments"
	}
	var names []string
	for _, a := range args {
		if a.Optional {
			names = append(names, "["+a.Name+"]")
		} else {
			names = append(names, a.Name)
		}
	}
	return strings.Join(names, " ")
}

// Argv is the command line in the form the CLI parses: the command words, the
// positional arguments, then every flag as --name=value.
func (p Parsed) Argv() []string {
	out := append(append([]string{}, p.Command.Path...), p.Args...)
	for _, f := range p.Flags {
		out = append(out, "--"+f[0]+"="+f[1])
	}
	return out
}

// String is the command line as a person would type it, each word quoted for
// a POSIX shell where it needs to be.
func (p Parsed) String() string {
	words := []string{"dispatchq"}
	for _, w := range p.Argv() {
		words = append(words, QuotePOSIX(w))
	}
	return strings.Join(words, " ")
}

// PowerShell is the command line as typed in PowerShell 7.3 or later, each
// word quoted that PowerShell would otherwise read differently. It is shown,
// never run: Windows PowerShell 5.1 passes a " inside a word to a native
// program differently, which only a card's reader typing it there would meet.
func (p Parsed) PowerShell() string {
	words := []string{"dispatchq"}
	for _, w := range p.Argv() {
		// PowerShell may drop a bare -- before a native command sees it.
		if w == "" || w == "--" || strings.IndexFunc(w, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_./=:", r)
		}) >= 0 {
			w = QuotePowerShell(w)
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

// QuotePOSIX quotes s for a POSIX shell, leaving plain words alone.
func QuotePOSIX(s string) string {
	plain := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./=:@%+,", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !plain(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// QuotePowerShell quotes s as a PowerShell single-quoted string. PowerShell
// also closes such a string on the typographic single quotes, so they are
// doubled the same way.
func QuotePowerShell(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		if strings.ContainsRune("'\u2018\u2019\u201a\u201b", r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// Redact replaces the value of every credential flag in argv, so a command
// that was refused for carrying one can still be shown.
func Redact(argv []string) []string {
	c, rest, ok := Lookup(argv)
	if !ok {
		return argv
	}
	out := append([]string{}, c.Path...)
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f, isFlag := c.Flag(name)
		if !strings.HasPrefix(a, "-") || !isFlag || !f.Secret {
			out = append(out, a)
			continue
		}
		if hasValue {
			out = append(out, "--"+name+"=***")
			continue
		}
		out = append(out, a)
		if i+1 < len(rest) {
			out = append(out, "***")
			i++
		}
	}
	return out
}
