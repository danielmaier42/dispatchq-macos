package provider

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

var poolNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// reading is a Claude-style limit reading: the 5-hour window and the week.
func reading(capacity, fiveHour, week float64, weekResetsIn time.Duration) Limits {
	reset := poolNow.Add(weekResetsIn)
	short := poolNow.Add(2 * time.Hour)
	return Limits{State: LimitsOK, Capacity: capacity, Windows: []LimitWindow{
		{ID: "five_hour", Label: "5 hours", UsedPercent: fiveHour, ResetsAt: &short},
		{ID: "week", Label: "Week", UsedPercent: week, ResetsAt: &reset},
	}}
}

// poolSet is two Claude accounts — a Max 20x and a Team seat on Max 5x — in
// one pool, the configuration the feature was asked for.
func poolSet(t *testing.T) (Set, Pool) {
	t.Helper()
	set, err := NewSet("max", []Instance{
		{ID: "max", Kind: KindClaudeCode, Name: "Claude Max", Enabled: true},
		{ID: "team", Kind: KindClaudeCode, Name: "Claude Team", Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := Pool{ID: "claude-pool", Name: "Claude", Members: []PoolMember{{ProviderID: "max"}, {ProviderID: "team"}}}
	return set.WithPools([]Pool{p}), p
}

func limitsOf(m map[string]Limits) func(Instance) (Limits, bool) {
	return func(inst Instance) (Limits, bool) {
		l, ok := m[inst.ID]
		return l, ok
	}
}

func first(scores []MemberScore) string { return scores[0].ProviderID }

func TestRankPool(t *testing.T) {
	set, p := poolSet(t)
	cases := []struct {
		name    string
		limits  map[string]Limits
		running map[string]int
		want    string
	}{
		{
			// The weekly allowance that expires soonest is used first: 10% of
			// the week resetting in four hours beats 60% for five more days.
			name: "soon-expiring allowance first",
			limits: map[string]Limits{
				"max":  reading(20, 10, 40, 5*24*time.Hour),
				"team": reading(5, 10, 90, 4*time.Hour),
			},
			want: "team",
		},
		{
			// The same free share and the same reset: the larger plan has more
			// to lose, so it goes first.
			name: "plan size weighs",
			limits: map[string]Limits{
				"max":  reading(20, 10, 50, 3*24*time.Hour),
				"team": reading(5, 10, 50, 3*24*time.Hour),
			},
			want: "max",
		},
		{
			// Weighted: 30% × 20 over 48 h beats 20% × 5 over 10 h.
			name: "weighted urgency",
			limits: map[string]Limits{
				"max":  reading(20, 10, 70, 48*time.Hour),
				"team": reading(5, 10, 80, 10*time.Hour),
			},
			want: "max",
		},
		{
			name: "a nearly full 5-hour window steps back",
			limits: map[string]Limits{
				"max":  reading(20, 95, 10, 2*24*time.Hour),
				"team": reading(5, 10, 80, 6*24*time.Hour),
			},
			want: "team",
		},
		{
			name: "a used-up week steps back",
			limits: map[string]Limits{
				"max":  reading(20, 0, 100, 2*24*time.Hour),
				"team": reading(5, 0, 99, 6*24*time.Hour),
			},
			want: "team",
		},
		{
			// Runs already on a member share its urgency, so two starts in one
			// tick do not pile onto the same account.
			name: "running jobs share the urgency",
			limits: map[string]Limits{
				"max":  reading(20, 10, 50, 3*24*time.Hour),
				"team": reading(5, 10, 50, 3*24*time.Hour),
			},
			running: map[string]int{"max": 4},
			want:    "team",
		},
		{
			name: "a member with a reading beats one without",
			limits: map[string]Limits{
				"team": reading(5, 10, 95, 6*24*time.Hour),
			},
			want: "team",
		},
		{
			name:   "nothing read: fewer running jobs, then configuration order",
			limits: map[string]Limits{},
			want:   "max",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scores := RankPool(set, p, RankInputs{
				Now:     poolNow,
				Limits:  limitsOf(tc.limits),
				Running: func(id string) int { return tc.running[id] },
			})
			if got := first(scores); got != tc.want {
				t.Fatalf("first = %s, want %s (%+v)", got, tc.want, scores)
			}
		})
	}
}

func TestRankPoolOperatorWeightWins(t *testing.T) {
	set, p := poolSet(t)
	p.Members[1].Weight = 100 // the operator knows better than the plan
	scores := RankPool(set, p, RankInputs{Now: poolNow, Limits: limitsOf(map[string]Limits{
		"max":  reading(20, 10, 50, 3*24*time.Hour),
		"team": reading(5, 10, 50, 3*24*time.Hour),
	})})
	if first(scores) != "team" || scores[0].Weight != 100 {
		t.Fatalf("scores = %+v, want the team member at weight 100 first", scores)
	}
}

func TestResolvePool(t *testing.T) {
	set, _ := poolSet(t)
	limits := limitsOf(map[string]Limits{
		"max":  reading(20, 10, 40, 5*24*time.Hour),
		"team": reading(5, 10, 90, 4*time.Hour),
	})
	av := func(blocked ...string) Availability {
		return Availability{
			OutOfAllowance: func(id string) bool {
				for _, b := range blocked {
					if b == id {
						return true
					}
				}
				return false
			},
			Limits: limits,
			Now:    func() time.Time { return poolNow },
		}
	}

	res, err := set.ResolveAvailable(Selection{PoolID: "claude-pool", Model: "opus"}, av())
	if err != nil {
		t.Fatalf("ResolveAvailable: %v", err)
	}
	if res.Instance.ID != "team" || res.Model != "opus" || res.Pool == nil || res.Pool.Resumed {
		t.Fatalf("resolved = %+v, want the ranked team member with the task's model", res)
	}
	if note := res.Pool.Note(res.Instance); !strings.Contains(note, "Pool Claude chose Claude Team") || !strings.Contains(note, "10% of week left") {
		t.Fatalf("note = %q", note)
	}

	// The member holding the interrupted session wins while it can work…
	res, _ = set.ResolveAvailable(Selection{PoolID: "claude-pool", Prefer: "max"}, av())
	if res.Instance.ID != "max" || !res.Pool.Resumed {
		t.Fatalf("resolved = %+v, want the session's member", res)
	}
	// …and not while it waits out its limit: the run starts over elsewhere.
	res, _ = set.ResolveAvailable(Selection{PoolID: "claude-pool", Prefer: "max"}, av("max"))
	if res.Instance.ID != "team" || res.Pool.Resumed {
		t.Fatalf("resolved = %+v, want the other member", res)
	}
	// Every member blocked: a blocked one, so the caller holds the task back.
	res, _ = set.ResolveAvailable(Selection{PoolID: "claude-pool"}, av("max", "team"))
	if res.Instance.ID != "max" {
		t.Fatalf("resolved = %+v, want a blocked member", res)
	}

	if _, err := set.ResolveAvailable(Selection{PoolID: "nope"}, av()); !errors.Is(err, ErrUnknownPool) {
		t.Fatalf("err = %v, want ErrUnknownPool", err)
	}
}

func TestResolvePoolWithEveryMemberSwitchedOff(t *testing.T) {
	set, err := NewSet("a", []Instance{{ID: "a", Kind: KindClaudeCode}, {ID: "b", Kind: KindClaudeCode}})
	if err != nil {
		t.Fatal(err)
	}
	set = set.WithPools([]Pool{{ID: "p", Members: []PoolMember{{ProviderID: "a"}, {ProviderID: "b"}}}})
	if _, err := set.Resolve(Selection{PoolID: "p"}); !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("err = %v, want ErrProviderDisabled", err)
	}
}

func TestValidatePool(t *testing.T) {
	set, err := NewSet("max", []Instance{
		{ID: "max", Kind: KindClaudeCode, Enabled: true},
		{ID: "team", Kind: KindClaudeCode, Enabled: true},
		{ID: "codex", Kind: KindCodex, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := func(ids ...string) []PoolMember {
		out := make([]PoolMember, len(ids))
		for i, id := range ids {
			out[i] = PoolMember{ProviderID: id}
		}
		return out
	}
	cases := []struct {
		name string
		pool Pool
		want string
	}{
		{"ok", Pool{ID: "pool", Members: m("max", "team")}, ""},
		{"bad id", Pool{ID: "a b", Members: m("max")}, "may only contain"},
		{"provider id", Pool{ID: "max", Members: m("team")}, "a provider already has this id"},
		{"empty", Pool{ID: "pool"}, "at least one member"},
		{"unknown member", Pool{ID: "pool", Members: m("max", "x")}, "unknown provider"},
		{"twice", Pool{ID: "pool", Members: m("max", "max")}, "member twice"},
		{"mixed kinds", Pool{ID: "pool", Members: m("max", "codex")}, "one type"},
		{"negative weight", Pool{ID: "pool", Members: []PoolMember{{ProviderID: "max", Weight: -1}}}, "zero (automatic) or more"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePool(set, tc.pool)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, ErrInvalidPool) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// A reading taken before a window reset says nothing after it: the member is
// ranked as the fresh account it now is, not as the full one it was.
func TestRankPoolTreatsAPassedResetAsFresh(t *testing.T) {
	set, p := poolSet(t)
	stale := reading(5, 95, 99.8, -time.Minute) // week "used up", but it reset a minute ago
	stale.Windows[0].ResetsAt = func() *time.Time { at := poolNow.Add(-time.Hour); return &at }()
	scores := RankPool(set, p, RankInputs{Now: poolNow, Limits: limitsOf(map[string]Limits{
		"max":  reading(20, 95, 60, 3*24*time.Hour), // 5 hours nearly full: steps back
		"team": stale,
	})})
	if first(scores) != "team" || scores[0].Tier != 0 || scores[0].WeekFree != 100 ||
		!strings.Contains(scores[0].Note, "has reset") {
		t.Fatalf("scores = %+v, want the reset member first as fresh", scores)
	}
}

// TestScoreProvider pins what urgency means: 1 is on pace to spend what is
// left by the reset, 2 needs twice that pace, and runs in flight share it.
func TestScoreProvider(t *testing.T) {
	inst := Instance{ID: "max", Kind: KindClaudeCode, Name: "Claude Max", Enabled: true}
	cases := []struct {
		name    string
		limits  Limits
		read    bool
		running int
		want    float64
		tier    int
	}{
		{"half left, half the week to go", reading(20, 0, 50, 84*time.Hour), true, 0, 1, 0},
		{"half left, a quarter of the week to go", reading(20, 0, 50, 42*time.Hour), true, 0, 2, 0},
		{"plan size does not count", reading(1, 0, 50, 42*time.Hour), true, 0, 2, 0},
		{"a run in flight shares it", reading(20, 0, 50, 42*time.Hour), true, 1, 1, 0},
		{"a full 5-hour window", reading(20, 95, 50, 42*time.Hour), true, 0, 2, 2},
		{"no reading", Limits{}, false, 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreProvider(inst, RankInputs{
				Now:     poolNow,
				Limits:  func(Instance) (Limits, bool) { return tc.limits, tc.read },
				Running: func(string) int { return tc.running },
			})
			if got.Tier != tc.tier || math.Abs(got.Urgency-tc.want) > 1e-9 {
				t.Fatalf("tier %d urgency %v, want tier %d urgency %v", got.Tier, got.Urgency, tc.tier, tc.want)
			}
		})
	}
}

// TestUrgencyOfOtherWindowLengths: a window of another length is measured
// against its own length, so a day or a month on pace is at 1 too.
func TestUrgencyOfOtherWindowLengths(t *testing.T) {
	inst := Instance{ID: "codex", Kind: KindCodex, Name: "Codex", Enabled: true}
	for _, tc := range []struct {
		id    string
		hours float64
	}{{"window_1440", 24}, {"window_43200", 720}} {
		reset := poolNow.Add(time.Duration(tc.hours/2) * time.Hour)
		l := Limits{State: LimitsOK, Windows: []LimitWindow{{ID: tc.id, Label: tc.id, UsedPercent: 50, ResetsAt: &reset}}}
		got := ScoreProvider(inst, RankInputs{Now: poolNow, Limits: func(Instance) (Limits, bool) { return l, true }})
		if math.Abs(got.Urgency-1) > 1e-9 {
			t.Errorf("%s half used half way: urgency %v, want 1", tc.id, got.Urgency)
		}
	}
}

func TestSpare(t *testing.T) {
	for _, tc := range []struct {
		s    MemberScore
		want bool
	}{
		{MemberScore{Tier: 0, Urgency: 2}, true},
		{MemberScore{Tier: 0, Urgency: 1.5}, false}, // above, not at
		{MemberScore{Tier: 2, Urgency: 9}, false},   // its 5-hour window is full
	} {
		if got := tc.s.Spare(1.5); got != tc.want {
			t.Errorf("%+v.Spare(1.5) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestResolveBackfill: a backfill selection takes a provider only while its
// allowance is at risk, never its fallback, and in a pool only such members.
func TestResolveBackfill(t *testing.T) {
	set, p := poolSet(t)
	maxInst, _ := set.Lookup("max")
	maxInst.FallbackProvider = "team"
	set, err := NewSet("max", []Instance{maxInst, func() Instance { i, _ := set.Lookup("team"); return i }()})
	if err != nil {
		t.Fatal(err)
	}
	set = set.WithPools([]Pool{p})
	atRisk := reading(1, 0, 50, 24*time.Hour) // 3.5
	onPace := reading(1, 0, 50, 6*24*time.Hour)
	av := func(limits map[string]Limits, blocked string) Availability {
		return Availability{
			OutOfAllowance: func(id string) bool { return id == blocked },
			Limits:         limitsOf(limits),
			Now:            func() time.Time { return poolNow },
		}
	}
	cases := []struct {
		name    string
		sel     Selection
		limits  map[string]Limits
		blocked string
		want    string
		errHas  string
	}{
		{"provider at risk", Selection{ProviderID: "max"}, map[string]Limits{"max": atRisk}, "", "max", ""},
		{"provider on pace", Selection{ProviderID: "max"}, map[string]Limits{"max": onPace}, "", "", "Claude Max is at urgency 0.6, backfill runs above 1.5"},
		{"limited provider, no fallback", Selection{ProviderID: "max"}, map[string]Limits{"max": atRisk, "team": atRisk}, "max", "", "waiting for its rate limit"},
		{"pool member at risk", Selection{PoolID: p.ID}, map[string]Limits{"max": onPace, "team": atRisk}, "", "team", ""},
		{"pool session waits on a member on pace", Selection{PoolID: p.ID, Prefer: "max"}, map[string]Limits{"max": onPace, "team": atRisk}, "", "team", ""},
		{"pool on pace", Selection{PoolID: p.ID}, map[string]Limits{"max": onPace, "team": onPace}, "", "", "pool Claude: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.sel.Backfill, tc.sel.MinUrgency = true, 1.5
			res, err := set.ResolveAvailable(tc.sel, av(tc.limits, tc.blocked))
			if tc.errHas != "" {
				if !errors.Is(err, ErrNoSpare) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want ErrNoSpare with %q", err, tc.errHas)
				}
				return
			}
			if err != nil || res.Instance.ID != tc.want {
				t.Fatalf("resolved %q (%v), want %q", res.Instance.ID, err, tc.want)
			}
		})
	}
}

// A member or fallback that cannot open the task's folder is stepped over,
// whatever its rank, and a pool member that holds the session is not resumed
// there. The note says why.
func TestUnreachableFolderStepsOverMembersAndFallbacks(t *testing.T) {
	set, _ := poolSet(t)
	limits := limitsOf(map[string]Limits{
		"max":  reading(20, 10, 40, 5*24*time.Hour),
		"team": reading(5, 10, 90, 4*time.Hour), // ranks first when reachable
	})
	unreachable := errors.New("other distribution")
	av := Availability{
		Limits:  limits,
		Now:     func() time.Time { return poolNow },
		Reaches: func(inst Instance) error { return map[string]error{"team": unreachable}[inst.ID] },
	}
	for _, sel := range []Selection{{PoolID: "claude-pool"}, {PoolID: "claude-pool", Prefer: "team"}, {PoolID: "claude-pool", Backfill: true}} {
		res, err := set.ResolveAvailable(sel, av)
		if sel.Backfill {
			if err == nil && res.Instance.ID == "team" {
				t.Fatalf("backfill went to the unreachable member")
			}
			continue
		}
		if err != nil || res.Instance.ID != "max" {
			t.Fatalf("%+v: resolved %+v, %v; want max", sel, res, err)
		}
	}
	scores := RankPool(set, set.Pools()[0], av.rankInputs())
	for _, sc := range scores {
		if sc.ProviderID == "team" && !strings.Contains(sc.Note, "can't reach this task's folder") {
			t.Errorf("team note = %q", sc.Note)
		}
	}

	// A rate-limit fallback into an environment that cannot reach the folder is
	// not taken; the task waits on its own provider instead.
	fb, err := NewSet("a", []Instance{
		{ID: "a", Kind: KindClaudeCode, Enabled: true, FallbackProvider: "b"},
		{ID: "b", Kind: KindClaudeCode, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	av = Availability{
		OutOfAllowance: func(id string) bool { return id == "a" },
		Reaches:        func(inst Instance) error { return map[string]error{"b": unreachable}[inst.ID] },
	}
	if res, _ := fb.ResolveAvailable(Selection{ProviderID: "a"}, av); res.Instance.ID != "a" || res.Substituted() {
		t.Fatalf("fallback crossed into an unreachable environment: %+v", res)
	}
	av.Reaches = nil
	if res, _ := fb.ResolveAvailable(Selection{ProviderID: "a"}, av); res.Instance.ID != "b" {
		t.Fatalf("reachable fallback not taken: %+v", res)
	}
}
