package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider/binpath"
	"github.com/danielmaier42/claudeq/internal/system"
)

// HealthState is what a readiness check concluded about one instance. It says
// whether the instance can run a job at all — not how a job went. A rate limit
// is execution state and is recorded on the run, never here.
type HealthState string

const (
	// HealthReady means a job may be started on this instance.
	HealthReady HealthState = "ready"
	// HealthNotInstalled means the harness CLI could not be found or is not
	// executable.
	HealthNotInstalled HealthState = "not_installed"
	// HealthNotAuthenticated means the CLI is there but nobody is logged in.
	HealthNotAuthenticated HealthState = "not_authenticated"
	// HealthInvalidConfiguration means the instance's own settings cannot be
	// used — an unreachable configuration directory, say.
	HealthInvalidConfiguration HealthState = "invalid_configuration"
	// HealthDisabled means the operator switched the instance off.
	HealthDisabled HealthState = "disabled"
	// HealthCheckFailed means the check itself could not reach a verdict, so
	// dispatchq does not know either way — and therefore does not start a job.
	HealthCheckFailed HealthState = "check_failed"
)

// Health is one readiness verdict for one instance.
type Health struct {
	// State is the verdict.
	State HealthState `json:"state"`
	// Reason is a short sentence naming what is wrong and, where there is one,
	// what fixes it. Empty when the instance is ready.
	Reason string `json:"reason,omitempty"`
	// Binary is the executable the check resolved, when it found one.
	Binary string `json:"binary,omitempty"`
	// Detail is extra non-secret context the operator may want (the CLI version,
	// the account's login method). Never credentials or their contents.
	Detail string `json:"detail,omitempty"`
	// CheckedAt is when the verdict was reached.
	CheckedAt time.Time `json:"checked_at"`
}

// Ready reports whether a job may start on the checked instance. Anything but
// a clean verdict says no: dispatchq does not launch a job on a harness it could
// not confirm.
func (h Health) Ready() bool { return h.State == HealthReady }

// KnownUnready reports whether the instance is known not to work, as opposed to
// merely not confirmed. It is the weaker test, used when deciding whether to
// *file* work rather than start it: a check that could not reach a verdict is
// no reason to throw away a follow-up task a run just asked for, and the
// scheduler still refuses to start it until the provider answers cleanly.
func (h Health) KnownUnready() bool {
	return h.State != HealthReady && h.State != HealthCheckFailed
}

// ReasonOr is the verdict's reason, or fallback when it carries none.
func (h Health) ReasonOr(fallback string) string {
	if strings.TrimSpace(h.Reason) != "" {
		return h.Reason
	}
	return fallback
}

// Prober runs a provider's own command for a readiness check and returns its
// combined output. It is an interface so tests drive adapters with recorded
// output instead of an installed CLI.
type Prober interface {
	Probe(ctx context.Context, c Command) ([]byte, error)
}

// ProbeTimeout bounds a single readiness probe, and CheckTimeout the whole
// check. A CLI that has to be asked twice must not keep the scheduler waiting,
// and the dashboard must not sit on a spinner while it does.
const (
	ProbeTimeout = 10 * time.Second
	CheckTimeout = 20 * time.Second
)

// ExecProber runs the probe as a real process: the binary directly, never
// through a shell, with the adapter's environment additions appended to the
// daemon's own.
type ExecProber struct{}

// Probe implements Prober.
func (ExecProber) Probe(ctx context.Context, c Command) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	cmd, err := binpath.CommandContext(ctx, c.Path, c.Args...) // a configured binary path, no shell
	if err != nil {
		return nil, err
	}
	system.NoWindow(cmd)
	if len(c.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.Env...)
	}
	// The probe owns its process tree, so the timeout stops anything it
	// started; one that escaped the tree and holds the output pipe cannot hold
	// the caller past the timeout either (see runOwned).
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = runOwned(ctx, cmd)
	return out.Bytes(), err
}

// DefaultHealthTTL is how long a verdict is reused. It is short because a
// readiness problem is usually fixed while the operator watches the Settings
// card, and long enough that a five-second scheduler tick does not spawn CLI
// processes on every pass.
const DefaultHealthTTL = 30 * time.Second

// Checker answers "can this instance run a job right now?" and caches the
// answer briefly. The cache key includes the instance's settings, so editing a
// path or a configuration directory invalidates the verdict by itself rather
// than leaving the app reporting a state that belongs to the old configuration.
//
// A Checker is safe for concurrent use; the daemon shares one between the
// scheduler and the API so the dashboard's polling costs no extra processes.
type Checker struct {
	// Registry resolves an instance's kind to the adapter that checks it.
	Registry *Registry
	// Prober runs the CLI probes of native instances. Nil means the registry's
	// host for the instance ([NativeHost] for a native one).
	Prober Prober
	// Now reads the clock. Nil means time.Now.
	Now func() time.Time
	// TTL overrides [DefaultHealthTTL].
	TTL time.Duration

	mu     sync.Mutex
	cached map[string]cachedHealth
}

type cachedHealth struct {
	fingerprint string
	health      Health
	at          time.Time
}

// NewChecker returns a checker for the adapters in reg.
func NewChecker(reg *Registry) *Checker { return &Checker{Registry: reg} }

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// host is the environment an instance's checks run in. A test Prober stands
// in for the daemon's own operating system only; an instance that runs
// elsewhere is always checked where it runs.
func (c *Checker) host(inst Instance) (Host, error) {
	if c.Prober != nil && inst.Execution.IsNative() {
		return NativeHost{Prober: c.Prober}, nil
	}
	return c.Registry.HostFor(inst)
}

func (c *Checker) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultHealthTTL
}

// fingerprint is everything about an instance that can change its verdict.
func fingerprint(inst Instance) string {
	// The variables hold secrets, so only a digest of them is kept as the key.
	env := sha256.Sum256([]byte(strings.Join(inst.EnvPairs(), "\x00")))
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t\x00%x\x00%s", inst.Kind, inst.BinaryPath, inst.ConfigDir, inst.Enabled, env, inst.Execution.key())
}

// Check returns the instance's readiness, reusing a recent verdict for the same
// configuration.
func (c *Checker) Check(ctx context.Context, inst Instance) Health {
	fp := fingerprint(inst)
	now := c.now()
	c.mu.Lock()
	entry, ok := c.cached[inst.ID]
	c.mu.Unlock()
	if ok && entry.fingerprint == fp && now.Sub(entry.at) < c.ttl() {
		return entry.health
	}
	return c.CheckFresh(ctx, inst)
}

// CheckFresh probes the instance now, ignoring (and replacing) any cached
// verdict. The scheduler uses it for the instance it is about to launch on, so
// a start is never recorded against a stale answer.
//
// A probe that was cut short — the caller went away, the daemon is shutting
// down — is not a verdict and is not remembered. It says nothing about the
// provider, and caching it would show a passing blip as a provider problem for
// as long as the verdict lives.
func (c *Checker) CheckFresh(ctx context.Context, inst Instance) Health {
	h := c.probe(ctx, inst)
	if ctx.Err() != nil {
		return c.lastKnown(inst, h)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached == nil {
		c.cached = map[string]cachedHealth{}
	}
	c.cached[inst.ID] = cachedHealth{fingerprint: fingerprint(inst), health: h, at: h.CheckedAt}
	return h
}

// lastKnown returns the verdict this instance last got under its current
// configuration, falling back to what the interrupted probe produced when there
// is none to fall back to.
func (c *Checker) lastKnown(inst Instance, fallback Health) Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.cached[inst.ID]; ok && entry.fingerprint == fingerprint(inst) {
		return entry.health
	}
	return fallback
}

// Forget drops the cached verdict for an instance id.
func (c *Checker) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cached, id)
}

func (c *Checker) probe(ctx context.Context, inst Instance) Health {
	now := c.now()
	if !inst.Enabled {
		return Health{
			State:     HealthDisabled,
			Reason:    inst.Label() + " is switched off (Settings → Providers).",
			CheckedAt: now,
		}
	}
	ad, err := c.Registry.Lookup(inst.Kind)
	if err != nil {
		return Health{
			State:     HealthInvalidConfiguration,
			Reason:    fmt.Sprintf("unknown provider kind %q", inst.Kind),
			CheckedAt: now,
		}
	}
	// One deadline for the whole check, so two slow probes cannot add up to
	// twice the wait a caller was promised.
	host, err := c.host(inst)
	if err != nil {
		return Health{State: HealthInvalidConfiguration, Reason: err.Error(), CheckedAt: now}
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	h := ad.CheckHealth(ctx, inst, host)
	if h.CheckedAt.IsZero() {
		h.CheckedAt = now
	}
	return h
}

// Status pairs a configured instance with its readiness, which is what every
// caller that lists providers actually wants.
type Status struct {
	Instance Instance `json:"instance"`
	Health   Health   `json:"health"`
	// Default marks the instance a task runs on when it names none.
	Default bool `json:"default"`
}

// CheckMaybeFresh is Check, or CheckFresh when fresh — the form every caller
// that has a "probe it now" switch wants, so none of them computes a verdict
// only to throw it away.
func (c *Checker) CheckMaybeFresh(ctx context.Context, inst Instance, fresh bool) Health {
	if fresh {
		return c.CheckFresh(ctx, inst)
	}
	return c.Check(ctx, inst)
}

// Statuses checks every instance in set, and answers in configuration order.
func (c *Checker) Statuses(ctx context.Context, set Set) []Status {
	insts := set.All()
	health := c.CheckEach(ctx, insts)
	out := make([]Status, len(insts))
	for i, inst := range insts {
		out[i] = Status{
			Instance: inst,
			Health:   health[i],
			Default:  inst.ID == set.DefaultID(),
		}
	}
	return out
}

// CheckEach returns the verdict for every instance in insts, positionally.
//
// The instances are checked at the same time, because a check that is not
// cached spawns a CLI and waits for it: asking four harnesses one after the
// other makes whoever is waiting — the dashboard's first request, the daemon's
// own startup report — wait for the sum instead of the slowest.
func (c *Checker) CheckEach(ctx context.Context, insts []Instance) []Health {
	out := make([]Health, len(insts))
	var wg sync.WaitGroup
	for i, inst := range insts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = c.Check(ctx, inst)
		}()
	}
	wg.Wait()
	return out
}
