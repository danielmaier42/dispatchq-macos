// The `dispatchq provider` command group: inspecting and configuring the harness
// instances tasks run on. It is the command-line half of Settings → Providers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

const providerUsage = `dispatchq provider - the harnesses tasks run on

Usage:
  dispatchq provider list [--json]
  dispatchq provider show ID [--json]
  dispatchq provider check ID [--json]   (probe it now, ignoring the cached verdict)
  dispatchq provider limits [ID] [--json] (how much of each allowance is used)
  dispatchq provider add  ID --kind KIND [--name N] [--path PATH]
                        [--config-dir PATH] [--default-model MODEL]
                        [--fallback ID] [--fallback-model MODEL]
                        [--env NAME=VALUE]...
                        [--wsl-distro DISTRO [--wsl-user USER]]   (Windows: run inside WSL)
  dispatchq provider edit ID [--name N] [--path PATH]
                        [--config-dir PATH] [--default-model MODEL]
                        [--fallback ID|none] [--fallback-model MODEL|none]
                        [--env NAME=VALUE]... [--unset-env NAME]...
                        (--env adds or replaces a variable the harness is started with,
                        an ANTHROPIC_API_KEY to run it on API billing; values are never
                        printed back, show lists the names only)
  dispatchq provider enable ID | dispatchq provider disable ID
  dispatchq provider default ID          (run tasks that name no provider on it)
  dispatchq provider rm ID`

// providerCheckTimeout bounds a whole `provider list` pass: every configured
// instance is probed, and a CLI that hangs must not hang the command.
const providerCheckTimeout = 60 * time.Second

func cmdProvider(st *store.Store, args []string) error {
	if len(args) == 0 {
		fmt.Println(providerUsage)
		return nil
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return cmdProviderList(st, rest)
	case "show":
		return cmdProviderShow(st, rest, false)
	case "check":
		return cmdProviderShow(st, rest, true)
	case "limits":
		return cmdProviderLimits(st, rest)
	case "add":
		return cmdProviderAdd(st, rest)
	case "edit":
		return cmdProviderEdit(st, rest)
	case "enable":
		return withProviderID(rest, "enabled", func(id string) error { return app.SetProviderEnabled(st, id, true) })
	case "disable":
		return withProviderID(rest, "disabled", func(id string) error { return app.SetProviderEnabled(st, id, false) })
	case "default":
		return withProviderID(rest, "is now the default provider;", func(id string) error { return app.SetDefaultProvider(st, id) })
	case "rm":
		return withProviderID(rest, "removed", func(id string) error { return app.RemoveProvider(st, id) })
	default:
		fmt.Println(providerUsage)
		return fmt.Errorf("unknown provider command %q", sub)
	}
}

// withProviderID runs a one-argument provider command and reports what it did,
// so the confirmation says which change landed rather than a generic "updated".
func withProviderID(args []string, done string, fn func(string) error) error {
	if len(args) != 1 {
		return fmt.Errorf("expected exactly one provider id")
	}
	if err := fn(args[0]); err != nil {
		return err
	}
	fmt.Printf("provider %q %s\n", args[0], done)
	return nil
}

// providerView is what `provider list --json` and `provider show --json` print:
// the configured instance, whether it is the default, and its readiness. A
// running job reads this to find out which providers can take work.
type providerView struct {
	provider.Instance
	// Env names the variables the instance is started with. Their values are
	// secrets and are never printed.
	Env     []string        `json:"env"`
	Default bool            `json:"default"`
	Health  provider.Health `json:"health"`
}

// checkProviders probes every instance in set. fresh is what `provider check`
// passes; the other commands run in a fresh process, where the cache is empty
// anyway, so it makes no difference to them.
func checkProviders(set provider.Set, only string, fresh bool) ([]providerView, error) {
	checker := provider.NewChecker(cliRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), providerCheckTimeout)
	defer cancel()

	var out []providerView
	for _, inst := range set.All() {
		if only != "" && inst.ID != only {
			continue
		}
		h := checker.CheckMaybeFresh(ctx, inst, fresh)
		out = append(out, providerView{Instance: inst, Env: inst.EnvNames(), Default: inst.ID == set.DefaultID(), Health: h})
	}
	if only != "" && len(out) == 0 {
		return nil, fmt.Errorf("%w %q", provider.ErrUnknownProvider, only)
	}
	return out, nil
}

func cmdProviderList(st *store.Store, args []string) error {
	fs := flag.NewFlagSet("provider list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the providers as JSON, readiness included")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set, err := app.Providers(st)
	if err != nil {
		return err
	}
	views, err := checkProviders(set, "", false)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(views)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "\tID\tKIND\tNAME\tDEFAULT MODEL\tFALLBACK\tSTATUS")
	for _, v := range views {
		mark := " "
		if v.Default {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			mark, v.ID, v.Kind, v.Name, orDefault(v.DefaultModel, "(provider default)"),
			orDefault(v.FallbackProvider, "-"), v.Health.State)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\n* the provider a task runs on when it names none")
	return nil
}

func cmdProviderShow(st *store.Store, args []string, fresh bool) error {
	id, rest, err := splitPositional(args, "a provider id")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("provider show", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the provider as JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	set, err := app.Providers(st)
	if err != nil {
		return err
	}
	views, err := checkProviders(set, id, fresh)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(views[0])
	}
	printProvider(views[0])
	return nil
}

func printProvider(v providerView) {
	fmt.Printf("id:             %s\n", v.ID)
	fmt.Printf("kind:           %s\n", v.Kind)
	fmt.Printf("name:           %s\n", v.Name)
	fmt.Printf("enabled:        %t\n", v.Enabled)
	fmt.Printf("default:        %t\n", v.Default)
	fmt.Printf("binary_path:    %s\n", orDefault(v.BinaryPath, "(auto-detect)"))
	fmt.Printf("config_dir:     %s\n", orDefault(v.ConfigDir, "(the CLI's own)"))
	fmt.Printf("env:            %s\n", orDefault(strings.Join(v.Env, ", "), "(none)"))
	fmt.Printf("default_model:  %s\n", orDefault(v.DefaultModel, "(the provider's own default)"))
	fmt.Printf("fallback:       %s\n", orDefault(v.FallbackProvider, "(none: its tasks wait for the limit)"))
	if v.FallbackProvider != "" {
		fmt.Printf("fallback_model: %s\n", orDefault(v.FallbackModel, "(chosen by dispatchq)"))
	}
	fmt.Printf("status:         %s\n", v.Health.State)
	if v.Health.Binary != "" {
		fmt.Printf("resolved:       %s\n", v.Health.Binary)
	}
	if v.Health.Detail != "" {
		fmt.Printf("detail:         %s\n", v.Health.Detail)
	}
	if v.Health.Reason != "" {
		fmt.Printf("reason:         %s\n", v.Health.Reason)
	}
}

// providerPatch is a parsed set of `provider add`/`provider edit` flags: the
// values plus which of them the caller passed, so an edit changes only the
// fields it names.
type providerPatch struct {
	set           map[string]bool
	kind          string
	name          string
	path          string
	configDir     string
	defaultModel  string
	fallback      string
	fallbackModel string
	env           repeatedFlag
	unsetEnv      repeatedFlag
	wslDistro     string
	wslUser       string
}

// repeatedFlag collects every occurrence of a flag that may be given more than
// once.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func (p *providerPatch) register(fs *flag.FlagSet, withKind bool) {
	if withKind {
		fs.StringVar(&p.kind, "kind", "", "adapter kind the instance runs on (required)")
		// Where the instance runs is chosen once, when it is created.
		fs.StringVar(&p.wslDistro, "wsl-distro", "", "DispatchQ for Windows: run the CLI inside this WSL distribution instead of Windows (paths are then Linux paths)")
		fs.StringVar(&p.wslUser, "wsl-user", "", "with --wsl-distro: the Linux user to run as (empty = the distribution's default user)")
	}
	fs.StringVar(&p.name, "name", "", "display name (defaults to the id)")
	fs.StringVar(&p.path, "path", "", "absolute path to the harness CLI (empty = auto-detect)")
	fs.StringVar(&p.configDir, "config-dir", "", "the CLI's configuration directory, which selects the account (empty = its own default)")
	fs.StringVar(&p.defaultModel, "default-model", "", "model used when neither the task nor the caller names one")
	fs.StringVar(&p.fallback, "fallback", "", "provider that takes this one's tasks while its limit is reached (\"none\" clears it)")
	fs.StringVar(&p.fallbackModel, "fallback-model", "", "model those substituted runs use (\"none\" lets dispatchq choose)")
	fs.Var(&p.env, "env", "NAME=VALUE environment variable the harness is started with (repeatable), e.g. ANTHROPIC_API_KEY=...")
	if !withKind {
		fs.Var(&p.unsetEnv, "unset-env", "NAME of an environment variable to remove (repeatable)")
	}
}

func (p providerPatch) apply(inst *provider.Instance) error {
	if p.set["name"] {
		inst.Name = p.name
	}
	if p.set["path"] {
		inst.BinaryPath = p.path
	}
	if p.set["config-dir"] {
		inst.ConfigDir = p.configDir
	}
	if p.set["default-model"] {
		inst.DefaultModel = p.defaultModel
	}
	if p.set["fallback"] {
		// "none" is how a flag clears a value that an empty string cannot: an
		// empty --fallback would be indistinguishable from not passing it at all
		// in a shell script that builds the arguments up.
		inst.FallbackProvider = clearable(p.fallback)
	}
	if p.set["fallback-model"] {
		inst.FallbackModel = clearable(p.fallbackModel)
	}
	return p.applyEnv(inst)
}

// applyEnv adds or replaces the --env variables and drops the --unset-env ones.
// The map is rebuilt rather than edited so an instance read from the store is
// never changed through a shared map.
func (p providerPatch) applyEnv(inst *provider.Instance) error {
	if len(p.env) == 0 && len(p.unsetEnv) == 0 {
		return nil
	}
	env := maps.Clone(inst.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, kv := range p.env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			return errors.New("--env: expected NAME=VALUE")
		}
		env[name] = value
	}
	for _, name := range p.unsetEnv {
		if _, ok := env[name]; !ok {
			return fmt.Errorf("--unset-env %q: the provider has no such variable", name)
		}
		delete(env, name)
	}
	if len(env) == 0 {
		env = nil
	}
	inst.Env = env
	return nil
}

// clearable reads the literal "none" as "unset this", so a flag can take a
// value away again.
func clearable(v string) string {
	if v == "none" {
		return ""
	}
	return v
}

func cmdProviderAdd(st *store.Store, args []string) error {
	id, rest, err := splitPositional(args, "a provider id")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("provider add", flag.ContinueOnError)
	var p providerPatch
	p.register(fs, true)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if p.set, err = passedFlags(fs); err != nil {
		return err
	}
	if p.kind == "" {
		return fmt.Errorf("--kind is required")
	}
	inst := provider.Instance{ID: id, Kind: provider.Kind(p.kind), Name: id, Enabled: true}
	switch {
	case p.wslDistro != "":
		inst.Execution = provider.Execution{Type: provider.ExecWSL, Distro: p.wslDistro, User: p.wslUser}
	case p.wslUser != "":
		return fmt.Errorf("--wsl-user needs --wsl-distro")
	}
	if err := p.apply(&inst); err != nil {
		return err
	}
	if inst.Name == "" {
		inst.Name = id
	}
	if err := app.AddProvider(st, cliRegistry(), inst); err != nil {
		return err
	}
	fmt.Printf("added provider %q\n", id)
	return nil
}

func cmdProviderEdit(st *store.Store, args []string) error {
	id, rest, err := splitPositional(args, "a provider id")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("provider edit", flag.ContinueOnError)
	var p providerPatch
	p.register(fs, false)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if p.set, err = passedFlags(fs); err != nil {
		return err
	}
	if len(p.set) == 0 {
		return fmt.Errorf("no changes given")
	}
	if err := app.EditProvider(st, cliRegistry(), id, func(inst *provider.Instance) error {
		return p.apply(inst)
	}); err != nil {
		return err
	}
	fmt.Printf("updated provider %q\n", id)
	return nil
}
