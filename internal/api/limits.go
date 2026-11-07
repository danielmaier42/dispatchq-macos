package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// limitsView is one provider's allowance as the dashboard shows it: which
// account, what kind, and what its windows say.
type limitsView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	TypeName string          `json:"type_name"`
	Default  bool            `json:"default"`
	Limits   provider.Limits `json:"limits"`
	// Urgency is how far behind the provider is on spending its week (see
	// provider.ScoreProvider), with the tier and note behind it. Absent for a
	// provider that is switched off.
	Urgency *provider.MemberScore `json:"urgency,omitempty"`
	// Backfill reports that the urgency is above the backfill threshold, so
	// backfill tasks may run on the provider right now.
	Backfill bool `json:"backfill"`
	// Blocked is the provider's rate-limit gate while it is closed: when it
	// reopens and, for an account block, which one. It is what proves a used-up
	// credit balance, which no reading can show.
	Blocked *blockView `json:"blocked,omitempty"`
	// Credit is the latest proof of an API-key account's credit: a ping or a
	// run that got an answer or was refused. Absent until there is one.
	Credit *creditView `json:"credit,omitempty"`
}

// creditView is a store.CreditCheck as the dashboard shows it, with why the
// latest ping got no answer at all, when it did not. Status is empty when that
// failed ping is all there is.
type creditView struct {
	store.CreditCheck
	PingFailed string `json:"ping_failed,omitempty"`
}

// CreditMonitor is the part of credit.Monitor the API uses.
type CreditMonitor interface {
	Checks() (map[string]store.CreditCheck, error)
	Failure(id string) string
	PingAll(ctx context.Context, insts []provider.Instance)
}

// blockView is a closed rate-limit gate as the dashboard shows it.
type blockView struct {
	Until time.Time `json:"until"`
	// Reason is a provider.Block, empty for an allowance limit.
	Reason string `json:"reason,omitempty"`
}

// listLimits answers with every configured provider's allowance, in
// configuration order. Without ?fresh=1 it is answered from the daemon's last
// readings and costs nothing; with it, readings older than half a minute are
// taken again — what opening the dashboard and View > Refresh ask for. With
// ?ping=1 as well, every enabled account on an API key is pinged first, so its
// credit is proven now: what the Dashboard's Read now button asks for.
func (s *server) listLimits(w http.ResponseWriter, r *http.Request) {
	set, ok := s.providerSet(w)
	if !ok {
		return
	}
	insts := set.All()
	out := make([]limitsView, 0, len(insts))
	if s.d.Limits == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	limits := s.d.Limits.Get(r.Context(), insts, r.URL.Query().Get("fresh") == "1")
	if s.d.Credits != nil && r.URL.Query().Get("ping") == "1" {
		var keyed []provider.Instance
		for i, inst := range insts {
			if inst.Enabled && limits[i].APIKey {
				keyed = append(keyed, inst)
			}
		}
		s.d.Credits.PingAll(r.Context(), keyed)
	}
	var credits map[string]store.CreditCheck
	if s.d.Credits != nil {
		var err error
		if credits, err = s.d.Credits.Checks(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	threshold := cfg.Settings.BackfillUrgencyOrDefault()
	// Readiness counts like it does for a pool: a provider that cannot run is
	// not one backfill work could go to, whatever its allowance says.
	var enabled []provider.Instance
	for _, inst := range insts {
		if inst.Enabled {
			enabled = append(enabled, inst)
		}
	}
	health := make(map[string]provider.Health, len(enabled))
	if s.d.Providers != nil {
		for i, h := range s.d.Providers.CheckEach(r.Context(), enabled) {
			health[enabled[i].ID] = h
		}
	}
	in := s.displayInputs(func(inst provider.Instance) provider.Health {
		if h, ok := health[inst.ID]; ok {
			return h
		}
		return provider.Health{State: provider.HealthReady} // nothing known against it
	})
	var blocked map[string]time.Time
	if s.d.BlockedProviders != nil {
		blocked = s.d.BlockedProviders()
	}
	reasons := s.blockReasons()
	for i, inst := range insts {
		v := limitsView{ID: inst.ID, Name: inst.Label(), Default: inst.ID == set.DefaultID(), Limits: limits[i]}
		if s.d.Credits != nil {
			c, ok := credits[inst.ID]
			// A failed first ping has no proof to go with, but still has to
			// say why the line has nothing to show.
			if failed := s.d.Credits.Failure(inst.ID); ok || failed != "" {
				v.Credit = &creditView{CreditCheck: c, PingFailed: failed}
			}
		}
		if until, ok := blocked[inst.ID]; ok {
			v.Blocked = &blockView{Until: until, Reason: reasons[inst.ID]}
		}
		if ad, err := s.d.Registry.Lookup(inst.Kind); err == nil {
			v.TypeName = ad.Describe().Name
		}
		if inst.Enabled {
			sc := provider.ScoreProvider(inst, in)
			v.Urgency, v.Backfill = &sc, sc.Spare(threshold)
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}
