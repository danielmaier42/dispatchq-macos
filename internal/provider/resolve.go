package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

// DefaultInstanceID is the ID of the Claude Code instance every dispatchq
// configuration has. It is the migration target for the pre-provider settings
// and the default provider for a task that names none.
const DefaultInstanceID = store.DefaultProviderID

// DefaultInstanceName is that instance's display name. It appears in run
// messages, so it reads as the product the operator installed.
const DefaultInstanceName = store.DefaultProviderName

// Errors a selection can fail with. None of them is ever answered by silently
// substituting another provider, account or model.
var (
	// ErrUnknownProvider means no configured instance has that ID.
	ErrUnknownProvider = errors.New("unknown provider")
	// ErrProviderDisabled means the instance exists but is switched off.
	ErrProviderDisabled = errors.New("provider is disabled")
	// ErrNoProviders means nothing is configured to run at all.
	ErrNoProviders = errors.New("no provider is configured")
	// ErrNoSpare means a backfill selection found no provider whose allowance
	// is at risk of going unused. It is a wait, not a fault.
	ErrNoSpare = errors.New("no spare allowance")
)

// Selection is what a task or a queue override asked for. An empty field
// inherits, following the resolution table in Set.Resolve.
type Selection struct {
	// ProviderID names a configured instance. Empty selects the default.
	ProviderID string
	// Model names a model for that provider. Empty selects the provider's own
	// default model.
	Model string
	// PoolID names a pool instead of a provider; the run goes to one of its
	// members (see Set.ResolveAvailable). It excludes ProviderID.
	PoolID string
	// Prefer is the pool member holding the task's interrupted session. It is
	// chosen over the ranking while it can take the work, because only there
	// can the session continue.
	Prefer string
	// Backfill takes only a provider whose allowance is at risk of going
	// unused: urgency above MinUrgency and room in both windows (see
	// MemberScore.Spare). A pool picks among its members that are; a single
	// provider that is not refuses with ErrNoSpare, and never hands the work to
	// its fallback, whose allowance is not the one at risk.
	Backfill   bool
	MinUrgency float64
}

// Resolved is the effective execution identity for one run.
type Resolved struct {
	// Instance is the provider instance that will run the job.
	Instance Instance
	// Model is the effective model; empty means the harness's own default.
	Model string
	// FallbackFrom is the instance the selection actually named, set only when
	// its allowance was used up and its fallback took the job. It is what the
	// run's log says happened; an empty value means nothing was substituted.
	FallbackFrom Instance
	// Pool is set when the selection named a pool: which one, and how its
	// members stood when this one was chosen.
	Pool *PoolChoice
}

// Substituted reports whether the run is going somewhere other than the
// provider it named.
func (r Resolved) Substituted() bool { return r.FallbackFrom.ID != "" }

// Set is the configured provider instances plus which of them is the default.
type Set struct {
	instances []Instance
	defaultID string
	pools     []Pool
}

// NewSet builds a set. It rejects an instance list that could not be resolved
// unambiguously: a missing or duplicate ID, or a default that names no member.
func NewSet(defaultID string, instances []Instance) (Set, error) {
	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if inst.ID == "" {
			return Set{}, fmt.Errorf("provider set: instance with empty id")
		}
		if _, dup := seen[inst.ID]; dup {
			return Set{}, fmt.Errorf("provider set: duplicate provider id %q", inst.ID)
		}
		seen[inst.ID] = struct{}{}
	}
	if defaultID != "" {
		if _, ok := seen[defaultID]; !ok {
			return Set{}, fmt.Errorf("provider set: default provider %q is not configured", defaultID)
		}
	} else if len(instances) > 0 {
		// No default named: the first configured instance is it. Leaving the
		// default empty would make every task that names no provider fail, which
		// is a worse answer than the only one there can be.
		defaultID = instances[0].ID
	}
	out := make([]Instance, len(instances))
	copy(out, instances)
	return Set{instances: out, defaultID: defaultID}, nil
}

// FromConfig reads the configured provider instances out of a stored
// configuration. The store seeds and migrates the entries (see
// store.Config.migrate), so every configuration it hands out already has the
// Claude Code instance; a Config assembled without it is rejected rather than
// silently given an invented provider.
func FromConfig(cfg store.Config) (Set, error) {
	if len(cfg.Providers) == 0 {
		return Set{}, ErrNoProviders
	}
	insts := make([]Instance, len(cfg.Providers))
	for i, p := range cfg.Providers {
		insts[i] = InstanceOf(p)
	}
	set, err := NewSet(cfg.Settings.DefaultProvider, insts)
	if err != nil {
		return Set{}, err
	}
	pools := make([]Pool, len(cfg.Pools))
	for i, p := range cfg.Pools {
		pools[i] = PoolOf(p)
	}
	return set.WithPools(pools), nil
}

// WithPools returns the set with these pools configured.
func (s Set) WithPools(pools []Pool) Set {
	s.pools = append([]Pool(nil), pools...)
	return s
}

// Pools returns the configured pools in order.
func (s Set) Pools() []Pool { return append([]Pool(nil), s.pools...) }

// LookupPool returns the pool with the given ID.
func (s Set) LookupPool(id string) (Pool, bool) {
	for _, p := range s.pools {
		if p.ID == id {
			return p, true
		}
	}
	return Pool{}, false
}

// PoolMembers returns the configured instances of a pool, in its order.
func (s Set) PoolMembers(p Pool) []Instance {
	out := make([]Instance, 0, len(p.Members))
	for _, m := range p.Members {
		if inst, ok := s.Lookup(m.ProviderID); ok {
			out = append(out, inst)
		}
	}
	return out
}

// All returns the configured instances in order.
func (s Set) All() []Instance {
	out := make([]Instance, len(s.instances))
	copy(out, s.instances)
	return out
}

// DefaultID returns the ID of the default provider, or "" when none is set.
func (s Set) DefaultID() string { return s.defaultID }

// Lookup returns the instance with the given ID.
func (s Set) Lookup(id string) (Instance, bool) {
	for _, inst := range s.instances {
		if inst.ID == id {
			return inst, true
		}
	}
	return Instance{}, false
}

// Resolve applies the resolution table:
//
//	neither given        default provider, that provider's default model
//	only a model given   default provider, the given model
//	only a provider      that provider, that provider's default model
//	both given           that provider, the given model
//
// Because the model default always comes from the resolved instance, changing
// the provider without naming a model can never carry a model from the old
// provider into the new one. There is no fallback to another provider: a
// selection that cannot be honoured returns an error saying why.
func (s Set) Resolve(sel Selection) (Resolved, error) {
	if sel.PoolID != "" {
		return s.resolvePool(sel, Availability{})
	}
	id := sel.ProviderID
	if id == "" {
		id = s.defaultID
	}
	if id == "" {
		return Resolved{}, ErrNoProviders
	}
	inst, ok := s.Lookup(id)
	if !ok {
		return Resolved{}, fmt.Errorf("%w %q", ErrUnknownProvider, id)
	}
	if !inst.Enabled {
		return Resolved{}, fmt.Errorf("%w: %q", ErrProviderDisabled, id)
	}
	model := sel.Model
	if model == "" {
		model = inst.DefaultModel
	}
	return Resolved{Instance: inst, Model: model}, nil
}

// Availability is what the fallback walk needs to know about the configured
// instances. Its zero value asks nothing, which makes [Set.ResolveAvailable]
// exactly [Set.Resolve].
type Availability struct {
	// OutOfAllowance reports that an instance is waiting out a rate limit. It is
	// the one condition that hands an instance's work to a fallback: everything
	// else about a provider is answered by refusing, never by substituting.
	OutOfAllowance func(id string) bool
	// CanTakeWork reports whether an instance could run the work at all — it is
	// installed, logged in, switched on. It is asked about the *hops* only: a
	// hop that cannot take the work is stepped over, which substitutes nothing,
	// because it is not the provider the task named. Nil accepts every hop.
	CanTakeWork func(inst Instance) bool

	// Limits, Running and Now are what a pool ranks its members by (see
	// RankPool). Nil Limits ranks every member as unread; nil Now is the zero
	// time, which only matters to a member with a reading.
	Limits  func(inst Instance) (Limits, bool)
	Running func(id string) int
	Now     func() time.Time
}

// ResolveAvailable resolves a selection and then, when the chosen instance is
// out of allowance, follows the fallback it was given.
//
// The chain is walked until an instance is found that has allowance left and
// can actually take the work; a hop that is blocked, switched off, unfit or
// read as used up (see readUsedUp) is stepped over, and a chain that is broken or blocked end to end resolves to
// the provider the selection named. The caller still sees the account whose
// allowance ran out rather than an error about a fallback nobody asked about.
//
// This is the one substitution dispatchq makes, and only this one: a rate limit
// is a pause, not a verdict on the work. A provider that is missing, logged
// out or switched off is still never answered by running somewhere else.
func (s Set) ResolveAvailable(sel Selection, av Availability) (Resolved, error) {
	if sel.PoolID != "" {
		return s.resolvePool(sel, av)
	}
	res, err := s.Resolve(sel)
	if err == nil && sel.Backfill {
		return res, av.checkSpare(res.Instance, sel.MinUrgency)
	}
	if err != nil || av.OutOfAllowance == nil || !av.OutOfAllowance(res.Instance.ID) {
		return res, err
	}
	origin := res.Instance
	seen := map[string]struct{}{origin.ID: {}}
	for cur := origin; cur.FallbackProvider != ""; {
		next, ok := s.Lookup(cur.FallbackProvider)
		if _, visited := seen[cur.FallbackProvider]; !ok || visited {
			break
		}
		seen[next.ID] = struct{}{}
		if av.usable(next) && !av.OutOfAllowance(next.ID) && !av.readUsedUp(next) {
			return Resolved{Instance: next, Model: fallbackModel(origin, cur, next, sel.Model), FallbackFrom: origin}, nil
		}
		cur = next
	}
	return res, nil
}

// rankInputs is what ranking needs from an Availability: the readings, the
// runs in flight and the time, and which instances can take work at all.
func (a Availability) rankInputs() RankInputs {
	in := RankInputs{Limits: a.Limits, Running: a.Running}
	if a.Now != nil {
		in.Now = a.Now()
	}
	in.Usable = func(inst Instance) (bool, string) {
		switch {
		case a.OutOfAllowance != nil && a.OutOfAllowance(inst.ID):
			return false, "waiting for its rate limit"
		case !a.usable(inst):
			return false, "cannot run right now"
		}
		return true, ""
	}
	return in
}

// checkSpare refuses a backfill run on inst unless its allowance is at risk
// of going unused, saying why in the words the queue shows.
func (a Availability) checkSpare(inst Instance, threshold float64) error {
	sc := ScoreProvider(inst, a.rankInputs())
	if sc.Spare(threshold) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrNoSpare, spareNote(sc, threshold))
}

// spareNote says why a provider is not taking backfill work.
func spareNote(sc MemberScore, threshold float64) string {
	if sc.Tier > 0 {
		return fmt.Sprintf("%s takes no backfill work (%s)", sc.Name, sc.Note)
	}
	return fmt.Sprintf("%s is at urgency %.1f, backfill runs above %.1f", sc.Name, sc.Urgency, threshold)
}

// readUsedUp reports whether a hop's last allowance reading shows a window
// that is used up and has not reset yet. Such a hop has no rate-limit block of
// its own only because nothing has run on it since (a block lives in memory,
// a reading survives a restart); handing it the work would spend a run on
// being refused. Without a reading, or with a window whose reset is unknown or
// already past, the hop is taken: only a reading that says so steps it over.
func (a Availability) readUsedUp(inst Instance) bool {
	if a.Limits == nil {
		return false
	}
	l, ok := a.Limits(inst)
	if !ok {
		return false
	}
	var now time.Time
	if a.Now != nil {
		now = a.Now()
	}
	for _, w := range l.Windows {
		if w.UsedPercent >= poolLongWindowFull && w.ResetsAt != nil && w.ResetsAt.After(now) {
			return true
		}
	}
	return false
}

// usable answers CanTakeWork for one hop, with the two things every caller
// means by it: the instance is switched on, and whatever the caller knows about
// its readiness says it could run.
func (a Availability) usable(inst Instance) bool {
	if !inst.Enabled {
		return false
	}
	return a.CanTakeWork == nil || a.CanTakeWork(inst)
}

// fallbackModel decides which model the substitute runs. The provider that
// handed the work over has the first word: whoever configured "when my limit is
// reached, go to Codex" may well know which model should answer there, and a
// named one is not second-guessed.
//
// Without that, a model name means something to one harness only: it travels to
// another account of the same kind — a second Claude subscription still runs the
// Opus the task asked for — and is dropped for a different harness, which falls
// back to that instance's own default rather than being handed a name it does
// not know.
func fallbackModel(origin, from, next Instance, want string) string {
	if from.FallbackModel != "" {
		return from.FallbackModel
	}
	if want != "" && next.Kind == origin.Kind {
		return want
	}
	return next.DefaultModel
}

// resolvePool picks the member of a pool a run goes to.
//
// The member holding the task's interrupted session is taken first while it
// can work, so the session continues. Otherwise the best-ranked member that can
// take work wins (see RankPool). A pool none of whose members can take work
// resolves to one that is waiting out its rate limit, if any is — the caller
// then holds the task back until one reopens — and otherwise to its first
// enabled member, whose readiness check says what is wrong. A pool's members do
// not hand work to their own fallbacks: the pool is the fallback.
func (s Set) resolvePool(sel Selection, av Availability) (Resolved, error) {
	p, ok := s.LookupPool(sel.PoolID)
	if !ok {
		return Resolved{}, fmt.Errorf("%w %q", ErrUnknownPool, sel.PoolID)
	}
	members := s.PoolMembers(p)
	var enabled []Instance
	for _, inst := range members {
		if inst.Enabled {
			enabled = append(enabled, inst)
		}
	}
	if len(enabled) == 0 {
		return Resolved{}, fmt.Errorf("%w: every member of pool %q is switched off", ErrProviderDisabled, p.ID)
	}
	model := func(inst Instance) string {
		if sel.Model != "" {
			return sel.Model
		}
		return inst.DefaultModel
	}
	out := func(inst Instance, c PoolChoice) Resolved {
		c.Pool = p
		return Resolved{Instance: inst, Model: model(inst), Pool: &c}
	}
	blocked := func(inst Instance) bool { return av.OutOfAllowance != nil && av.OutOfAllowance(inst.ID) }
	in := av.rankInputs()
	if sel.Backfill {
		// Only the members whose allowance is at risk take backfill work; the
		// rest rank as unable to, saying why.
		base := in
		in.Usable = func(inst Instance) (bool, string) {
			if sc := ScoreProvider(inst, base); !sc.Spare(sel.MinUrgency) {
				return false, spareNote(sc, sel.MinUrgency)
			}
			return true, ""
		}
	}
	for _, inst := range enabled {
		if inst.ID != sel.Prefer || blocked(inst) || !av.usable(inst) {
			continue
		}
		if ok, _ := in.Usable(inst); ok {
			return out(inst, PoolChoice{Resumed: true}), nil
		}
	}
	scores := RankPool(s, p, in)
	if len(scores) > 0 && scores[0].Tier < 3 {
		inst, _ := s.Lookup(scores[0].ProviderID)
		return out(inst, PoolChoice{Scores: scores}), nil
	}
	if sel.Backfill {
		notes := make([]string, 0, len(scores))
		for _, sc := range scores {
			notes = append(notes, sc.Note)
		}
		return Resolved{}, fmt.Errorf("%w: pool %s: %s", ErrNoSpare, p.Label(), strings.Join(notes, "; "))
	}
	for _, inst := range enabled {
		if blocked(inst) {
			return out(inst, PoolChoice{Scores: scores}), nil
		}
	}
	return out(enabled[0], PoolChoice{Scores: scores}), nil
}
