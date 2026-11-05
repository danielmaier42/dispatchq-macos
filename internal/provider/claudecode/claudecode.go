// Package claudecode is the provider adapter for the Claude Code CLI. It owns
// everything dispatchq knows about that particular harness: its command-line
// flags, how a session is started and resumed, how its configuration directory
// is selected, and how its stream-json output maps onto dispatchq's normalized
// run events. Nothing outside this package needs to know any of it.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// BinaryName is the CLI's command name, used when no path is configured and
// detection found nothing — the exec lookup may still succeed at run time.
const BinaryName = "claude"

// ConfigDirEnv is the environment variable Claude Code reads its configuration
// (and therefore its account and sessions) from. An instance with its own
// configuration directory is how a second Claude subscription stays separate.
const ConfigDirEnv = "CLAUDE_CONFIG_DIR"

// Adapter runs jobs through the Claude Code CLI.
type Adapter struct {
	// detect locates the CLI; swapped in tests.
	detect func() string

	mu       sync.Mutex
	probed   bool   // DetectBinary has run (see DetectBinary)
	detected string // what it found; "" means the CLI was not located

	// catalog holds each instance's model list: a binary's own help text does
	// not change while the daemon runs, but two instances can be two binaries.
	catalog provider.Catalog

	// keychain, http, usageEndpoint and clock stand in for the keychain, the
	// network and time when reading the plan limits (see limits.go); nil and
	// empty mean the real ones.
	keychain      func(ctx context.Context, service string) ([]byte, error)
	http          *http.Client
	usageEndpoint string
	clock         func() time.Time
}

// New returns the Claude Code adapter.
func New() *Adapter { return &Adapter{detect: DetectBinary} }

// Kind implements provider.Adapter.
func (a *Adapter) Kind() provider.Kind { return provider.KindClaudeCode }

// Describe implements provider.Adapter.
func (a *Adapter) Describe() provider.Description {
	return provider.Description{Name: "Claude", DefaultConfigDir: "~/.claude"}
}

// Capabilities implements provider.Adapter. Claude Code has no flag that
// enforces a read-only or workspace-only sandbox, so those modes are not
// claimed: dispatchq must not promise a restriction the harness cannot deliver.
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SessionResume:     true,
		InteractiveResume: true,
		StructuredOutput:  true,
		ModelDiscovery:    true,
		ReasoningEffort:   true,
		UsageMetrics:      true,
		CostMetrics:       true,
		RateLimitResume:   true,
		Subagents:         true,
		Asides:            true,
		AccessModes: []provider.AccessMode{
			provider.AccessProviderDefault,
			provider.AccessFullAccess,
		},
	}
}

// DetectBinary locates the CLI, probing at most once per process. The daemon's
// launchd PATH forces a login-shell probe (see detect.go) that sources the
// user's profile, which is far too expensive — and too unpredictable — to run
// before a job, so the answer is kept even when it found nothing. That is how
// dispatchq has always resolved the binary: once, at daemon startup. An operator
// who installs the CLI afterwards points Settings at it, which skips detection
// altogether.
func (a *Adapter) DetectBinary() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.probed {
		a.detected, a.probed = a.detect(), true
	}
	return a.detected
}

// Command implements provider.Adapter. The argument order is the CLI behaviour
// verified in PLAN.md §8: the flags first, then the appended system prompt, and
// the task's prompt as the final positional argument.
func (a *Adapter) Command(inst provider.Instance, req provider.Request) (provider.Command, error) {
	if req.SessionID == "" {
		return provider.Command{}, fmt.Errorf("claude-code: a run needs a session id")
	}
	access := req.AccessMode.OrDefault()
	if !a.Capabilities().SupportsAccess(access) {
		return provider.Command{}, provider.UnsupportedAccessError(inst, access)
	}

	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.ReasoningEffort != "" {
		args = append(args, "--effort", req.ReasoningEffort)
	}
	if access == provider.AccessFullAccess {
		args = append(args, "--dangerously-skip-permissions")
	}
	if req.Resume {
		args = append(args, "--resume", req.SessionID)
	} else {
		args = append(args, "--session-id", req.SessionID)
	}
	// Claude Code accepts --append-system-prompt once, so dispatchq's guidance
	// arrives as a single value that adds to — never replaces — the CLI's own
	// system prompt.
	args = append(args, "--append-system-prompt", req.SystemPrompt)
	args = append(args, req.Prompt)

	return provider.Command{Path: a.binary(inst), Args: args, Env: a.env(inst)}, nil
}

// binary resolves the executable for a run: whatever ResolveBinary found, and
// the bare name when it found nothing, so an exec-time PATH lookup still gets a
// chance rather than the run failing before it starts.
func (a *Adapter) binary(inst provider.Instance) string {
	if p := a.ResolveBinary(inst); p != "" {
		return p
	}
	return BinaryName
}

// NewParser implements provider.Adapter.
func (a *Adapter) NewParser() provider.Parser { return &parser{} }

// parser translates Claude Code's stream-json output into normalized events.
// It is per-run because the CLI reports the rate limit and the allowance
// window's reset time on separate lines, in either order: an approaching-limit
// event can precede the rejection, or a rejection can be followed by the window
// details. The parser therefore remembers the timing it has seen and, once a
// rate limit is known, re-reports it whenever a later line refines it.
type parser struct {
	retryAfter time.Duration
	resetAt    time.Time
	sawPause   bool // a pause of some kind was reported, so refined timing is news
	sawLimit   bool // the CLI reported a limit of its own, so its timing is about one
	// blockDetail names an account block — an organisation that disallows the
	// subscription, or a credit balance that is used up — once one was seen.
	blockDetail string
}

// The CLI's own names for the organisation block: the account is logged in, but
// its organisation does not allow a Claude subscription to drive Claude Code, so
// every request is refused (as a 401 or a 403) before any work happens. The
// error field carries the first name, the result envelope the second.
const (
	orgBlockedError = "oauth_org_not_allowed"
	orgBlockedCode  = "oauth_not_allowed_for_organization"
)

// The CLI's signs of a used-up API credit balance: an account running on an API
// key that has no prepaid credit left is refused with a 400 before any work
// happens. The assistant line carries the error name; the result envelope only
// the API's own message, as a successful subtype with is_error set.
const (
	creditExhaustedError = "billing_error"
	creditExhaustedText  = "credit balance is too low"
)

// AccountBlockedRetryAfter is how long an account block pauses the instance:
// an organisation block or a used-up credit balance. Neither refusal names a
// window of its own, and both lift when an admin or a billing change lifts them
// — never on their own in the next few minutes — so the usual blind backoff
// would only burn a run every quarter of an hour.
const AccountBlockedRetryAfter = time.Hour

// orgBlockedDetail is what the run says happened, in place of the generic rate
// limit message: the account is not out of allowance, it has none.
const orgBlockedDetail = "this organisation has disabled Claude subscription access for Claude Code; " +
	"the provider is paused like a rate limit until an admin enables it"

// creditExhaustedDetail is what the run says happened when the API credit
// balance is used up: no allowance window will reset it, only adding credit.
const creditExhaustedDetail = "the API credit balance of this account is too low; " +
	"the provider is paused like a rate limit until credit is added"

// streamEvent covers the fields dispatchq reads from the CLI's stream-json lines:
// the opening `init` system event, the final `result` envelope, intermediate
// `api_retry` system events, and `rate_limit_event`. Unknown fields are ignored.
type streamEvent struct {
	Type            string         `json:"type"`
	Subtype         string         `json:"subtype"`
	Model           string         `json:"model"`
	ParentToolUseID string         `json:"parent_tool_use_id"`
	AgentID         string         `json:"agent_id"`
	IsError         bool           `json:"is_error"`
	APIErrorStatus  *int           `json:"api_error_status"`
	APIErrorCode    string         `json:"api_error_code"`
	ErrorStatus     *int           `json:"error_status"`
	Error           string         `json:"error"`
	RetryDelayMS    *int           `json:"retry_delay_ms"`
	SessionID       string         `json:"session_id"`
	ResultText      string         `json:"result"`
	TotalCostUSD    float64        `json:"total_cost_usd"`
	NumTurns        int            `json:"num_turns"`
	DurationMS      int64          `json:"duration_ms"`
	Usage           *usageTokens   `json:"usage"`
	RateLimitInfo   *rateLimitInfo `json:"rate_limit_info"`
}

// rateLimitInfo is the payload of a `rate_limit_event`. ResetsAt is the
// absolute reset time as unix seconds; Status is "rejected" when the request
// was actually blocked, as opposed to a mere approaching-limit warning.
type rateLimitInfo struct {
	Status   string `json:"status"`
	ResetsAt int64  `json:"resetsAt"`
}

type usageTokens struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Parse implements provider.Parser. A single line can carry several signals —
// a final result that is itself a 429, for instance — so it returns a slice.
func (p *parser) Parse(line []byte) []provider.Event {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil // non-JSON or partial line: nothing to classify
	}

	// Remember rate-limit timing wherever it appears, including on an
	// approaching-limit event that does not itself reject the request: when a
	// later event does, this is the window it is waiting for.
	newTiming := false
	if ev.RetryDelayMS != nil && *ev.RetryDelayMS > 0 {
		if d := time.Duration(*ev.RetryDelayMS) * time.Millisecond; d != p.retryAfter {
			p.retryAfter, newTiming = d, true
		}
	}
	if info := ev.RateLimitInfo; info != nil && info.ResetsAt > 0 {
		if at := time.Unix(info.ResetsAt, 0); !at.Equal(p.resetAt) {
			p.resetAt, newTiming = at, true
		}
	}
	limited := p.isRateLimited(ev)
	if limited {
		p.sawLimit = true
	}
	if detail := p.accountBlock(ev); detail != "" {
		limited = true
		if p.blockDetail == "" {
			p.blockDetail = detail
		}
	}
	if limited {
		p.sawPause = true
	}

	var out []provider.Event
	if ev.SessionID != "" {
		started := provider.Event{Type: provider.EventSessionStarted, SessionID: ev.SessionID}
		// The run's own init names the model it resolved, alias or default
		// alike; a subagent's init names the subagent's.
		if ev.Type == "system" && ev.Subtype == "init" && ev.ParentToolUseID == "" && ev.AgentID == "" {
			started.Model = ev.Model
		}
		out = append(out, started)
	}
	if p.isAuthFailure(ev) {
		out = append(out, provider.Event{Type: provider.EventAuthFailed})
	}
	// Report the rate limit when this line signals one, and again when a later
	// line names the window of a limit already reported — otherwise a reset time
	// that arrives after the rejection would be lost and the engine would fall
	// back to a blind backoff.
	if limited || (newTiming && p.sawPause) {
		pause := provider.Event{
			Type:       provider.EventRateLimited,
			RetryAfter: p.retryAfter,
			ResetAt:    p.resetAt,
		}
		if p.blockDetail != "" {
			pause.Detail = p.blockDetail
			// The block names no window of its own, and the timing the CLI
			// mentions describes the allowance window — the right thing to wait
			// for only when it actually reported a limit. Otherwise the fixed
			// hour stands, so a one-second backoff from some transient API error
			// cannot turn this pause into a retry loop.
			if !p.sawLimit {
				pause.RetryAfter, pause.ResetAt = AccountBlockedRetryAfter, time.Time{}
			}
		}
		out = append(out, pause)
	}
	if ev.Type == "result" && ev.ParentToolUseID == "" && ev.AgentID == "" {
		m := &provider.Metrics{CostUSD: ev.TotalCostUSD, NumTurns: ev.NumTurns, DurationMS: ev.DurationMS}
		if ev.Usage != nil {
			m.InputTokens = ev.Usage.InputTokens
			m.OutputTokens = ev.Usage.OutputTokens
		}
		out = append(out, provider.Event{
			Type:        provider.EventCompleted,
			IsError:     ev.IsError,
			FinalOutput: ev.ResultText,
			Metrics:     m,
		})
	}
	return out
}

func (p *parser) isRateLimited(ev streamEvent) bool {
	if ev.Error == "rate_limit" {
		return true
	}
	if statusIs(ev.APIErrorStatus, 429) || statusIs(ev.ErrorStatus, 429) {
		return true
	}
	// A rejected request means the allowance is actually exhausted; a session
	// limit surfaces this way even when no 429 or error field follows.
	return ev.RateLimitInfo != nil && ev.RateLimitInfo.Status == "rejected"
}

// isOrgBlocked reports the organisation block. It is deliberately not an
// authentication failure: nobody can log in differently to fix it, and failing
// the task would waste the night on a queue whose account simply has no
// allowance to spend. Treating it as a limit is what pauses this provider and
// hands its work to the fallback, which is exactly the right answer.
func (p *parser) isOrgBlocked(ev streamEvent) bool {
	return ev.APIErrorCode == orgBlockedCode || ev.Error == orgBlockedError
}

// isCreditExhausted reports a used-up API credit balance. Like the organisation
// block it is a limit rather than a failure: the task is fine, the account has
// nothing to spend, so the provider pauses and its fallback takes the work. The
// message is only read from an error result, so a run that merely quotes it in
// its successful answer is not mistaken for one.
func (p *parser) isCreditExhausted(ev streamEvent) bool {
	if ev.Error == creditExhaustedError {
		return true
	}
	return ev.Type == "result" && ev.IsError &&
		strings.Contains(strings.ToLower(ev.ResultText), creditExhaustedText)
}

// accountBlock names the account block this line reports, or "" for none.
func (p *parser) accountBlock(ev streamEvent) string {
	switch {
	case p.isOrgBlocked(ev):
		return orgBlockedDetail
	case p.isCreditExhausted(ev):
		return creditExhaustedDetail
	}
	return ""
}

func (p *parser) isAuthFailure(ev streamEvent) bool {
	// The organisation block arrives as a 401 or a 403, depending on how the CLI
	// got the refusal, and it is not a login problem: the credentials are fine,
	// the organisation is not. Reading it as one would outrank the pause in the
	// collector — retiring the task, skipping the fallback, and telling the
	// operator to log in again for something no login can fix.
	if p.isOrgBlocked(ev) {
		return false
	}
	if ev.Error == "authentication_failed" {
		return true
	}
	return statusIs(ev.APIErrorStatus, 401) || statusIs(ev.ErrorStatus, 401)
}

func statusIs(p *int, want int) bool { return p != nil && *p == want }
