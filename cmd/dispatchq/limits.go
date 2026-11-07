package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/adapters"
	"github.com/danielmaier42/claudeq/internal/store"
)

// newProviderRegistry is the adapter set `provider limits` reads through; a
// variable so tests can put a harness with a scripted allowance in its place.
var newProviderRegistry = adapters.Default

// limitsView is what `provider limits --json` prints per provider: the shape
// the dashboard's /api/limits answers with, minus the daemon's rate-limit gate
// (`blocked`), which only the running daemon knows.
type limitsView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	TypeName string          `json:"type_name"`
	Default  bool            `json:"default"`
	Limits   provider.Limits `json:"limits"`
	// Urgency and Backfill are what the Dashboard shows next to the windows:
	// how far behind the provider is on spending its week, and whether that
	// is above the backfill threshold. Absent for a provider switched off.
	Urgency  *provider.MemberScore `json:"urgency,omitempty"`
	Backfill bool                  `json:"backfill"`
	// Credit is the daemon's latest proof of an API-key account's credit (a
	// ping or a run), as the state file records it. Absent until there is one.
	Credit *store.CreditCheck `json:"credit,omitempty"`
}

// cmdProviderLimits reads every provider's allowance (or one provider's) right
// now, straight from the harnesses — the same readings the daemon takes, and
// like them it costs no usage.
func cmdProviderLimits(st *store.Store, args []string) error {
	var only string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		only, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("provider limits", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the limits as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The id may come after the flags too (`--json codex`); anything beyond one
	// id is a mistake, not something to ignore.
	if rest := fs.Args(); len(rest) > 0 {
		if only != "" || len(rest) > 1 {
			return fmt.Errorf("expected at most one provider id, got %q", strings.Join(append([]string{only}, rest...), " "))
		}
		only = rest[0]
	}
	set, err := app.Providers(st)
	if err != nil {
		return err
	}
	var insts []provider.Instance
	for _, inst := range set.All() {
		if only == "" || inst.ID == only {
			insts = append(insts, inst)
		}
	}
	if only != "" && len(insts) == 0 {
		return fmt.Errorf("%w %q", provider.ErrUnknownProvider, only)
	}

	reg := newProviderRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), providerCheckTimeout)
	defer cancel()
	limits := provider.NewLimitMonitor(reg, nil).Get(ctx, insts, true)
	cfg, err := st.LoadConfig()
	if err != nil {
		return err
	}
	threshold := cfg.Settings.BackfillUrgencyOrDefault()
	byID := make(map[string]provider.Limits, len(insts))
	for i, inst := range insts {
		byID[inst.ID] = limits[i]
	}
	// Read from here, nothing is known about runs in flight or rate-limit
	// gates: the urgency is the allowance's alone.
	in := provider.DisplayInputs(time.Now(),
		func(inst provider.Instance) (provider.Limits, bool) {
			l, ok := byID[inst.ID]
			return l, ok && len(l.Windows) > 0
		},
		func(provider.Instance) provider.Health { return provider.Health{State: provider.HealthReady} }, nil)
	state, err := st.LoadState()
	if err != nil {
		return err
	}
	views := make([]limitsView, len(insts))
	for i, inst := range insts {
		views[i] = limitsView{ID: inst.ID, Name: inst.Label(), Default: inst.ID == set.DefaultID(), Limits: limits[i]}
		if c, ok := state.CreditChecks[inst.ID]; ok && limits[i].APIKey {
			views[i].Credit = &c
		}
		if ad, err := reg.Lookup(inst.Kind); err == nil {
			views[i].TypeName = ad.Describe().Name
		}
		if inst.Enabled {
			sc := provider.ScoreProvider(inst, in)
			views[i].Urgency, views[i].Backfill = &sc, sc.Spare(threshold)
		}
	}
	if *asJSON {
		return printJSON(views)
	}
	if err := printLimits(views, time.Now()); err != nil {
		return err
	}
	fmt.Printf("\nBackfill tasks run above urgency %.1f.\n%s\n", threshold, urgencyNote)
	return nil
}

// urgencyCell is a provider's urgency as the limits table shows it, marked
// when backfill tasks may run on it.
func urgencyCell(v limitsView) string {
	if v.Urgency == nil || v.Urgency.Tier > 0 {
		return "-"
	}
	s := strconv.FormatFloat(v.Urgency.Urgency, 'f', 1, 64)
	if v.Backfill {
		s += " backfill"
	}
	return s
}

// printLimits writes one line per window, and one line for a provider that has
// none to show, with the reason in the last column.
func printLimits(views []limitsView, now time.Time) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWINDOW\tUSED\tRESETS\tURGENCY\tNOTE")
	for _, v := range views {
		l := v.Limits
		note := ""
		switch l.State {
		case provider.LimitsDisabled:
			note = "switched off"
		case provider.LimitsUnsupported:
			note = l.Reason
			if v.Credit != nil {
				note = creditNote(*v.Credit)
			}
		case provider.LimitsUnavailable:
			note = l.Reason
			if !l.UpdatedAt.IsZero() {
				note += " (figures from " + l.UpdatedAt.Local().Format("Mon 15:04") + ")"
			}
		}
		if len(l.Windows) == 0 {
			fmt.Fprintf(w, "%s\t-\t-\t-\t-\t%s\n", v.ID, note)
			continue
		}
		// The urgency is the provider's, so it is printed once, on its first row.
		urgency := urgencyCell(v)
		for _, win := range l.Windows {
			fmt.Fprintf(w, "%s\t%s\t%.0f%%\t%s\t%s\t%s\n", v.ID, win.Label, win.UsedPercent, resetsText(win.ResetsAt, now), urgency, note)
			urgency = ""
		}
	}
	return w.Flush()
}

// creditNote is an API-key account's latest credit proof as the limits table
// shows it, in place of the unreadable balance.
func creditNote(c store.CreditCheck) string {
	by := "by a ping"
	if c.Source == "run" {
		by = "by a run"
	}
	when := c.CheckedAt.Local().Format("Mon 15:04")
	switch provider.Block(c.Status) {
	case provider.BlockCreditExhausted:
		return "No credit left (refused " + by + ", " + when + ")"
	case provider.BlockBillingRefused:
		return "Billing refused (" + by + ", " + when + ")"
	}
	return "Credit OK (answered " + by + ", " + when + ")"
}

// resetsText is when a window starts over: the local day and time, and how far
// off that is.
func resetsText(at *time.Time, now time.Time) string {
	if at == nil {
		return "-"
	}
	d := at.Sub(now)
	if d <= 0 {
		return "now"
	}
	return at.Local().Format("Mon 15:04") + " (in " + shortDuration(d) + ")"
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", max(1, int(d.Minutes())))
	}
}
