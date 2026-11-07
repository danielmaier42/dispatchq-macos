// Package credit keeps track of the prepaid credit of accounts on an API key.
//
// Such a balance cannot be read without an admin key, so it is proven instead:
// by a one-word ping that costs a fraction of a cent when the account answers
// and nothing when it is refused, once a day and whenever the Dashboard's Read
// now asks, and by every run on the account. What was
// proven is written to the state file, so an account known to be out of credit
// stays closed across a restart, and the gate of such an account reopens only
// when a ping gets an answer again — never on a timer that hands it the next
// task to refuse.
package credit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// StatusOK is the CreditCheck status of an account that answered.
const StatusOK = "ok"

// Sources of a CreditCheck.
const (
	SourcePing = "ping"
	SourceRun  = "run"
)

// Interval is how often an account is pinged: once a day, whether it answered
// or was refused, so a balance that ran dry is known before the next night's
// queue is handed to it. An account refused for credit stays closed until the
// next day's ping gets an answer; after a top-up, the Dashboard's Read now
// pings at once. A ping that failed for another reason (no network, a CLI that
// did not start) waits for the next day too.
const Interval = 24 * time.Hour

// PingTimeout bounds one ping: the CLI starts, sends one request and exits.
const PingTimeout = 90 * time.Second

// PingText is what a ping says. The answer is not read: that the account
// answered at all is the proof.
const PingText = "Reply with the single word pong."

// PingModel is the model a ping asks, the cheapest Claude tier. Only Claude
// Code accounts on an API key are pinged (see provider.Limits.APIKey).
const PingModel = "haiku"

// Asker is the part of aside.Runner a ping uses.
type Asker interface {
	Ask(ctx context.Context, inst provider.Instance, req provider.AsideRequest) (provider.Aside, error)
}

// Monitor remembers each API-key account's last credit proof and pings the
// ones that are due. It is safe for concurrent use.
type Monitor struct {
	// Store persists the proofs (store.State.CreditChecks).
	Store *store.Store
	// List returns the configured instances.
	List func() ([]provider.Instance, error)
	// APIKey reports whether an instance is an account on an API key, which is
	// what the allowance reading finds out (provider.LimitMonitor.APIKey);
	// known is false until a reading said either way.
	APIKey func(provider.Instance) (apiKey, known bool)
	// Ask runs a ping.
	Ask Asker
	// Block closes a provider's gate for an account block; Lift reopens it.
	// Both are the engine's (BlockAccount, LiftAccountBlock).
	Block func(id string, b provider.Block)
	Lift  func(id string)
	// Now reads the clock. Nil means time.Now.
	Now func() time.Time
	// Logf reports pings that failed for a reason other than credit.
	Logf func(format string, args ...any)

	mu sync.Mutex
	// failedAt is when the latest ping of an instance failed without an answer
	// or a refusal. It is not a proof, so it is kept in memory only.
	failedAt map[string]time.Time
	failure  map[string]string
	pinging  map[string]*sync.Mutex
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Monitor) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// Restore closes the gate of every account the state file says is out of
// credit. The daemon calls it once at start, before the scheduler's first
// pass, so a restart does not hand that account the next task.
func (m *Monitor) Restore() error {
	st, err := m.Store.LoadState()
	if err != nil {
		return fmt.Errorf("load credit checks: %w", err)
	}
	for id, c := range st.CreditChecks {
		if c.Status != StatusOK && m.Block != nil {
			m.Block(id, provider.Block(c.Status))
		}
	}
	return nil
}

// Checks returns every remembered proof, by provider id.
func (m *Monitor) Checks() (map[string]store.CreditCheck, error) {
	st, err := m.Store.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load credit checks: %w", err)
	}
	return st.CreditChecks, nil
}

// Failure returns why the latest ping of id got neither an answer nor a
// refusal, or "" when it did.
func (m *Monitor) Failure(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failure[id]
}

// RunSeen records what a run proved about its account's credit. It only
// touches accounts a ping already knows as API-key accounts, so a run on a
// subscription leaves no credit entry behind.
func (m *Monitor) RunSeen(id string, block provider.Block, answered bool) {
	st, err := m.Store.LoadState()
	if err != nil {
		m.logf("credit: %v", err)
		return
	}
	if _, known := st.CreditChecks[id]; !known {
		return
	}
	status := StatusOK
	if !answered {
		status = string(block)
	}
	m.record(id, status, SourceRun)
}

// record persists a proof and opens or closes the gate to match it.
func (m *Monitor) record(id, status, source string) {
	c := store.CreditCheck{Status: status, CheckedAt: m.now(), Source: source}
	if err := m.Store.UpdateState(func(st *store.State) error {
		st.CreditChecks[id] = c
		return nil
	}); err != nil {
		m.logf("credit: save check of %s: %v", id, err)
	}
	switch {
	case status == StatusOK && m.Lift != nil:
		m.Lift(id)
	case status != StatusOK && m.Block != nil:
		m.Block(id, provider.Block(status))
	}
}

// forget drops the proof of id and reopens a gate it kept closed.
func (m *Monitor) forget(id string) {
	if err := m.Store.UpdateState(func(st *store.State) error {
		delete(st.CreditChecks, id)
		return nil
	}); err != nil {
		m.logf("credit: forget check of %s: %v", id, err)
	}
	if m.Lift != nil {
		m.Lift(id)
	}
}

// Due reports whether an account with this proof should be pinged at now.
func Due(c store.CreditCheck, known bool, now time.Time) bool {
	if !known {
		return true
	}
	return !now.Before(c.CheckedAt.Add(Interval))
}

// Ping asks inst for a one-word answer now and records what came back: an
// answer opens its gate, a refusal for credit closes it. A ping that failed
// for any other reason proves nothing and changes nothing; Failure says why.
func (m *Monitor) Ping(ctx context.Context, inst provider.Instance) {
	lock := m.pingLock(inst.ID)
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	_, err := m.Ask.Ask(ctx, inst, provider.AsideRequest{Model: PingModel, Text: PingText})
	var blocked *provider.AccountBlockedError
	switch {
	case err == nil:
		m.setFailure(inst.ID, "")
		m.record(inst.ID, StatusOK, SourcePing)
	case errors.As(err, &blocked) && blocked.Block != provider.BlockOrgDisallowed:
		m.setFailure(inst.ID, "")
		m.record(inst.ID, string(blocked.Block), SourcePing)
	case ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled):
		// The caller went away (the Dashboard was closed mid-read): that says
		// nothing about the account.
	default:
		m.setFailure(inst.ID, firstLine(err.Error()))
		m.logf("credit: ping %s: %v", inst.Label(), err)
	}
}

// PingDue pings every enabled API-key account whose proof is due (see Due),
// one after the other: nothing waits on them.
func (m *Monitor) PingDue(ctx context.Context) error {
	insts, err := m.List()
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	checks, err := m.Checks()
	if err != nil {
		return err
	}
	now := m.now()
	for _, inst := range insts {
		apiKey, read := m.APIKey(inst)
		c, known := checks[inst.ID]
		if read && !apiKey && known {
			// The account moved to a plan login: its credit no longer
			// matters, and a block remembered from the key must not outlive it.
			m.forget(inst.ID)
			continue
		}
		if !inst.Enabled || !apiKey || m.retryPending(inst.ID, now) {
			continue
		}
		if Due(c, known, now) {
			m.Ping(ctx, inst)
		}
		if ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

// PingAll pings the given API-key accounts at once, regardless of when they
// were last checked: what the Dashboard's Read now asks for.
func (m *Monitor) PingAll(ctx context.Context, insts []provider.Instance) {
	var wg sync.WaitGroup
	for _, inst := range insts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.Ping(ctx, inst)
		}()
	}
	wg.Wait()
}

// Run pings what is due every interval until ctx ends.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	m.loop(ctx, t.C)
}

func (m *Monitor) loop(ctx context.Context, tick <-chan time.Time) {
	for {
		if err := m.PingDue(ctx); err != nil && ctx.Err() == nil {
			m.logf("credit: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick:
		}
	}
}

// retryPending reports a ping of id that failed less than Interval ago.
func (m *Monitor) retryPending(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.failedAt[id]
	return ok && now.Before(at.Add(Interval))
}

func (m *Monitor) setFailure(id, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failedAt == nil {
		m.failedAt, m.failure = map[string]time.Time{}, map[string]string{}
	}
	if reason == "" {
		delete(m.failedAt, id)
		delete(m.failure, id)
		return
	}
	m.failedAt[id], m.failure[id] = m.now(), reason
}

func (m *Monitor) pingLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pinging == nil {
		m.pinging = map[string]*sync.Mutex{}
	}
	l, ok := m.pinging[id]
	if !ok {
		l = &sync.Mutex{}
		m.pinging[id] = l
	}
	return l
}

// firstLine keeps a failed ping's reason to one short line: it is served over
// the API and written to the daemon log, and the CLI's stderr may say more
// about the machine than either needs.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const limit = 160
	if r := []rune(strings.TrimSpace(s)); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return strings.TrimSpace(s)
}
