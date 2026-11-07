package credit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// fakeAsk answers each account the way the table says: nil for an answer,
// otherwise the error. It counts the pings per account.
type fakeAsk struct {
	mu      sync.Mutex
	answers map[string]error
	pings   map[string]int
}

func (f *fakeAsk) Ask(_ context.Context, inst provider.Instance, req provider.AsideRequest) (provider.Aside, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Model != PingModel || req.Text != PingText {
		return provider.Aside{}, errors.New("not a ping")
	}
	if f.pings == nil {
		f.pings = map[string]int{}
	}
	f.pings[inst.ID]++
	return provider.Aside{Text: "pong"}, f.answers[inst.ID]
}

func (f *fakeAsk) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings[id]
}

// gates records what the monitor did to each provider's gate.
type gates struct {
	mu     sync.Mutex
	closed map[string]provider.Block
}

func (g *gates) block(id string, b provider.Block) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed[id] = b
}

func (g *gates) lift(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.closed, id)
}

func (g *gates) get(id string) (provider.Block, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.closed[id]
	return b, ok
}

type fixture struct {
	m     *Monitor
	ask   *fakeAsk
	gates *gates
	now   *time.Time
	st    *store.Store
}

var refused = &provider.AccountBlockedError{Block: provider.BlockCreditExhausted, Detail: "Credit balance is too low"}

// newFixture monitors three accounts: two on an API key, one on a plan.
func newFixture(t *testing.T, st *store.Store, answers map[string]error) fixture {
	t.Helper()
	if st == nil {
		var err error
		if st, err = store.Open(t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	f := fixture{ask: &fakeAsk{answers: answers}, gates: &gates{closed: map[string]provider.Block{}}, now: &now, st: st}
	insts := []provider.Instance{
		{ID: "api0", Kind: "claude-code", Enabled: true},
		{ID: "api1", Kind: "claude-code", Enabled: true},
		{ID: "plan", Kind: "claude-code", Enabled: true},
	}
	f.m = &Monitor{
		Store:  st,
		List:   func() ([]provider.Instance, error) { return insts, nil },
		APIKey: func(inst provider.Instance) (bool, bool) { return inst.ID != "plan", true },
		Ask:    f.ask,
		Block:  f.gates.block,
		Lift:   f.gates.lift,
		Now:    func() time.Time { return *f.now },
	}
	return f
}

func TestPingsOnlyAPIKeysAndOnlyOnceADay(t *testing.T) {
	f := newFixture(t, nil, map[string]error{"api0": refused})
	ctx := context.Background()

	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if f.ask.count("api0") != 1 || f.ask.count("api1") != 1 || f.ask.count("plan") != 0 {
		t.Fatalf("pings = %v, want one each for the API keys and none for the plan", f.ask.pings)
	}
	if b, ok := f.gates.get("api0"); !ok || b != provider.BlockCreditExhausted {
		t.Fatalf("api0 gate = %q/%v, want closed for credit", b, ok)
	}
	if _, ok := f.gates.get("api1"); ok {
		t.Fatal("api1 answered, its gate must stay open")
	}

	// Neither the refused nor the answering account is pinged again within the day.
	*f.now = f.now.Add(23 * time.Hour)
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if f.ask.count("api0") != 1 || f.ask.count("api1") != 1 {
		t.Fatalf("pings after 23h = %v, want no second ping", f.ask.pings)
	}

	// A day after the last proof both are due, and a topped-up account reopens.
	f.ask.answers["api0"] = nil
	*f.now = f.now.Add(time.Hour)
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if f.ask.count("api0") != 2 || f.ask.count("api1") != 2 {
		t.Fatalf("pings after 24h = %v, want a second ping each", f.ask.pings)
	}
	if _, ok := f.gates.get("api0"); ok {
		t.Fatal("api0 answered the daily ping, its gate must reopen")
	}
}

func TestReadNowPingsAtOnceAndUnlocks(t *testing.T) {
	f := newFixture(t, nil, map[string]error{"api0": refused})
	ctx := context.Background()
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	// Credit added ten minutes later: Read now pings regardless of the day.
	f.ask.answers["api0"] = nil
	*f.now = f.now.Add(10 * time.Minute)
	f.m.PingAll(ctx, []provider.Instance{{ID: "api0", Kind: "claude-code", Enabled: true}})
	if f.ask.count("api0") != 2 {
		t.Fatalf("api0 pings = %d, want Read now to ping again", f.ask.count("api0"))
	}
	if _, ok := f.gates.get("api0"); ok {
		t.Fatal("api0 answered Read now's ping, its gate must reopen")
	}
	checks, err := f.m.Checks()
	if err != nil {
		t.Fatal(err)
	}
	if c := checks["api0"]; c.Status != StatusOK || c.Source != SourcePing || !c.CheckedAt.Equal(*f.now) {
		t.Fatalf("api0 check = %+v, want ok by ping now", c)
	}
}

func TestARefusalOutlivesARestart(t *testing.T) {
	f := newFixture(t, nil, map[string]error{"api0": refused})
	if err := f.m.PingDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A new daemon on the same state file closes the gate before anything runs.
	g := newFixture(t, f.st, nil)
	if err := g.m.Restore(); err != nil {
		t.Fatal(err)
	}
	if b, ok := g.gates.get("api0"); !ok || b != provider.BlockCreditExhausted {
		t.Fatalf("api0 gate after restart = %q/%v, want closed for credit", b, ok)
	}
	if _, ok := g.gates.get("api1"); ok {
		t.Fatal("api1 had answered; a restart must not close it")
	}
}

func TestRunsProveCreditOfKnownAPIKeysOnly(t *testing.T) {
	f := newFixture(t, nil, nil)
	f.m.RunSeen("plan", "", true)
	if checks, _ := f.m.Checks(); len(checks) != 0 {
		t.Fatalf("checks = %v, want none for an account no ping knows", checks)
	}
	if err := f.m.PingDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Hour)
	f.m.RunSeen("api1", provider.BlockCreditExhausted, false)
	if b, ok := f.gates.get("api1"); !ok || b != provider.BlockCreditExhausted {
		t.Fatalf("api1 gate = %q/%v, want closed after a refused run", b, ok)
	}
	checks, _ := f.m.Checks()
	if c := checks["api1"]; c.Status != string(provider.BlockCreditExhausted) || c.Source != SourceRun {
		t.Fatalf("api1 check = %+v, want refused by a run", c)
	}
}

func TestAFailedPingProvesNothingAndWaitsADay(t *testing.T) {
	f := newFixture(t, nil, map[string]error{"api0": errors.New("exec: claude: not found")})
	ctx := context.Background()
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.gates.get("api0"); ok {
		t.Fatal("a ping that got no answer must not close the gate")
	}
	if got := f.m.Failure("api0"); got != "exec: claude: not found" {
		t.Fatalf("failure = %q", got)
	}
	if checks, _ := f.m.Checks(); func() bool { _, ok := checks["api0"]; return ok }() {
		t.Fatal("a failed ping must not be recorded as a proof")
	}
	*f.now = f.now.Add(time.Hour)
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if f.ask.count("api0") != 1 {
		t.Fatalf("api0 pings = %d, want the failed ping retried only the next day", f.ask.count("api0"))
	}
}

func TestAMoveToAPlanForgetsTheBlock(t *testing.T) {
	f := newFixture(t, nil, map[string]error{"api0": refused})
	ctx := context.Background()
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	f.m.APIKey = func(provider.Instance) (bool, bool) { return false, true }
	if err := f.m.PingDue(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.gates.get("api0"); ok {
		t.Fatal("api0 logs in with a plan now; the key's credit block must lift")
	}
	if checks, _ := f.m.Checks(); len(checks) != 0 {
		t.Fatalf("checks = %v, want none left", checks)
	}
}
