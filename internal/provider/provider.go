// Package provider is dispatchq's harness abstraction: the scheduler and the
// store talk about *provider instances* and *capabilities*, never about a
// particular CLI. Everything a single harness does differently — how its
// process is invoked, how its output is read, how its configuration directory
// is passed — lives in an [Adapter] behind this contract.
//
// The pieces fit together like this:
//
//	Kind        an adapter implementation for one CLI harness ("claude-code")
//	Instance    a configured adapter instance, chosen by its stable ID ("claude")
//	Registry    maps a Kind to the Adapter that implements it
//	Set         the configured instances plus which one is the default
//	Adapter     builds the [Command] for a run and parses its output into [Event]s
//	Collector   folds those events into one normalized [Result]
//
// Instance ID and Kind are deliberately separate: two Claude subscriptions are
// two instances of the same kind, each with its own configuration directory,
// sessions and rate-limit state.
package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

// Kind identifies an adapter implementation for one CLI harness. It is the
// registry key; a configured [Instance] names the kind it uses.
type Kind string

// KindClaudeCode is the adapter for the Claude Code CLI. It is the kind the
// store seeds a fresh or migrated configuration with, so both sides name the
// same string by construction.
const KindClaudeCode Kind = store.DefaultProviderKind

// KindCodex is the adapter for the Codex CLI.
const KindCodex Kind = "codex"

// KindOpencode is the adapter for the opencode CLI.
const KindOpencode Kind = "opencode"

// Instance is a configured adapter instance — the thing a task selects by ID.
// It is the domain form of [store.Provider]: the store owns how it is written
// to config.toml, this package owns what it means.
type Instance struct {
	// ID is the stable identifier a task selects ("claude").
	ID string `json:"id"`
	// Kind names the adapter that runs this instance.
	Kind Kind `json:"kind"`
	// Name is the human-readable label shown in the UI and in run messages.
	Name string `json:"name"`
	// BinaryPath is an absolute path to the harness CLI. Empty lets the adapter
	// detect it.
	BinaryPath string `json:"binary_path"`
	// ConfigDir selects the harness's configuration/account directory, which is
	// how a second subscription of the same kind stays separate. Empty leaves
	// the choice to the CLI and the daemon's environment, which is what a
	// single-account setup wants; an instance that must be isolated names one.
	ConfigDir string `json:"config_dir"`
	// Env is the environment variables the harness is started with, added to
	// the daemon's own and winning over them. It is how an instance takes an
	// API key (ANTHROPIC_API_KEY) instead of a login. The values are secrets and
	// are never serialised.
	Env map[string]string `json:"-"`
	// DefaultModel is used when neither the task nor the caller names a model.
	DefaultModel string `json:"default_model"`
	// FallbackProvider is the id of the instance that takes this one's tasks
	// while its allowance is used up: a rate limit belongs to an account, so
	// naming a second one here is what keeps the queue moving through the night
	// instead of waiting for the window to reopen. Empty means its tasks wait.
	FallbackProvider string `json:"fallback_provider"`
	// FallbackModel is the model the substitute runs those tasks with. Empty
	// leaves the choice to [Set.ResolveAvailable], which keeps the model between
	// two accounts of the same harness and takes the substitute's own default
	// otherwise. It means nothing without a FallbackProvider.
	FallbackModel string `json:"fallback_model"`
	// Enabled turns the instance off without removing it.
	Enabled bool `json:"enabled"`
	// Execution is the environment the instance runs in. The zero value is the
	// daemon's own operating system (see [Execution]).
	Execution Execution `json:"execution"`
}

// InstanceOf reads a stored provider entry as an instance.
func InstanceOf(p store.Provider) Instance {
	return Instance{
		ID:               p.ID,
		Kind:             Kind(p.Kind),
		Name:             p.Name,
		BinaryPath:       p.BinaryPath,
		ConfigDir:        p.ConfigDir,
		Env:              maps.Clone(p.Env),
		DefaultModel:     p.DefaultModel,
		FallbackProvider: p.FallbackProvider,
		FallbackModel:    p.FallbackModel,
		Enabled:          p.Enabled,
		Execution:        executionOf(p.Execution),
	}
}

// Stored returns the instance in the shape config.toml keeps.
func (i Instance) Stored() store.Provider {
	return store.Provider{
		ID:               i.ID,
		Kind:             string(i.Kind),
		Name:             i.Name,
		BinaryPath:       i.BinaryPath,
		ConfigDir:        i.ConfigDir,
		Env:              maps.Clone(i.Env),
		DefaultModel:     i.DefaultModel,
		FallbackProvider: i.FallbackProvider,
		FallbackModel:    i.FallbackModel,
		Enabled:          i.Enabled,
		Execution:        i.Execution.stored(),
	}
}

// Label is how an instance is named in text the operator reads: its display
// name when it has one, its id otherwise.
func (i Instance) Label() string {
	if i.Name != "" {
		return i.Name
	}
	return i.ID
}

// AccessMode is dispatchq's provider-neutral expression of how much authority a
// run gets. Each adapter validates and maps only the modes it can actually
// enforce, so the application never promises a restriction a harness cannot
// deliver.
type AccessMode string

const (
	// AccessProviderDefault leaves the harness's own permission handling alone.
	AccessProviderDefault AccessMode = "provider-default"
	// AccessReadOnly forbids any modification of the working tree.
	AccessReadOnly AccessMode = "read-only"
	// AccessWorkspaceWrite allows writes inside the working directory only.
	AccessWorkspaceWrite AccessMode = "workspace-write"
	// AccessFullAccess bypasses the harness's permission prompts entirely.
	AccessFullAccess AccessMode = "full-access"
)

// OrDefault resolves the zero value to AccessProviderDefault. An unset access
// mode must never be read as "anything goes": a request that says nothing about
// authority gets the harness's own permission handling.
func (m AccessMode) OrDefault() AccessMode {
	if m == "" {
		return AccessProviderDefault
	}
	return m
}

// Capabilities describe what an adapter kind can do, so callers can ask about a
// capability instead of branching on a provider name.
type Capabilities struct {
	// SessionResume: the harness can continue a previous session non-interactively.
	SessionResume bool
	// InteractiveResume: the harness can reopen a session in a terminal.
	InteractiveResume bool
	// StructuredOutput: the harness emits machine-readable run output.
	StructuredOutput bool
	// ModelDiscovery: the harness can list the models it accepts.
	ModelDiscovery bool
	// ReasoningEffort: the harness accepts a reasoning-effort setting.
	ReasoningEffort bool
	// UsageMetrics: the harness reports token and turn counts.
	UsageMetrics bool
	// CostMetrics: the harness reports a monetary cost.
	CostMetrics bool
	// RateLimitResume: a rate-limited session can be picked up again later.
	RateLimitResume bool
	// Subagents: the harness runs its own subagents inside one job.
	Subagents bool
	// Asides: the harness can answer dispatchq's own questions — tool-free, in a
	// session of its own, returning the structure dispatchq asked for. Prompt
	// review needs one turn of this; the feedback assistant needs several.
	Asides bool
	// AccessModes lists the access modes this adapter can actually enforce.
	AccessModes []AccessMode
}

// SupportsAccess reports whether the adapter can enforce mode.
func (c Capabilities) SupportsAccess(mode AccessMode) bool {
	for _, m := range c.AccessModes {
		if m == mode {
			return true
		}
	}
	return false
}

// Request is one run, expressed without reference to any particular CLI. The
// adapter turns it into a [Command].
type Request struct {
	// Prompt is the instruction for the harness.
	Prompt string
	// WorkingDir is the directory the harness runs in.
	WorkingDir string
	// Model is the already-resolved effective model. Empty means the harness's
	// own default.
	Model string
	// SessionID identifies the session to start or continue.
	SessionID string
	// Resume continues SessionID instead of starting a new session.
	Resume bool
	// AccessMode is the requested authority for this run. The zero value means
	// AccessProviderDefault (see AccessMode.OrDefault).
	AccessMode AccessMode
	// SystemPrompt is dispatchq's own run guidance, to be delivered alongside the
	// harness's built-in instructions rather than replacing them.
	SystemPrompt string
	// ReasoningEffort is how hard the model should think, for the harnesses that
	// take such a setting. Empty leaves it to the harness. An adapter whose
	// Capabilities do not claim ReasoningEffort ignores it.
	ReasoningEffort string
	// Host is the environment the run executes in, for an adapter that reads
	// the instance's own files while building the command. Nil is the daemon's
	// own operating system.
	Host Host
}

// Command is the process invocation an adapter produced for a [Request].
type Command struct {
	// Path is the resolved executable, invoked directly — never through a shell.
	Path string
	// Args are the arguments after the executable.
	Args []string
	// Env holds the adapter's own environment additions (for example a
	// configuration-directory variable). They are appended to the daemon's
	// environment, so a later entry wins.
	Env []string
	// Stdin is written to the process's standard input, which is how a harness
	// that takes its prompt there receives it. Empty attaches nothing, and the
	// process sees an immediately closed stdin.
	Stdin string
}

// Description is what a harness is called and where it keeps its configuration.
// It lets the app name and explain a provider without knowing which kind it is
// — the same reason capabilities exist.
type Description struct {
	// Name is the harness's product name, as a person would say it ("Claude",
	// "Codex"), rather than the adapter kind ("claude-code").
	Name string `json:"name"`
	// DefaultConfigDir is where the CLI keeps its configuration when nothing
	// points it elsewhere, written the way a person would ("~/.claude"). It is
	// shown as the placeholder of the field that overrides it, so an empty field
	// says what it falls back to.
	DefaultConfigDir string `json:"default_config_dir"`
}

// Model is a model an adapter offers as a suggestion. The id is what a task
// stores; the label is what the app shows. A catalog is never validation: a
// model a task names but this list does not is passed to the harness anyway.
type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// EventType is a normalized application event. Adapters translate their
// harness's output into these, so the executor never reads provider syntax.
type EventType string

const (
	// EventSessionStarted reports the session ID the harness is using.
	EventSessionStarted EventType = "session_started"
	// EventCompleted is the harness's terminal result for the run.
	EventCompleted EventType = "completed"
	// EventRateLimited reports that the provider refused the work for allowance
	// reasons; the run can be resumed once the window reopens.
	EventRateLimited EventType = "rate_limited"
	// EventAuthFailed reports a login/authentication problem.
	EventAuthFailed EventType = "authentication_failed"
	// EventModelRejected reports that the provider does not accept the model.
	EventModelRejected EventType = "model_rejected"
	// EventFailed reports a harness-level failure with a reason.
	EventFailed EventType = "failed"
)

// Event is one normalized observation from a run's output.
type Event struct {
	// Type selects which of the fields below carry meaning.
	Type EventType
	// SessionID is set on EventSessionStarted.
	SessionID string
	// Model is the model the harness says it runs, set on EventSessionStarted
	// by a harness that names it. It resolves an alias or an unset model to the
	// one that actually answered.
	Model string
	// FinalOutput is the harness's final response, set on EventCompleted.
	FinalOutput string
	// IsError marks an EventCompleted that reports a failure.
	IsError bool
	// Detail is a short human-readable reason, for the failure-ish events.
	Detail string
	// RetryAfter is a relative wait reported with EventRateLimited.
	RetryAfter time.Duration
	// ResetAt is the absolute time the allowance window reopens, reported with
	// EventRateLimited.
	ResetAt time.Time
	// Block names an account block behind an EventRateLimited, when the pause
	// is one rather than a used-up allowance window.
	Block Block
	// Metrics are the run's usage figures, reported with EventCompleted.
	Metrics *Metrics
}

// Block names why an account refuses all work although no allowance window is
// used up. Such a block pauses the provider like a limit, but it lifts only
// through an admin or a billing change, so the operator needs to know which.
type Block string

const (
	// BlockOrgDisallowed is an organisation that does not allow its Claude
	// subscription to drive Claude Code.
	BlockOrgDisallowed Block = "org_disallowed"
	// BlockCreditExhausted is an API key whose prepaid credit balance is used
	// up: the API refused a run with "Credit balance is too low".
	BlockCreditExhausted Block = "credit_exhausted"
	// BlockBillingRefused is an API key the API refused with a billing error
	// that did not say why: the credit may be used up, or a spend limit set on
	// the account reached.
	BlockBillingRefused Block = "billing_refused"
)

// Metrics are the usage figures a harness reports for a run. Values it does not
// report stay zero — dispatchq never infers a missing cost from a price table.
type Metrics struct {
	CostUSD      float64
	InputTokens  int
	OutputTokens int
	NumTurns     int
	DurationMS   int64
}

// Result is the normalized outcome of a run.
type Result struct {
	// Status is the classified outcome.
	Status store.RunStatus
	// SessionID is the session the run actually used.
	SessionID string
	// Model is the model the harness reported running; empty when it named none.
	Model string
	// ExitCode is the harness process's exit status.
	ExitCode int
	// RetryAfter is how long to wait after a rate limit, when reported.
	RetryAfter time.Duration
	// ResetAt is the absolute rate-limit reset time, when reported.
	ResetAt time.Time
	// Block names the account block behind a rate-limited outcome, empty for an
	// ordinary allowance limit.
	Block Block
	// Message is a short human-readable detail for a non-success outcome.
	Message string
	// FinalOutput is the harness's final response — the answer, as opposed to
	// the raw run log.
	FinalOutput string
	// Metrics are the reported usage figures, nil when the harness reported none.
	Metrics *Metrics
}

// Parser reads one run's output. Adapters return a fresh parser per run, so a
// parser may keep run-local state (a rate-limit reset time seen earlier, say).
type Parser interface {
	// Parse translates one line of harness output into zero or more events.
	Parse(line []byte) []Event
}

// Adapter implements one CLI harness. Everything provider-specific —
// arguments, environment, output syntax — lives behind this interface.
type Adapter interface {
	// Kind is the registry key this adapter implements.
	Kind() Kind
	// Capabilities describes what the harness can do.
	Capabilities() Capabilities
	// Describe reports what the harness is called and where it keeps its
	// configuration by default.
	Describe() Description
	// DetectBinary locates the harness CLI, returning "" when it cannot be found.
	DetectBinary() string
	// ResolveBinary reports the executable this instance would run, or "" when
	// the CLI cannot be located at all.
	ResolveBinary(inst Instance) string
	// CheckHealth reports whether the instance can run a job right now. It may
	// look at the instance's filesystem and ask the CLI cheap questions through h — its
	// version, whether it is logged in — but must never consume model usage.
	CheckHealth(ctx context.Context, inst Instance, h Host) Health
	// ListModels returns the models this instance suggests, best effort. It may
	// ask the CLI through h, must never consume model usage, and returns its own
	// fallback list rather than nothing when discovery fails — a provider whose
	// catalog cannot be read is still perfectly able to run.
	ListModels(ctx context.Context, inst Instance, h Host) []Model
	// Command builds the invocation for a run on the given instance.
	Command(inst Instance, req Request) (Command, error)
	// InteractiveResumeCommand builds the command that reopens a finished run's
	// session in a terminal. Adapters whose Capabilities do not claim
	// InteractiveResume return ErrUnsupported.
	InteractiveResumeCommand(inst Instance, req Request) (Command, error)
	// AsideCommand builds the invocation for one turn of an aside (see
	// aside.go). Adapters whose Capabilities do not claim Asides return
	// ErrUnsupported.
	AsideCommand(inst Instance, req AsideRequest) (Command, error)
	// ParseAside reads a harness's answer to an aside out of its output.
	ParseAside(out []byte) (Aside, error)
	// NewParser returns a parser for one run's output.
	NewParser() Parser
}

// ErrUnsupported is returned by an adapter asked for something its harness
// cannot do (an access mode it cannot enforce, a resume it does not support).
var ErrUnsupported = errors.New("not supported by this provider")

// UnsupportedAccessError reports an access mode an adapter cannot enforce.
func UnsupportedAccessError(inst Instance, mode AccessMode) error {
	return fmt.Errorf("%w: %s cannot enforce %q access", ErrUnsupported, inst.Label(), mode)
}
