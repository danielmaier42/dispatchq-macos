package api

import (
	"net/http"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
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
// taken again — what opening the dashboard and View > Refresh ask for.
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
