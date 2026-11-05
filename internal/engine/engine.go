// Package engine ties the store, scheduler, limit gate, and executor into the
// dispatchq run loop: on each tick it starts every task that is due and permitted
// by priority/concurrency/limit rules, and records outcomes (PLAN.md §5.2/§7).
package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/limit"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/randid"
	"github.com/danielmaier42/claudeq/internal/schedule"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
	"github.com/danielmaier42/claudeq/internal/wake"
	"github.com/danielmaier42/claudeq/internal/workflow"
)

// DefaultRateLimitBackoff is used when a rate-limit event does not carry a
// retry delay (PLAN.md V2: an absolute reset time is not always exposed).
const DefaultRateLimitBackoff = 15 * time.Minute

// RateLimitResetBuffer is added on top of the absolute reset time reported by
// the CLI before resuming, so a run never restarts marginally before the
// window actually reopens (clock skew, server-side rounding).
const RateLimitResetBuffer = time.Minute

// Runner executes a single request. *executor.Executor satisfies it; tests use
// a stub.
type Runner interface {
	Run(ctx context.Context, req executor.Request) (provider.Result, error)
}

// Waker schedules the machine to wake at a future time. *wake.Scheduler
// satisfies it. It is optional; when nil the engine does not plan wakes.
type Waker interface {
	Schedule(ctx context.Context, at time.Time) error
}

// SetWaker enables wake planning after each loop tick.
func (e *Engine) SetWaker(w Waker) { e.waker = w }

// SetNotifier enables outcome notifications (failure / auth error).
func (e *Engine) SetNotifier(n notify.Notifier) { e.notifier = n }

// SetRunFinished registers f to be told the provider instance a run just ended
// on — the moment that account's allowance has moved. f must not block: it is
// called on the finishing run's goroutine. Script jobs use no provider and do
// not call it.
func (e *Engine) SetRunFinished(f func(providerID string)) { e.runFinished = f }

// SetLimits gives the engine each provider's last allowance reading, which is
// what a pool picks its member by. f should answer from memory: it is asked on
// the scheduler's pass. Without it every pool member counts as unread, and a
// pool spreads its work by how busy each member is.
func (e *Engine) SetLimits(f func(ctx context.Context, inst provider.Instance) (provider.Limits, bool)) {
	e.limits = f
}

// WakeError reports the last wake-scheduling error, or "" if the most recent
// attempt succeeded (or wake is disabled / not yet attempted). Surfaced in the
// UI so a broken scheduled-wake setup (e.g. missing pmset sudoers entry) is
// visible instead of silently failing.
func (e *Engine) WakeError() string {
	if e.waker == nil {
		return ""
	}
	if p := e.wakeErr.Load(); p != nil {
		return *p
	}
	return ""
}

// LimitedUntil returns the earliest time a blocked provider's rate limit
// reopens, or the zero time when none is blocked. Surfaced in the UI so a queue
// that is waiting (rather than stuck) says so, and names the time it continues.
func (e *Engine) LimitedUntil() time.Time { return e.gates.BlockedUntil() }

// BlockedProviders returns the provider instance IDs currently waiting out a
// rate limit, each with the time its own gate reopens. Surfaced in the UI so
// the waiting banner names which account it is waiting on, not just "the
// queue".
func (e *Engine) BlockedProviders() map[string]time.Time { return e.gates.Blocked() }

// BlockReasons returns, for the blocked provider instances whose pause is an
// account block rather than a used-up allowance window, which block it is (a
// provider.Block). Surfaced on the dashboard so a used-up credit balance reads
// as one, not as a limit that resets on its own.
func (e *Engine) BlockReasons() map[string]string { return e.gates.Reasons() }

// RunningOn returns how many runs are in flight per provider instance, which
// is part of a provider's urgency (see provider.ScoreProvider). Surfaced on the
// Dashboard, so the urgency it shows is the one the scheduler acts on.
func (e *Engine) RunningOn() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return maps.Clone(e.runningOn)
}

// Holds returns, per task, why the last tick did not start it although it was
// due, and since when it has been waiting. Surfaced in the queue, so a task
// that sits there says what it waits for.
func (e *Engine) Holds() map[string]schedule.Hold { return e.holds.snapshot() }

// Engine orchestrates task execution. Construct it with [New].
type Engine struct {
	store *store.Store
	// gates hold the rate limits, one per provider instance: an allowance
	// belongs to an account, so a blocked Codex says nothing about Claude and a
	// second subscription is not held up by the first.
	gates *limit.Gates
	run   Runner
	clock clock.Clock

	newRunID     func() string
	newSessionID func() string
	backoff      time.Duration
	waker        Waker
	lastWakeErr  string // loop-local, for once-only logging
	// lastArtifactErr and lastNotifyErr are loop-local too (see
	// notifyNewArtifacts and deliverTaskNotifications).
	lastArtifactErr string
	lastNotifyErr   string
	wakeErr         atomic.Pointer[string] // exposed to the API (thread-safe)
	notifier        notify.Notifier
	runFinished     func(providerID string)
	limits          func(ctx context.Context, inst provider.Instance) (provider.Limits, bool)

	runCtx    context.Context
	runCancel context.CancelFunc

	// providers answers whether a task's harness can run right now. It is
	// required: without it the daemon would start jobs on a CLI that is missing
	// or logged out and record the failure at three in the morning.
	providers *provider.Checker

	mu                sync.Mutex
	active            map[string]bool               // taskID -> currently running
	cancels           map[string]context.CancelFunc // runID -> stops that run's process
	canceled          map[string]bool               // runID -> user requested cancellation
	takeovers         map[string]chan store.Run     // runID -> waits for the record of a run taken over by hand
	nonParallelActive int
	parallelActive    int
	// runningOn counts the runs in flight per provider instance, which a pool
	// weighs its members by so concurrent starts spread out.
	runningOn map[string]int
	wg        sync.WaitGroup
	awake     sleepGuard // keeps the Mac awake while runs are in flight

	// providerHealth is the health state last announced per provider, so an
	// unresolved problem is reported once instead of on every tick. It is seeded
	// from state.json, which is what makes the memo survive a restart. It has its
	// own lock because the readiness pass deliberately runs outside mu.
	healthMu       sync.Mutex
	providerHealth map[string]string

	// holds says why each due task the last tick left out did not start, and
	// holdLog is where a long wait is written down (stdout, the daemon's log).
	holds   holds
	holdLog io.Writer
}

// ShutdownGrace is how long Loop lets in-flight runs finish on shutdown before
// terminating them, so a normal stop/restart doesn't fail running tasks.
const ShutdownGrace = 30 * time.Second

// New builds an Engine with production defaults (real UUIDs and run ids).
// providers is how the engine finds out whether a task's harness can run at
// all; it must not be nil.
func New(st *store.Store, gates *limit.Gates, r Runner, c clock.Clock, providers *provider.Checker) *Engine {
	e := &Engine{
		store:          st,
		gates:          gates,
		run:            r,
		clock:          c,
		providers:      providers,
		backoff:        DefaultRateLimitBackoff,
		active:         map[string]bool{},
		cancels:        map[string]context.CancelFunc{},
		canceled:       map[string]bool{},
		takeovers:      map[string]chan store.Run{},
		runningOn:      map[string]int{},
		providerHealth: map[string]string{},
		holdLog:        os.Stdout,
	}
	// Runs use their own context so that cancelling the loop (SIGINT) does not
	// immediately kill in-flight Claude processes; shutdown drains them first.
	e.runCtx, e.runCancel = context.WithCancel(context.Background())
	e.newRunID = func() string {
		return e.clock.Now().UTC().Format("20060102T150405") + "-" + randid.Hex(4)
	}
	e.newSessionID = randid.UUID
	return e
}

// Tick starts every task that is due and permitted right now. Started tasks run
// asynchronously; use [Engine.WaitIdle] to await their completion.
func (e *Engine) Tick(ctx context.Context) error {
	cfg, err := e.store.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// The global pause switch stops the queue dead: nothing new is started, and
	// no scheduling state is touched, so a task that came due while paused runs
	// as soon as the switch goes off again.
	if cfg.Settings.Paused {
		e.holdAll(cfg, "Runs are paused.")
		return nil
	}

	providers, err := provider.FromConfig(cfg)
	if err != nil {
		return fmt.Errorf("read provider configuration: %w", err)
	}

	// Which tasks are due is decided without touching a CLI, so the lock is held
	// only for the scheduling arithmetic. A task whose provider is waiting out
	// its own rate limit is dropped here too — there is no point probing a
	// harness that may not be asked to run anyway, and the allowance belongs to
	// that account alone, so the other providers carry on.
	// Which dependent jobs may go is decided once per tick, from history: it is
	// the same answer for every task, and reading the log per task would make a
	// fan-out quadratic in its own size.
	deps, err := e.dependencyState(cfg)
	if err != nil {
		return err
	}
	due, err := e.dueTasks(cfg, deps)
	if err != nil {
		return err
	}
	// Backfill work only soaks up allowance nothing else wants, so it never
	// goes ahead of a task that is not backfill: not to a pool's best member,
	// and not into the exclusive slot.
	slices.SortStableFunc(due, func(a, b task.Task) int {
		return cmp.Compare(b2i(a.Backfill), b2i(b.Backfill))
	})
	// held collects why each due task does not start this tick; whatever is
	// left in it at the end is what the queue shows.
	held := map[string]string{}
	assigned := e.assign(ctx, providers, due, cfg.Settings.BackfillUrgencyOrDefault(), held)

	// Their harnesses are then probed *outside* the lock. A readiness check
	// spawns a CLI and a CLI can hang; holding e.mu across that would stall the
	// dashboard, a cancel and every finishing run for as long as the timeout.
	// Only the providers of tasks that are actually due are asked, and they are
	// asked without the cache — this is the moment right before a start, which is
	// exactly when a stale verdict would cost a recorded run.
	health := e.refreshProviderHealth(ctx, providers, assigned)
	if ctx.Err() != nil {
		// Stopping: the probes were cut short, so their verdicts must not hold
		// tasks back or start them. The next daemon's first tick decides.
		return nil
	}

	// Back under the lock, with the scheduling state read *here*: a run that
	// finished while the probes were out has already written its completion, and
	// deciding against a snapshot from before that would start a one-shot task a
	// second time.
	e.mu.Lock()
	defer e.mu.Unlock()

	st, err := e.store.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	now := e.clock.Now()
	// Seed the in-memory snapshot so freshly-added cron tasks have an anchor for
	// the due check below (their first run is the next occurrence, not now).
	seeded := e.seedCronAnchors(cfg, st, now)

	// A task whose harness cannot run it is not started and not retired: it keeps
	// its place in the queue, takes no concurrency slot, and advances no
	// one-shot or cron state, so it simply runs once the provider is ready again.
	runnable := make([]task.Task, 0, len(assigned))
	ready := make(map[string]provider.Resolved, len(assigned))
	for _, a := range assigned {
		stillDue, err := e.isDue(a.task, st, deps, now)
		if err != nil {
			return err
		}
		switch {
		case !stillDue:
			delete(held, a.task.ID)
			continue
		case a.err != nil:
			held[a.task.ID] = a.err.Error()
			continue
		case !a.task.IsScript() && !health.ready(a.resolved):
			held[a.task.ID] = health.reason(a.resolved)
			continue
		}
		runnable = append(runnable, a.task)
		ready[a.task.ID] = a.resolved
	}

	selected := schedule.Select(runnable, e.runningState())
	maps.Copy(held, schedule.Held(runnable, e.runningState()))
	e.holds.update(held, now, e.holdLog)
	starts := make([]start, 0, len(selected))
	for _, t := range selected {
		starts = append(starts, start{task: t, resolved: ready[t.ID], deps: deps[t.ID]})
	}

	// Persist scheduling state only when something changed, and via a targeted
	// update so we never clobber read-status set concurrently through the API.
	if seeded || len(starts) > 0 {
		if err := e.store.UpdateState(func(cur *store.State) error {
			e.seedCronAnchors(cfg, cur, now)
			for _, s := range starts {
				cur.RecordStart(s.task.ID, now)
				cur.RecordRun(s.task.ID, now)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("persist scheduling state: %w", err)
		}
	}

	for _, s := range starts {
		sessionID, resume, dropped := e.sessionFor(s.task, st, s.resolved.Instance.ID)
		notes := runNotes(s.resolved, dropped)
		if err := e.launchTask(s.task, cfg.Settings, s.resolved, s.deps, sessionID, resume, notes, now); err != nil {
			return err
		}
	}
	return nil
}

// holdAll files one reason for every task that is due right now: what a tick
// that starts nothing at all, such as while runs are paused, has to say. The
// reasons only feed the queue's display, so failing to work them out is logged
// and never stops the loop.
func (e *Engine) holdAll(cfg store.Config, reason string) {
	due, err := e.allDue(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: work out which tasks wait: %v\n", err)
		return
	}
	held := make(map[string]string, len(due))
	for _, t := range due {
		held[t.ID] = reason
	}
	e.holds.update(held, e.clock.Now(), e.holdLog)
}

// allDue is dueTasks with the dependencies worked out first.
func (e *Engine) allDue(cfg store.Config) ([]task.Task, error) {
	deps, err := e.dependencyState(cfg)
	if err != nil {
		return nil, err
	}
	return e.dueTasks(cfg, deps)
}

// dueTasks reports the tasks whose trigger has come round, without asking any
// provider anything. The verdict is re-taken under the lock before a start is
// recorded (see Tick); this first pass exists only to find out which harnesses
// are worth probing at all.
func (e *Engine) dueTasks(cfg store.Config, deps dependencyState) ([]task.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	st, err := e.store.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	now := e.clock.Now()
	// Give a freshly added cron task an anchor of now, so its first run is the
	// next occurrence rather than a backfill. The anchor is only persisted later,
	// under the lock that records the starts.
	e.seedCronAnchors(cfg, st, now)
	due := make([]task.Task, 0, len(cfg.Tasks))
	for _, t := range cfg.Tasks {
		ok, err := e.isDue(t, st, deps, now)
		if err != nil {
			return nil, err
		}
		if ok {
			due = append(due, t)
		}
	}
	return due, nil
}

// assignment is one due task together with the provider instance that would
// actually run it right now, or the reason it has none.
type assignment struct {
	task     task.Task
	resolved provider.Resolved
	err      error
}

// assign works out where each due task would run once the rate-limit fallback
// has been applied: a task whose provider is waiting out its allowance goes to
// the provider that one names as its fallback, if that one can take it.
//
// Tasks whose provider — and every fallback behind it — is still blocked are
// dropped here. There is no point probing a harness that is not going to be
// asked to run, and an allowance belongs to an account, so one provider's pause
// holds up only its own work. A task whose provider cannot be resolved at all
// is kept, carrying the reason: it is reported as blocked further down rather
// than silently disappearing from the tick.
//
// A backfill task goes only to a provider whose urgency is above backfill
// (see provider.Selection.Backfill); while there is none it is dropped too.
//
// Why a dropped task does not start is filed in held, by task id.
func (e *Engine) assign(ctx context.Context, set provider.Set, due []task.Task, backfill float64, held map[string]string) []assignment {
	out := make([]assignment, 0, len(due))
	// Which pool member holds a task's interrupted session is read once per
	// pass. Without it a pool would pick by rank and drop the session.
	st, stErr := e.store.LoadState()
	if stErr != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: load state for pool assignment: %v; pool tasks wait for the next tick\n", stErr)
	}
	// Runs planned onto a provider in this pass count as running on it, so two
	// pool tasks due in the same tick do not both pick the same account, and a
	// backfill task sees the urgency the work placed before it leaves.
	planned := map[string]int{}
	av := e.availability(ctx)
	running := av.Running
	av.Running = func(id string) int { return running(id) + planned[id] }
	for _, t := range due {
		// A script job runs no harness, so there is nothing to resolve and no
		// allowance it could be waiting for: it goes through even while every
		// provider sits out a rate limit. That is what lets a watcher keep
		// watching and file the work for when the limit reopens.
		if t.IsScript() {
			out = append(out, assignment{task: t})
			continue
		}
		// Without the state a pool cannot see which member holds the task's
		// interrupted session, and ranking it elsewhere would drop that session.
		// The task waits a tick instead.
		if t.Pool != "" && stErr != nil {
			held[t.ID] = "The scheduling state could not be read; the pool picks a member on the next tick."
			continue
		}
		sel := selectionFor(t, st)
		sel.Backfill, sel.MinUrgency = t.Backfill, backfill
		res, err := set.ResolveAvailable(sel, av)
		if errors.Is(err, provider.ErrNoSpare) {
			// Waiting for spare allowance is the normal state of backfill work,
			// not a provider problem worth announcing.
			held[t.ID] = "Backfill: " + strings.TrimPrefix(err.Error(), provider.ErrNoSpare.Error()+": ") + "."
			continue
		}
		if err == nil && !e.gateOpen(res.Instance.ID) {
			held[t.ID] = fmt.Sprintf("%s is out of allowance until %s.",
				res.Instance.Label(), reopenText(e.gates.For(res.Instance.ID).BlockedUntil(), e.clock.Now()))
			continue
		}
		if err == nil {
			planned[res.Instance.ID]++
		}
		out = append(out, assignment{task: t, resolved: res, err: err})
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// reopenText is when a gate reopens, as a time of day, with the date in front
// when that is not today: a weekly allowance reopening on Thursday must not
// read like tomorrow morning.
func reopenText(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	if at.YearDay() == now.YearDay() && at.Year() == now.Year() {
		return at.Format("15:04")
	}
	return at.Format("Mon 2 Jan 15:04")
}

// selectionFor is what a task asks the provider set for. A pool task also names
// the member its interrupted session is waiting on, if any.
func selectionFor(t task.Task, st *store.State) provider.Selection {
	sel := provider.Selection{ProviderID: t.Provider, Model: t.Model, PoolID: t.Pool}
	if t.Pool != "" && st != nil {
		if pending, ok := st.PendingResume(t.ID); ok {
			sel.Prefer = pending.ProviderID
		}
	}
	return sel
}

// gateOpen reports whether a provider instance has allowance left right now,
// which for the scheduler means its own rate-limit gate has reopened.
func (e *Engine) gateOpen(providerID string) bool { return e.gates.For(providerID).Open() }

// availability is what the fallback walk asks about the configured instances:
// whose allowance ran out, and which of the stand-ins could take the work at
// all. Readiness is the remembered verdict rather than a fresh probe — the
// instance that is actually picked is probed straight afterwards, and asking
// every hop of every chain again would spawn a CLI for providers nothing is
// about to run on.
func (e *Engine) availability(ctx context.Context) provider.Availability {
	return provider.Availability{
		OutOfAllowance: func(id string) bool { return !e.gateOpen(id) },
		CanTakeWork: func(inst provider.Instance) bool {
			return !e.providers.Check(ctx, inst).KnownUnready()
		},
		Limits: func(inst provider.Instance) (provider.Limits, bool) {
			if e.limits == nil {
				return provider.Limits{}, false
			}
			return e.limits(ctx, inst)
		},
		Running: func(id string) int {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.runningOn[id]
		},
		Now: e.clock.Now,
	}
}

// inheritedProvider is the instance a job queued by this run should inherit
// when it names none: the one the task was scheduled onto, not the stand-in a
// rate limit sent this single run to.
func inheritedProvider(res provider.Resolved) string {
	if res.Pool != nil {
		// The task names a pool, and so does whatever it queues (the task
		// itself travels to the child; see executor.Request.InheritProvider).
		return ""
	}
	if res.Substituted() {
		return res.FallbackFrom.ID
	}
	return res.Instance.ID
}

// runNotes are the remarks dispatchq writes into a run's own log before the
// harness says anything: that the run went to a fallback provider, and that a
// waiting session had to be abandoned. They go where whoever reads the run will
// look for them.
func runNotes(res provider.Resolved, dropped string) []string {
	var notes []string
	if res.Pool != nil {
		notes = append(notes, res.Pool.Note(res.Instance))
	}
	if res.Substituted() {
		notes = append(notes, fmt.Sprintf(
			"%s has used up its allowance, so this run goes to its fallback %s instead",
			res.FallbackFrom.Label(), res.Instance.Label()))
	}
	if dropped != "" {
		notes = append(notes, dropped)
	}
	return notes
}

// isDue evaluates one task's trigger. The caller holds e.mu, because whether the
// task is already running is part of the answer.
//
// A one-shot task holding an interrupted session has not completed, whatever
// its completed-once mark says. The mark can predate the session: a task
// re-queued under the id of one that already ran, and then started by hand,
// keeps it. Without this such a run, once a rate limit or a used-up credit
// balance stopped it, would never be picked up again: neither on another pool
// member or fallback right away, nor on its own account once that reopens.
func (e *Engine) isDue(t task.Task, st *store.State, deps dependencyState, now time.Time) (bool, error) {
	anchor, _ := st.LastStart(t.ID)
	_, waiting := st.PendingResume(t.ID)
	ok, err := schedule.Due(t, schedule.Inputs{
		Now:             now,
		Running:         e.active[t.ID],
		CompletedOnce:   st.IsCompletedOnce(t.ID) && !waiting,
		CronAnchor:      anchor,
		DependenciesMet: deps.met(t.ID),
	})
	if err != nil {
		return false, fmt.Errorf("evaluate task %q: %w", t.ID, err)
	}
	return ok, nil
}

// dependencyState is one tick's answer to "may this dependent job go yet", and
// what its dependencies said — computed once, from the run history.
type dependencyState map[string][]workflow.Dependency

// met reports whether every job the task waits for has finished. A task that
// waits for nothing is always met.
func (d dependencyState) met(taskID string) bool {
	ok, _ := workflow.Ready(d[taskID])
	return ok
}

// dependencyState resolves every waiting task's dependencies against history.
// Tasks that depend on nothing are not in the map and cost nothing.
func (e *Engine) dependencyState(cfg store.Config) (dependencyState, error) {
	var waiting []task.Task
	for _, t := range cfg.Tasks {
		if len(t.DependsOn) > 0 {
			waiting = append(waiting, t)
		}
	}
	if len(waiting) == 0 {
		return nil, nil
	}
	runs, err := e.store.Runs()
	if err != nil {
		return nil, fmt.Errorf("read history for dependencies: %w", err)
	}
	out := make(dependencyState, len(waiting))
	for _, t := range waiting {
		out[t.ID] = workflow.Resolve(t, cfg.Tasks, runs)
	}
	return out, nil
}

// start is one task the scheduler picked, together with the execution identity
// it was cleared to run under.
type start struct {
	task     task.Task
	resolved provider.Resolved
	// deps are the finished jobs this one waited for, so their answers can be
	// put in front of its prompt when it asked for them.
	deps []workflow.Dependency
}

// providerHealth is one tick's readiness verdict per provider id, taken before
// the scheduler lock so the decisions under it cost nothing.
type providerHealth map[string]provider.Health

// ready reports whether the instance a task was assigned to was found fit to
// run in this tick's probe. An instance that was not probed at all (the task
// was enabled moments ago) is not ready: not knowing is no reason to start, so
// it waits for the next tick, which will know.
//
// A provider that cannot run is never answered by moving its tasks elsewhere —
// the one substitution dispatchq makes is the rate-limit fallback, which happened
// before the probe. The scheduler leaves the rest queued; a manual run reports
// the reason to whoever asked for it.
func (h providerHealth) ready(res provider.Resolved) bool {
	got, ok := h[res.Instance.ID]
	return ok && got.Ready()
}

// reason says why res cannot run, for a task the check held back.
func (h providerHealth) reason(res provider.Resolved) string {
	return h[res.Instance.ID].ReasonOr(res.Instance.Label() + " cannot run tasks right now.")
}

// resolveRunnable probes one task's provider and reports the execution identity
// it may run under, or why it may not. It is the single-task form of
// refreshProviderHealth, for the manual "run now".
func (e *Engine) resolveRunnable(ctx context.Context, set provider.Set, st *store.State, t task.Task) (provider.Resolved, error) {
	res, err := set.ResolveAvailable(selectionFor(t, st), e.availability(ctx))
	if err != nil {
		return provider.Resolved{}, err
	}
	h := e.providers.CheckFresh(ctx, res.Instance)
	if err := ctx.Err(); err != nil {
		// A probe cut short says nothing about the provider; see refreshProviderHealth.
		return provider.Resolved{}, fmt.Errorf("check provider %q: %w", res.Instance.ID, err)
	}
	e.noteProviderHealth(st, res.Instance, h)
	if !h.Ready() {
		return provider.Resolved{}, fmt.Errorf("provider %q is not ready: %s",
			res.Instance.ID, h.ReasonOr("it cannot run tasks right now"))
	}
	return res, nil
}

// refreshProviderHealth probes the provider of every task that is due, once per
// distinct provider, and announces whatever changed. It runs outside the
// scheduler lock, and it probes without the cache — a task is due only when it
// is actually time to run it, so this is the check immediately before a start
// rather than something the tick does every five seconds.
func (e *Engine) refreshProviderHealth(ctx context.Context, set provider.Set, due []assignment) providerHealth {
	out := providerHealth{}
	known := map[string]bool{}
	for _, inst := range set.All() {
		known[inst.ID] = true
	}
	st, err := e.store.LoadState()
	if err != nil {
		st = nil // the memo falls back to memory; a health check is not worth failing a tick over
	}
	for _, a := range due {
		if a.task.IsScript() {
			continue // no harness behind it, so nothing to probe
		}
		asked := a.task.Provider
		if a.task.Pool != "" {
			asked = a.task.Pool
		}
		known[asked] = true // an id no instance answers to is remembered too
		if a.err != nil {
			// The instance is gone from the configuration, or switched off. There
			// is nothing to probe, so it is reported under the id the task asked
			// for.
			e.noteProviderHealth(st, provider.Instance{ID: asked, Name: asked},
				provider.Health{State: provider.HealthInvalidConfiguration, Reason: a.err.Error()})
			continue
		}
		inst := a.resolved.Instance
		if _, done := out[inst.ID]; done {
			continue
		}
		h := e.providers.CheckFresh(ctx, inst)
		if ctx.Err() != nil {
			// The daemon is stopping (an update, a restart). A probe cut off
			// halfway says nothing about the provider, and announcing it as
			// unable to run tasks would raise an alarm about a working account.
			return out
		}
		out[inst.ID] = h
		e.noteProviderHealth(st, inst, h)
	}
	e.forgetUnknownProviders(known)
	return out
}

// forgetUnknownProviders drops the announcement memo of provider ids nothing
// refers to any more, so an instance removed and later re-added under the same
// id is announced again instead of inheriting the old one's state.
func (e *Engine) forgetUnknownProviders(known map[string]bool) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	for id := range e.providerHealth {
		if !known[id] {
			delete(e.providerHealth, id)
		}
	}
}

// noteProviderHealth announces a provider's readiness when it changes, and only
// then: the scheduler looks at every blocked task on every tick, and an alert
// per tick would bury the one that matters. The memo is kept in state.json as
// well as in memory, so a restart does not re-announce a condition the operator
// already knows about.
func (e *Engine) noteProviderHealth(st *store.State, inst provider.Instance, h provider.Health) {
	current := string(h.State)
	e.healthMu.Lock()
	previous, seen := e.providerHealth[inst.ID]
	if !seen && st != nil {
		previous = st.NotifiedProviderState(inst.ID)
	}
	e.providerHealth[inst.ID] = current
	e.healthMu.Unlock()
	if previous == current {
		return
	}
	if err := e.store.UpdateState(func(s *store.State) error {
		s.SetNotifiedProviderState(inst.ID, current)
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: record provider health for %q: %v\n", inst.ID, err)
	}
	if e.notifier == nil {
		return
	}
	n := notify.Notification{Kind: store.InboxKindProvider}
	switch {
	case h.Ready() && previous == "":
		// First look at a working provider: nothing happened worth announcing.
		return
	case h.Ready():
		n.Title = "DispatchQ: " + inst.Label() + " is ready again"
		n.Message = "Tasks waiting for this provider will start on the next check."
	default:
		n.Title = "DispatchQ: " + inst.Label() + " cannot run tasks"
		n.Message = h.ReasonOr("The provider is not ready.") + "\nTasks for it stay queued until it works again."
	}
	// Off the scheduler goroutine: a channel that takes its time must not delay
	// the tick that noticed the problem.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.send(n)
	}()
}

// seedCronAnchors gives every not-yet-seen cron task an anchor of now, so its
// first run is the next occurrence after it was added (not a backfill).
func (e *Engine) seedCronAnchors(cfg store.Config, st *store.State, now time.Time) bool {
	changed := false
	for _, t := range cfg.Tasks {
		if t.Trigger != task.TriggerCron {
			continue
		}
		if _, ok := st.LastStart(t.ID); !ok {
			st.RecordStart(t.ID, now)
			changed = true
		}
	}
	return changed
}

func (e *Engine) runningState() schedule.Running {
	return schedule.Running{
		NonParallel: e.nonParallelActive > 0,
		Parallel:    e.parallelActive > 0,
	}
}

// launchTask starts a run for t on the already-resolved execution identity. The
// caller must hold e.mu, have checked that the provider can run, and have
// persisted the RecordStart. sessionID/resume come from the caller's snapshot,
// notes are the remarks that go into the run's log before the harness starts
// writing (see runNotes).
func (e *Engine) launchTask(t task.Task, settings store.Settings, resolved provider.Resolved, deps []workflow.Dependency, sessionID string, resume bool, notes []string, started time.Time) error {
	runID := e.newRunID()

	logFile, err := os.Create(e.store.LogPath(runID))
	if err != nil {
		return fmt.Errorf("create log for run %s: %w", runID, err)
	}

	snapshot := t
	// A run that belongs to no workflow yet starts one under its own id, so a
	// job it queues has something to inherit and the two read as one piece of
	// work afterwards.
	workflowID := t.WorkflowID
	if workflowID == "" {
		workflowID = runID
	}
	rec := store.Run{
		RunID: runID, TaskID: t.ID, TaskName: t.Name,
		StartedAt: started, Status: store.StatusRunning,
		SessionID: sessionID, LogPath: e.store.LogPath(runID),
		Task:        &snapshot,
		WorkflowID:  workflowID,
		ParentRunID: t.ParentRun,
		// The identity is written down now, while it is true. A provider renamed
		// or removed tomorrow must not change what this run says it ran on.
		Provider: runProvider(t, resolved),
	}
	// Record the start before marking the task active, so a failure here leaves
	// no task stuck in the running set (which would block the scheduler). A
	// quiet-history task is the exception: its runs enter history only if they
	// end in something worth seeing (see finish), so nothing is written now.
	if !t.QuietHistory {
		if err := e.store.AppendRun(rec); err != nil {
			_ = logFile.Close()
			return fmt.Errorf("record run start: %w", err)
		}
	}

	// What dispatchq itself has to say about this run — a fallback provider, a
	// session that could not be continued — goes into the run's own log, through
	// the same handle the executor writes to, so it cannot collide with the
	// harness's first line.
	for _, note := range notes {
		writeNote(logFile, note)
	}

	e.active[t.ID] = true
	if id := resolved.Instance.ID; id != "" {
		e.runningOn[id]++
	}
	if t.Parallel {
		e.parallelActive++
	} else {
		e.nonParallelActive++
	}
	e.awake.acquire() // hold off idle system sleep until this run finishes

	// What the jobs it waited for answered goes in front of its own prompt,
	// fenced and labelled as data. The task itself is left alone: history keeps
	// the prompt the operator wrote, not one with a digest baked into it. A
	// script is a program, not prose — prepending anything to it would break it,
	// so the same digest reaches it on standard input instead.
	prompted, stdin := t, ""
	if t.IncludeResults && len(deps) > 0 {
		if t.IsScript() {
			stdin = workflow.Context(deps)
		} else {
			prompted.Prompt = workflow.Context(deps) + "\n" + t.Prompt
		}
	}

	req := executor.Request{
		Task:               prompted,
		Stdin:              stdin,
		WorkflowID:         workflowID,
		Provider:           resolved.Instance,
		InheritProvider:    inheritedProvider(resolved),
		RunID:              runID,
		SessionID:          sessionID,
		Resume:             resume,
		Model:              resolved.Model,
		AccessMode:         accessMode(t.Permissions),
		ReasoningEffort:    t.ReasoningEffort,
		CustomSystemPrompt: settings.SystemPrompt,
		IdleTimeout:        settings.IdleTimeout(),
		Log:                logFile,
	}
	// Per-run context so a single run can be cancelled from the dashboard
	// without touching the daemon-wide runCtx (which shutdown owns).
	runCtx, cancelRun := context.WithCancel(e.runCtx)
	e.cancels[runID] = cancelRun

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancelRun()
		defer func() { _ = logFile.Close() }()
		res, runErr := e.runGuarded(runCtx, req)
		e.finish(t, resolved.Instance.ID, rec, res, runErr)
	}()
	return nil
}

// runProvider is the execution identity a run records. A script job has none —
// no account, no model, no authority a harness would ask about — so it records
// what it was instead, which is what the app shows where the provider goes.
func runProvider(t task.Task, resolved provider.Resolved) store.RunProvider {
	if t.IsScript() {
		return store.RunProvider{Kind: store.ScriptRunKind, Name: "Script"}
	}
	return store.RunProvider{
		ID:              resolved.Instance.ID,
		Kind:            string(resolved.Instance.Kind),
		Name:            resolved.Instance.Label(),
		Pool:            poolLabel(resolved),
		Model:           resolved.Model,
		ReasoningEffort: t.ReasoningEffort,
		AccessMode:      string(accessMode(t.Permissions)),
	}
}

// poolLabel names the pool a run was placed through, or "".
func poolLabel(res provider.Resolved) string {
	if res.Pool == nil {
		return ""
	}
	return res.Pool.Pool.Label()
}

// runGuarded runs the request and turns a panic into a failed result instead of
// crashing the daemon, so one bad run never takes the whole queue down.
func (e *Engine) runGuarded(ctx context.Context, req executor.Request) (res provider.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = provider.Result{Status: store.StatusFailed, Message: fmt.Sprintf("internal error: %v", r)}
			err = nil
		}
	}()
	return e.run.Run(ctx, req)
}

// CancelRun stops a run the user no longer wants. A run that is in flight has
// its process (group) terminated and is recorded as canceled. A run that has
// already paused on the rate limit is not a process any more but a plan — its
// scheduled resume is dropped instead, so the interrupted session is not picked
// up when the gate reopens (see cancelResume). Returns an error when the run id
// is neither in flight nor waiting to resume.
func (e *Engine) CancelRun(runID string) error {
	e.mu.Lock()
	cancel, ok := e.cancels[runID]
	if ok {
		e.canceled[runID] = true
	}
	e.mu.Unlock()
	if ok {
		cancel()
		return nil
	}
	return e.cancelResume(runID)
}

// TakeOverRun stops a run in flight so its session can be continued by hand,
// and returns the run as finally recorded. Two processes must never write to
// one session, so it waits until the unattended one is gone and its record is
// written — that record also carries the session id the harness reported,
// which for some harnesses is only known once the run ends. The run is
// recorded as canceled (a success that beat the kill stays a success), with a
// reason that says where the conversation went.
func (e *Engine) TakeOverRun(ctx context.Context, runID string) (store.Run, error) {
	e.mu.Lock()
	cancel, ok := e.cancels[runID]
	if !ok {
		e.mu.Unlock()
		return store.Run{}, fmt.Errorf("run %q is not running", runID)
	}
	if e.canceled[runID] {
		e.mu.Unlock()
		return store.Run{}, fmt.Errorf("run %q is already being stopped", runID)
	}
	done := make(chan store.Run, 1)
	e.takeovers[runID] = done
	e.canceled[runID] = true
	e.mu.Unlock()

	cancel()
	select {
	case rec := <-done:
		return rec, nil
	case <-ctx.Done():
		return store.Run{}, fmt.Errorf("wait for run %q to stop: %w", runID, ctx.Err())
	}
}

// cancelResume drops the scheduled resume of a rate-limited run: the pending
// session is forgotten, a one-shot task leaves the queue (so it never runs
// again), and the run is recorded as canceled. A recurring task keeps its
// schedule — only the interrupted session is discarded, its next occurrence
// starts fresh.
func (e *Engine) cancelResume(runID string) error {
	runs, err := e.store.Runs()
	if err != nil {
		return fmt.Errorf("read history: %w", err)
	}
	var rec *store.Run
	for i := range runs {
		if runs[i].RunID == runID {
			rec = &runs[i]
			break
		}
	}
	switch {
	case rec == nil:
		return fmt.Errorf("run %q is not running", runID)
	case rec.Status != store.StatusRateLimited:
		return fmt.Errorf("run %q is not waiting to resume", runID)
	}

	e.mu.Lock()
	active := e.active[rec.TaskID]
	e.mu.Unlock()
	if active {
		// The task is running again already (a resume in flight); cancel that
		// run by its own id instead of retracting a plan that has been acted on.
		return fmt.Errorf("run %q has already resumed", runID)
	}

	st, err := e.store.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if pending, ok := st.PendingResume(rec.TaskID); !ok || pending.SessionID != rec.SessionID {
		return fmt.Errorf("run %q is no longer scheduled to resume", runID)
	}

	e.retire(rec.TaskID, e.isOneShot(*rec))

	canceled := *rec
	canceled.Status = store.StatusCanceled
	canceled.ResumeAt = nil
	canceled.Error = "resume canceled by the user"
	if canceled.FinishedAt == nil {
		now := e.clock.Now()
		canceled.FinishedAt = &now
	}
	if err := e.store.AppendRun(canceled); err != nil {
		return fmt.Errorf("record canceled resume: %w", err)
	}
	e.logStatus(canceled)
	return nil
}

// isOneShot reports whether the run's task runs only once, so cancelling its
// resume must take it out of the queue. The queued definition decides (its
// trigger may have been edited since the run started); the run's own snapshot
// is the fallback for a task that has meanwhile left the queue.
func (e *Engine) isOneShot(rec store.Run) bool {
	if cfg, err := e.store.LoadConfig(); err == nil {
		for _, t := range cfg.Tasks {
			if t.ID == rec.TaskID {
				return oneShotTrigger(t.Trigger)
			}
		}
	}
	return rec.Task != nil && oneShotTrigger(rec.Task.Trigger)
}

func oneShotTrigger(tr task.Trigger) bool {
	return tr == task.TriggerASAP || tr == task.TriggerFixed
}

// retire clears a task's pending resume and, for a one-shot task, marks it
// completed and takes it out of the queue — it stays in history and can be
// replayed from there. Recurring tasks remain queued for their next occurrence.
func (e *Engine) retire(taskID string, oneShot bool) {
	_ = e.store.UpdateState(func(st *store.State) error {
		st.ClearPendingResume(taskID)
		if oneShot {
			st.MarkCompletedOnce(taskID)
		}
		return nil
	})
	if !oneShot {
		return
	}
	_ = e.store.UpdateConfig(func(cfg *store.Config) error {
		for i := range cfg.Tasks {
			if cfg.Tasks[i].ID == taskID {
				cfg.Tasks = append(cfg.Tasks[:i], cfg.Tasks[i+1:]...)
				break
			}
		}
		return nil
	})
}

// logStatus records a run's final status and reason into its log, so the reason
// shows in both the raw and the chat view (especially for failures, pauses and
// interruptions).
func (e *Engine) logStatus(rec store.Run) {
	reason := rec.Error
	if reason == "" {
		reason = string(rec.Status)
	}
	line, err := json.Marshal(map[string]string{
		"type": "dispatchq_status", "status": string(rec.Status), "message": reason,
	})
	if err != nil {
		return
	}
	_ = e.store.AppendRunLog(rec.RunID, append(line, '\n'))
}

// writeNote records a dispatchq remark in a run's log, so something the operator
// needs to know about the run is where they read the run.
func writeNote(log io.Writer, message string) {
	line, err := json.Marshal(map[string]string{"type": "dispatchq_status", "status": "note", "message": message})
	if err != nil {
		return
	}
	_, _ = log.Write(append(line, '\n'))
}

// sessionFor returns the session to use, whether it is a resume, and — when a
// waiting session had to be abandoned — why.
//
// A task waiting after a rate limit continues its own session. It can only do
// that on the provider that issued it: a session id means nothing to another
// harness, and nothing at all to another account. A task moved in the meantime
// therefore starts fresh rather than handing Codex a Claude conversation.
//
// The abandoned session is not kept for later. When the substitute finishes,
// the task is retired like any other completed run and its pending resume goes
// with it: the work has been done from the start on the other account, and
// running it a second time when the first provider's window reopens would be
// worse than losing the conversation.
func (e *Engine) sessionFor(t task.Task, st *store.State, providerID string) (sessionID string, resume bool, dropped string) {
	// A script keeps no conversation: there is no session to hand it and none it
	// could ever be waiting to resume.
	if t.IsScript() {
		return "", false, ""
	}
	pending, ok := st.PendingResume(t.ID)
	switch {
	case !ok:
		return e.newSessionID(), false, ""
	case pending.ProviderID == "" || pending.ProviderID == providerID:
		// No provider recorded means it was written before sessions carried one,
		// when there was only the one provider to have issued it.
		return pending.SessionID, true, ""
	}
	return e.newSessionID(), false, fmt.Sprintf(
		"the interrupted session belongs to provider %q and this task now runs on %q, so it starts fresh",
		pending.ProviderID, providerID)
}

// finish records a completed run and updates scheduling state. providerID is
// the instance the run executed on, because a rate limit closes that
// provider's gate and no other.
func (e *Engine) finish(t task.Task, providerID string, rec store.Run, res provider.Result, runErr error) {
	e.mu.Lock()
	wasCanceled := e.canceled[rec.RunID]
	takeover := e.takeovers[rec.RunID]
	delete(e.canceled, rec.RunID)
	delete(e.takeovers, rec.RunID)
	delete(e.cancels, rec.RunID)
	if providerID != "" {
		if e.runningOn[providerID]--; e.runningOn[providerID] <= 0 {
			delete(e.runningOn, providerID)
		}
	}
	if t.Parallel {
		e.parallelActive--
	} else {
		e.nonParallelActive--
	}
	e.mu.Unlock()
	e.awake.release()

	finished := e.clock.Now()
	rec.FinishedAt = &finished
	rec.ExitCode = res.ExitCode
	rec.Status = res.Status
	if res.SessionID != "" {
		rec.SessionID = res.SessionID
	}
	if res.Model != "" {
		rec.Provider.ReportedModel = res.Model
	}
	if runErr != nil {
		rec.Status = store.StatusFailed
		rec.Error = runErr.Error()
	} else if res.Message != "" {
		rec.Error = res.Message
	}
	// The harness's own answer is kept on the record, bounded: it is what a
	// dependent job consolidates and what the notification quotes, and history
	// is read whole on every load.
	rec.FinalOutput, rec.FinalOutputTruncated = clipOutput(res.FinalOutput)
	if m := res.Metrics; m != nil {
		rec.CostUSD = m.CostUSD
		rec.InputTokens = m.InputTokens
		rec.OutputTokens = m.OutputTokens
		rec.NumTurns = m.NumTurns
		rec.DurationMS = m.DurationMS
	}
	// A user-requested cancellation surfaces as an interrupted/failed run from
	// the executor; record it as canceled instead. If the process managed to
	// finish successfully before the kill landed, keep the success.
	if wasCanceled && rec.Status != store.StatusSuccess {
		rec.Status = store.StatusCanceled
		rec.Error = "stopped by the user"
		if takeover != nil {
			rec.Error = "taken over in Terminal by the user"
		}
	}

	// Targeted state update: touch only this task's keys so a concurrent API
	// read-status change is preserved.
	switch rec.Status {
	case store.StatusRateLimited:
		// Prefer the absolute reset time the CLI reported (rate_limit_event):
		// block exactly until then plus a small buffer. Fall back to the retry
		// delay, then to the blind backoff, when no reset time is known or it
		// already lies in the past (stale event).
		// The limit belongs to the provider that hit it, so only its gate closes.
		gate := e.gates.For(providerID)
		if until := res.ResetAt; !until.IsZero() && until.After(e.clock.Now()) {
			gate.BlockBecause(until.Add(RateLimitResetBuffer), string(res.Block))
		} else {
			delay := res.RetryAfter
			if delay <= 0 {
				delay = e.backoff
			}
			// wait for reset, then resume this session
			gate.BlockBecause(e.clock.Now().Add(delay), string(res.Block))
		}
		// Record when the session is planned to continue (the gate keeps the
		// longest known block, so this is the real time, not just this run's),
		// so the pause reads as a scheduled resume instead of a dead end.
		if resume := gate.BlockedUntil(); !resume.IsZero() {
			rec.ResumeAt = &resume
		}
		_ = e.store.UpdateState(func(st *store.State) error {
			st.SetPendingResume(t.ID, res.SessionID, providerID)
			return nil
		})
	default:
		e.retire(t.ID, oneShotTrigger(t.Trigger))
	}

	// Only now does the task leave the running set. What keeps a one-shot from
	// starting a second time is CompletedOnce, and retire writes that above: a
	// tick that landed between the two would find the task neither running nor
	// completed and start it again. The concurrency counters were given back
	// before the write, so nothing waits on a disk write for its slot.
	e.mu.Lock()
	delete(e.active, t.ID)
	e.mu.Unlock()

	if quietDrop(t, rec.Status) {
		// A quiet task's routine tick leaves no trace: the run was never
		// recorded (see launchTask), and its log goes too.
		if err := os.Remove(rec.LogPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "dispatchqd: remove log of quiet run %s: %v\n", rec.RunID, err)
		}
	} else {
		_ = e.store.AppendRun(rec)

		// Bound disk usage: prune old runs/logs beyond the configured limit.
		if cfg, err := e.store.LoadConfig(); err == nil {
			_ = e.store.PruneHistory(cfg.Settings.RunHistoryLimit())
		}

		if rec.Status != store.StatusSuccess {
			e.logStatus(rec)
		}
	}

	if takeover != nil {
		takeover <- rec // buffered: the taker may have stopped waiting
	}

	if e.runFinished != nil && providerID != "" {
		e.runFinished(providerID)
	}

	// Notify outside any lock so channel I/O never blocks other finishing runs.
	e.notifyOutcome(t, rec, res.FinalOutput)
}

// clipOutput cuts a final answer to what a run record may carry, reporting
// whether it had to. The cut lands on a rune boundary, so the stored text is
// never half a character.
func clipOutput(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) <= store.MaxFinalOutput {
		return s, false
	}
	cut := store.MaxFinalOutput
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// quietDrop reports whether a run of a quiet-history task leaves history
// alone. Only a success does: it is the routine tick the setting exists to
// silence. Failures, auth problems, cancellations and rate-limit pauses are
// the outcomes the operator needs to see, so those are recorded like any other
// run's.
//
// A pause is kept even though the daemon resumes it by itself, because it is
// not a finished run: it closes the provider's gate and holds up every other
// task on that provider, possibly for days when a weekly allowance is what ran
// out. Dropping it left the queue stopped with a banner and nothing in
// the Log to explain it, and with no entry there was nowhere to cancel the
// resume either.
func quietDrop(t task.Task, status store.RunStatus) bool {
	if !t.QuietHistory {
		return false
	}
	return status == store.StatusSuccess
}

// notifyOutcome sends a best-effort notification. Failures and auth problems
// always notify (FA-35/FA-38); successes notify only when the task opted in via
// NotifyOnResult. When opted in, the last result message is included.
func (e *Engine) notifyOutcome(t task.Task, rec store.Run, resultText string) {
	if e.notifier == nil {
		return
	}
	msg := truncateRunes(strings.TrimSpace(resultText), 300)

	// The run is the click target: the notification opens its log in the window.
	n := notify.Notification{RunID: rec.RunID, TaskID: rec.TaskID, TaskName: rec.TaskName}
	switch rec.Status {
	case store.StatusSuccess:
		if !t.NotifyOnResult {
			return
		}
		n.Kind = store.InboxKindSuccess
		n.Title = "DispatchQ ✓ " + rec.TaskName
		n.Message = msg
		if n.Message == "" {
			n.Message = "Completed successfully."
		}
	case store.StatusFailed:
		n.Kind = store.InboxKindFailure
		n.Title = "DispatchQ ✗ " + rec.TaskName + " failed"
		if t.NotifyOnResult && msg != "" {
			n.Message = msg
		} else if rec.Error != "" {
			n.Message = rec.Error
		} else {
			n.Message = "Task failed."
		}
	case store.StatusAuthError:
		n.Kind = store.InboxKindFailure
		n.Title = "DispatchQ: login problem"
		n.Message = rec.TaskName + ": Claude Code authentication problem — please re-login."
	default:
		return
	}
	e.send(n)
}

// send delivers one notification, best-effort, and reports a failed channel
// on stderr: the daemon runs unattended, so the log is the only place the
// operator can find out why an alert never arrived. Deliberately not bound to
// the loop's context — a notification raised moments before shutdown would
// otherwise be lost.
//
// Before anything goes out, the notification is written to the inbox the
// Notifications view lists, under the id the macOS notification then carries:
// a click marks that entry read, and a channel that swallows the message still
// leaves it where the operator can find it.
func (e *Engine) send(n notify.Notification) {
	now := e.clock.Now()
	n.ID = "i-" + now.UTC().Format("20060102T150405") + "-" + randid.Hex(3)
	if err := e.store.AddInboxEntry(inboxEntry(n, now)); err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: notification %q not recorded: %v\n", n.Title, err)
		n.ID = "" // nothing to mark read; a click falls back to the target alone
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := e.notifier.Notify(ctx, n); err != nil {
		fmt.Fprintf(os.Stderr, "dispatchqd: notification %q not delivered: %v\n", n.Title, err)
	}
}

// inboxEntry is the inbox record of a notification about to be sent.
func inboxEntry(n notify.Notification, sentAt time.Time) store.InboxEntry {
	return store.InboxEntry{
		ID:         n.ID,
		Kind:       n.Kind,
		Title:      n.Title,
		Message:    n.Message,
		ArtifactID: n.ArtifactID,
		RunID:      n.RunID,
		URL:        n.URL,
		TaskID:     n.TaskID,
		TaskName:   n.TaskName,
		SentAt:     sentAt,
	}
}

// noteErr logs a recurring failure once: err is written to stderr only when it
// differs from the last one recorded in last (this runs on every tick), and a
// nil err clears the memo so the next failure is logged again.
func noteErr(last *string, what string, err error) {
	if err == nil {
		*last = ""
		return
	}
	if msg := err.Error(); msg != *last {
		fmt.Fprintln(os.Stderr, "dispatchqd: "+what+":", err)
		*last = msg
	}
}

// truncateRunes shortens s to at most limit runes, marking the cut with an
// ellipsis, so a long text still fits a notification channel's limits.
func truncateRunes(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// WaitIdle blocks until all in-flight runs have completed.
func (e *Engine) WaitIdle() { e.wg.Wait() }

// ActiveTaskIDs returns the ids of tasks currently running.
func (e *Engine) ActiveTaskIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.active))
	for id := range e.active {
		ids = append(ids, id)
	}
	return ids
}

// Loop runs Tick repeatedly until ctx is cancelled, then waits for in-flight
// runs to finish. Ticks no-op while the limit gate is closed. After each tick it
// plans the next wake (if a Waker is set), so the machine can sleep between runs
// and be woken when the next task is due (PLAN.md D8).
func (e *Engine) Loop(ctx context.Context, interval time.Duration) error {
	for {
		if err := e.Tick(ctx); err != nil {
			e.WaitIdle()
			return err
		}
		// Artifacts are published by the task's own dispatchq CLI call, so the
		// daemon learns about them by re-reading the list each tick.
		e.notifyNewArtifacts()
		// Same for notifications a task queued with `dispatchq notify`.
		e.deliverTaskNotifications()
		if e.waker != nil {
			// Wake scheduling is best-effort (needs root); never fatal. Log a
			// given failure only once to avoid spamming on every tick.
			err := e.planWake(ctx)
			switch {
			case ctx.Err() != nil:
				// Stopping: a wake plan cut short is neither a failure nor a success.
			case err != nil:
				msg := err.Error()
				if msg != e.lastWakeErr {
					fmt.Fprintln(os.Stderr, "dispatchqd: wake scheduling failed:", err)
					e.lastWakeErr = msg
				}
				e.wakeErr.Store(&msg)
			default:
				e.lastWakeErr = ""
				empty := ""
				e.wakeErr.Store(&empty)
			}
		}
		select {
		case <-ctx.Done():
			e.drain()
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// drain lets in-flight runs finish (up to ShutdownGrace) on shutdown, then
// terminates any stragglers — so a normal stop/restart doesn't fail runs.
func (e *Engine) drain() {
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(ShutdownGrace):
		e.runCancel() // terminate remaining runs
		e.wg.Wait()
	}
}

// planWake computes the next relevant wake time and registers it via the Waker.
func (e *Engine) planWake(ctx context.Context) error {
	cfg, err := e.store.LoadConfig()
	if err != nil {
		return err
	}
	st, err := e.store.LoadState()
	if err != nil {
		return err
	}
	now := e.clock.Now()
	// While paused nothing would run on wake, so nothing is worth waking for
	// (the heartbeat below stays, so the daemon notices the switch going off).
	var cands []time.Time
	if !cfg.Settings.Paused {
		cands = e.wakeCandidates(cfg, st, now)
		if bu := e.gates.BlockedUntil(); !bu.IsZero() {
			cands = append(cands, bu)
		}
	}
	at, ok := wake.NextWakeTime(now, cands, cfg.Settings.HeartbeatOrDefault())
	if !ok {
		return nil
	}
	return e.waker.Schedule(ctx, at)
}

// wakeCandidates returns concrete future times at which pending tasks want to
// run: future fixed starts and the next cron occurrences.
func (e *Engine) wakeCandidates(cfg store.Config, st *store.State, now time.Time) []time.Time {
	var out []time.Time
	for _, t := range cfg.Tasks {
		if !t.Enabled {
			continue
		}
		switch t.Trigger {
		case task.TriggerFixed:
			if !st.IsCompletedOnce(t.ID) && t.FixedAt.After(now) {
				out = append(out, t.FixedAt)
			}
		case task.TriggerCron:
			if sched, err := t.CronSchedule(); err == nil {
				anchor, ok := st.LastStart(t.ID)
				if !ok {
					anchor = now
				}
				out = append(out, sched.Next(anchor))
			}
		}
	}
	return out
}

// RunTaskNow runs a specific task once, synchronously, ignoring its trigger and
// completion state — the manual "run now" test trigger (FA-16). It refuses to
// start while runs are globally paused, and still records history and honours
// resume-after-limit for that run.
func (e *Engine) RunTaskNow(ctx context.Context, taskID string) error {
	cfg, err := e.store.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Settings.Paused {
		return store.ErrPaused
	}
	providers, err := provider.FromConfig(cfg)
	if err != nil {
		return fmt.Errorf("read provider configuration: %w", err)
	}
	var target *task.Task
	for i := range cfg.Tasks {
		if cfg.Tasks[i].ID == taskID {
			target = &cfg.Tasks[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("task %q not found", taskID)
	}

	st, err := e.store.LoadState()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	// Probed before the lock, like the scheduler's pass: a manual run reports the
	// problem to whoever pressed the button instead of filing a failed run, and a
	// hung CLI must not stall everything else in the meantime. A script job has
	// no harness to probe and runs whatever the providers are doing.
	var resolved provider.Resolved
	if !target.IsScript() {
		resolved, err = e.resolveRunnable(ctx, providers, st, *target)
		if err != nil {
			return err
		}
	}

	// A manual run is the operator overriding the schedule, so it does not wait
	// for the jobs the task depends on. What those jobs have answered so far is
	// still handed over, marked with what has not finished — running a join by
	// hand is a legitimate way to see it work, and silently dropping the results
	// would make it look broken.
	deps, err := e.dependencyState(cfg)
	if err != nil {
		return err
	}

	e.mu.Lock()
	if e.active[taskID] {
		e.mu.Unlock()
		return fmt.Errorf("task %q is already running", taskID)
	}
	sessionID, resume, dropped := e.sessionFor(*target, st, resolved.Instance.ID)
	notes := runNotes(resolved, dropped)
	now := e.clock.Now()
	if err := e.store.UpdateState(func(cur *store.State) error {
		cur.RecordStart(taskID, now)
		cur.RecordRun(taskID, now)
		return nil
	}); err != nil {
		e.mu.Unlock()
		return fmt.Errorf("record run start: %w", err)
	}
	startErr := e.launchTask(*target, cfg.Settings, resolved, deps[taskID], sessionID, resume, notes, now)
	e.mu.Unlock()

	e.WaitIdle()
	return startErr
}

// accessMode maps a task's permission setting onto dispatchq's provider-neutral
// access intent. The two stored values carry the same authority as before:
// leave the harness's own prompts in place, or bypass them entirely.
func accessMode(p task.Permissions) provider.AccessMode {
	if p == task.PermissionsSkip {
		return provider.AccessFullAccess
	}
	return provider.AccessProviderDefault
}
