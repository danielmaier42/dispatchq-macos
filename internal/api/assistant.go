package api

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/danielmaier42/claudeq/internal/assistant"
	"github.com/danielmaier42/claudeq/internal/provider"
)

// assistantStatus tells the dashboard whether the assistant chat can run.
type assistantStatus struct {
	Available bool `json:"available"`
	// Reason explains an unavailable chat in one sentence.
	Reason string `json:"reason,omitempty"`
	// Provider names the instance that answers, for the panel's header.
	Provider string `json:"provider,omitempty"`
}

func (s *server) getAssistant(w http.ResponseWriter, _ *http.Request) {
	var st assistantStatus
	inst, _, why := s.assistantTarget("answer")
	switch {
	case s.d.Assistant == nil:
		st.Reason = "the assistant is not available in this build"
	case why != "":
		st.Reason = why
	default:
		st.Available = true
		st.Provider = inst.Label()
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *server) assistantTurn(w http.ResponseWriter, r *http.Request) {
	if s.d.Assistant == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("the assistant is not available"))
		return
	}
	var req assistant.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	inst, model, why := s.assistantTarget("answer")
	if why != "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New(why))
		return
	}
	reply, err := s.d.Assistant.Turn(r.Context(), inst, model, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

// assistantTarget resolves the provider instance and model the assistant runs
// on, for the chat and the feedback page alike: the one Settings names for it,
// otherwise the default provider. job says what it would do there ("draft an
// issue") for the reason a provider cannot.
//
// Both are conversations with a schema, which not every harness can hold (see
// internal/aside). One that cannot is reported as no target, which is what
// makes the dashboard say so up front instead of failing on the first message.
// The reason names the provider, since "Default" in Settings may well be one
// that cannot answer.
func (s *server) assistantTarget(job string) (provider.Instance, string, string) {
	const noProvider = "no provider is set up for the assistant; choose one in Settings"
	cfg, err := s.d.Store.LoadConfig()
	if err != nil {
		return provider.Instance{}, "", noProvider
	}
	set, err := provider.FromConfig(cfg)
	if err != nil {
		return provider.Instance{}, "", noProvider
	}
	res, err := set.Resolve(provider.Selection{ProviderID: cfg.Settings.AssistantProvider})
	if err != nil {
		return provider.Instance{}, "", noProvider
	}
	name := res.Instance.Label()
	if cfg.Settings.AssistantProvider == "" {
		name += ", the default provider,"
	}
	ad, err := s.d.Registry.Lookup(res.Instance.Kind)
	if err != nil || !ad.Capabilities().Asides {
		return provider.Instance{}, "", name + " cannot " + job + "; choose another assistant provider in Settings"
	}
	if ad.ResolveBinary(res.Instance) == "" {
		return provider.Instance{}, "", name + " has no CLI installed; install it or choose another assistant provider in Settings"
	}
	return res.Instance, cfg.Settings.AssistantModel, ""
}

// assistantCLIRequest is a run_cli proposal the operator confirmed.
type assistantCLIRequest struct {
	Argv []string `json:"argv"`
}

// assistantCLIResult is what the command printed.
type assistantCLIResult struct {
	Output string `json:"output"`
}

// assistantCLI runs a dispatchq command the operator confirmed on an assistant
// card. The assistant checks it against the CLI reference again, so only a
// command the reference describes, without credentials, ever runs, and it runs
// as a program with arguments, never through a shell. It carries the same
// guards as uninstall: a page elsewhere in the browser must not be able to
// run commands here.
func (s *server) assistantCLI(w http.ResponseWriter, r *http.Request) {
	if !loopbackHost(r.Host) {
		writeErr(w, http.StatusForbidden, errors.New("commands are only accepted on 127.0.0.1"))
		return
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, errors.New("expected a JSON body"))
		return
	}
	if s.d.Assistant == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("the assistant is not available"))
		return
	}
	var req assistantCLIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.d.Assistant.RunCLI(r.Context(), req.Argv)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusOK, assistantCLIResult{Output: out})
}
