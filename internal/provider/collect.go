package provider

import (
	"fmt"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

// Collector folds the normalized events of one run into a [Result]. The
// classification is provider-neutral: every adapter reports the same events, so
// the rules below decide the outcome for all of them.
type Collector struct {
	// name is how the provider instance is called in an operator-facing message.
	name string

	sessionID   string
	model       string
	finalOutput string
	metrics     *Metrics

	completed    bool
	completedErr bool
	rateLimited  bool
	authFailed   bool
	modelReject  bool
	failed       bool
	detail       string
	limitDetail  string // why the pause happened, when it was more than "limit reached"
	block        Block
	retryAfter   time.Duration
	resetAt      time.Time
}

// NewCollector starts collecting for one run. sessionID is the session dispatchq
// assigned up front, used until the harness reports one of its own; name is the
// provider instance's display name, which appears in the result message.
func NewCollector(sessionID, name string) *Collector {
	return &Collector{sessionID: sessionID, name: name}
}

// Add folds one event into the run's outcome.
func (c *Collector) Add(ev Event) {
	switch ev.Type {
	case EventSessionStarted:
		if ev.SessionID != "" {
			c.sessionID = ev.SessionID
		}
		// The first model named is the run's own; a later one would belong to
		// something the run started.
		if ev.Model != "" && c.model == "" {
			c.model = ev.Model
		}
	case EventCompleted:
		c.completed = true
		c.completedErr = ev.IsError
		c.finalOutput = ev.FinalOutput
		if ev.Metrics != nil {
			c.metrics = ev.Metrics
		}
	case EventRateLimited:
		c.rateLimited = true
		// A block arrives with the detail that names it, and a later line may
		// name it more precisely (a billing error, then the credit balance), so
		// the latest block wins. Other details keep the first reason given.
		if ev.Block != "" {
			c.block, c.limitDetail = ev.Block, ev.Detail
		} else if ev.Detail != "" && c.limitDetail == "" {
			c.limitDetail = ev.Detail
		}
		if ev.RetryAfter > 0 {
			c.retryAfter = ev.RetryAfter
		}
		if !ev.ResetAt.IsZero() {
			c.resetAt = ev.ResetAt
		}
	case EventAuthFailed:
		c.authFailed = true
		c.noteDetail(ev.Detail)
	case EventModelRejected:
		c.modelReject = true
		c.noteDetail(ev.Detail)
	case EventFailed:
		c.failed = true
		c.noteDetail(ev.Detail)
	}
}

// AddAll folds a batch of events, the shape a [Parser] returns.
func (c *Collector) AddAll(evs []Event) {
	for _, ev := range evs {
		c.Add(ev)
	}
}

// noteDetail keeps the first reason given; later events elaborate on a failure
// that is already explained rather than replacing its cause.
func (c *Collector) noteDetail(detail string) {
	if detail != "" && c.detail == "" {
		c.detail = detail
	}
}

// SessionID returns the session the run is using — the harness's own once it
// reported one, otherwise the one dispatchq assigned.
func (c *Collector) SessionID() string { return c.sessionID }

// CompletedRateLimit reports a terminal, resumable limit result. A rejection
// alone may come from a subagent while the main run is still doing useful work.
func (c *Collector) CompletedRateLimit() bool {
	return c.completed && c.Result(0).Status == store.StatusRateLimited
}

// Result classifies the run now that the process has exited with exitCode.
//
// The order matters. An authentication problem outranks everything, because no
// other outcome is meaningful once the harness could not log in. A rate limit
// outranks a failed or missing result, because the run is resumable rather than
// broken — but not a result that actually succeeded, which happens when the
// harness merely reported an approaching limit.
func (c *Collector) Result(exitCode int) Result {
	res := Result{
		SessionID:   c.sessionID,
		Model:       c.model,
		ExitCode:    exitCode,
		RetryAfter:  c.retryAfter,
		ResetAt:     c.resetAt,
		FinalOutput: c.finalOutput,
		Metrics:     c.metrics,
	}
	switch {
	case c.authFailed:
		res.Status = store.StatusAuthError
		res.Message = c.detailOr(fmt.Sprintf("%s reported an authentication problem", c.label()))
	case c.modelReject:
		res.Status = store.StatusFailed
		res.Message = c.detailOr(fmt.Sprintf("%s rejected the selected model", c.label()))
	case c.rateLimited && (!c.completed || c.completedErr):
		res.Status = store.StatusRateLimited
		res.Block = c.block
		res.Message = c.limitDetailOr("rate limit hit; waiting for reset")
	case c.completed && !c.completedErr && exitCode == 0:
		res.Status = store.StatusSuccess
	case c.failed:
		res.Status = store.StatusFailed
		res.Message = c.detailOr(fmt.Sprintf("run failed (exit %d)", exitCode))
	case exitCode == -1:
		// Terminated by a signal (e.g. the daemon was stopped) before it could
		// finish. Not a task failure per se, but the run did not complete.
		res.Status = store.StatusFailed
		res.Message = "run was interrupted before completing (the process was terminated — e.g. the daemon stopped)"
	default:
		res.Status = store.StatusFailed
		res.Message = fmt.Sprintf("run failed (exit %d)", exitCode)
	}
	return res
}

// limitDetailOr is the reason the limit event gave for the pause, or fallback
// when it gave none. It is kept apart from detail so a pause never borrows the
// wording of an unrelated failure, and the other way round.
func (c *Collector) limitDetailOr(fallback string) string {
	if c.limitDetail != "" {
		return c.limitDetail
	}
	return fallback
}

func (c *Collector) detailOr(fallback string) string {
	if c.detail != "" {
		return c.detail
	}
	return fallback
}

func (c *Collector) label() string {
	if c.name != "" {
		return c.name
	}
	return "the provider"
}
