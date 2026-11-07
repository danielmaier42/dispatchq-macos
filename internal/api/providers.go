package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielmaier42/claudeq/internal/app"
	"github.com/danielmaier42/claudeq/internal/provider"
)

// providerView is one configured instance plus what dispatchq currently knows
// about it: whether it can run a job, and whether it is the default. It carries
// no credentials — the provider CLIs own those, and a readiness check never
// reads them into dispatchq. The environment variables an operator set are the
// one exception that lives in dispatchq's file, so only their names go out.
type providerView struct {
	provider.Instance
	// Env names the variables the harness is started with. The values are
	// secrets and are write-only: the app shows the names and keeps the stored
	// value unless a new one is typed.
	Env     []string        `json:"env"`
	Health  provider.Health `json:"health"`
	Default bool            `json:"default"`
	// Detected is the binary dispatchq would use when no path is configured, so
	// the Settings card can offer it instead of asking the operator to find it.
	Detected string `json:"detected,omitempty"`
	// ReasoningEffort reports whether this harness takes a reasoning-effort
	// setting, so the task form can offer the field where it means something
	// instead of the app having to know which kinds those are.
	ReasoningEffort bool `json:"reasoning_effort"`
	// Asides reports whether this harness can answer dispatchq's own questions —
	// prompt review, the feedback assistant — so Settings offers it for those
	// jobs only where it would work.
	Asides bool `json:"asides"`
	// TypeName is what the harness is called ("Claude"), as opposed to the
	// adapter kind it is keyed by ("claude-code").
	TypeName string `json:"type_name"`
	// DefaultConfigDir is where this CLI keeps its configuration when nothing
	// points it elsewhere, for the placeholder of the field that overrides it.
	DefaultConfigDir string `json:"default_config_dir,omitempty"`
}

// providerKind is one adapter this build can run, for the "add a provider" form.
type providerKind struct {
	Kind string `json:"kind"`
	// Name is what the harness is called, which is what the app offers; the
	// kind is what it stores.
	Name string `json:"name"`
	// DefaultConfigDir is where this CLI keeps its configuration when nothing
	// points it elsewhere, for the placeholder of the field that overrides it.
	DefaultConfigDir string `json:"default_config_dir,omitempty"`
}

// listProviderKinds reports the adapters this build supports. The app needs it
// to offer anything but the kind it already has, and it is the registry that
// answers — so a later adapter appears here by being registered, with no change
// on this side.
func (s *server) listProviderKinds(w http.ResponseWriter, _ *http.Request) {
	kinds := s.d.Registry.Kinds()
	out := make([]providerKind, 0, len(kinds))
	for _, k := range kinds {
		ad, err := s.d.Registry.Lookup(k)
		if err != nil {
			continue
		}
		desc := ad.Describe()
		out = append(out, providerKind{Kind: string(k), Name: desc.Name, DefaultConfigDir: desc.DefaultConfigDir})
	}
	writeJSON(w, http.StatusOK, out)
}

// providerSet reads the configured instances, answering the caller with a plain
// error when the configuration cannot be read at all.
func (s *server) providerSet(w http.ResponseWriter) (provider.Set, bool) {
	set, err := app.Providers(s.d.Store)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return provider.Set{}, false
	}
	return set, true
}

// views renders instances with their health. fresh re-probes rather than
// reusing a recent verdict, which is what the Settings card's "Check again"
// asks for.
func (s *server) views(ctx context.Context, set provider.Set, only string, fresh bool) []providerView {
	out := make([]providerView, 0, len(set.All()))
	for _, inst := range set.All() {
		if only != "" && inst.ID != only {
			continue
		}
		h := s.d.Providers.CheckMaybeFresh(ctx, inst, fresh)
		v := providerView{Instance: inst, Env: inst.EnvNames(), Health: h, Default: inst.ID == set.DefaultID()}
		if ad, err := s.d.Registry.Lookup(inst.Kind); err == nil {
			caps, desc := ad.Capabilities(), ad.Describe()
			v.ReasoningEffort, v.Asides = caps.ReasoningEffort, caps.Asides
			v.TypeName, v.DefaultConfigDir = desc.Name, desc.DefaultConfigDir
			// Detection looks at this computer, so it describes a native
			// instance only; a Linux one is found by the dialog's Find.
			if inst.BinaryPath == "" && inst.Execution.IsNative() {
				v.Detected = ad.DetectBinary()
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *server) listProviders(w http.ResponseWriter, r *http.Request) {
	set, ok := s.providerSet(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.views(r.Context(), set, "", false))
}

// checkProvider re-probes one instance and returns its fresh verdict. This is
// the only endpoint that always spends the probe, so the operator gets an
// answer about the state right now rather than one from a minute ago.
func (s *server) checkProvider(w http.ResponseWriter, r *http.Request) {
	s.writeOne(w, r, r.PathValue("id"), http.StatusOK)
}

// writeOne answers with one provider and a verdict probed just now, which is
// what every caller that just changed something wants to see.
func (s *server) writeOne(w http.ResponseWriter, r *http.Request, id string, status int) {
	set, ok := s.providerSet(w)
	if !ok {
		return
	}
	views := s.views(r.Context(), set, id, true)
	if len(views) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%w %q", provider.ErrUnknownProvider, id))
		return
	}
	writeJSON(w, status, views[0])
}

// providerInput is the editable part of an instance. The id comes from the URL
// on an update, and from the body when one is created.
type providerInput struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	BinaryPath   string `json:"binary_path"`
	ConfigDir    string `json:"config_dir"`
	DefaultModel string `json:"default_model"`
	// FallbackProvider is the instance that takes this one's tasks while its
	// allowance is used up. An empty string clears it.
	FallbackProvider string `json:"fallback_provider"`
	// FallbackModel is the model those substituted runs use. Empty leaves the
	// choice to dispatchq.
	FallbackModel string `json:"fallback_model"`
	Enabled       *bool  `json:"enabled"`
	// Env is the complete list of variables the harness is started with. Absent
	// leaves the stored ones alone; a list replaces them, so a variable left out
	// is removed.
	Env *[]envInput `json:"env"`
	// Execution is where the instance runs. Absent is this computer. It is
	// chosen when the instance is created; an update may repeat it but not
	// change it.
	Execution *provider.Execution `json:"execution"`
}

// envInput is one variable of that list. A missing value keeps the one already
// stored under the name, which is how an edit leaves a secret it cannot see
// untouched.
type envInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value"`
}

// envFrom builds the stored variables from the submitted list, taking the
// value of any entry without one from current.
func envFrom(list []envInput, current map[string]string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	env := make(map[string]string, len(list))
	for _, e := range list {
		if _, dup := env[e.Name]; dup {
			return nil, fmt.Errorf("environment variable %s is listed twice", e.Name)
		}
		switch {
		case e.Value != nil:
			env[e.Name] = *e.Value
		default:
			old, ok := current[e.Name]
			if !ok {
				return nil, fmt.Errorf("environment variable %s has no value yet", e.Name)
			}
			env[e.Name] = old
		}
	}
	return env, nil
}

func (s *server) addProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	inst := provider.Instance{
		ID:               in.ID,
		Kind:             provider.Kind(in.Kind),
		Name:             in.Name,
		BinaryPath:       in.BinaryPath,
		ConfigDir:        in.ConfigDir,
		DefaultModel:     in.DefaultModel,
		FallbackProvider: in.FallbackProvider,
		FallbackModel:    in.FallbackModel,
		Enabled:          in.Enabled == nil || *in.Enabled,
	}
	if in.Execution != nil {
		inst.Execution = *in.Execution
	}
	if inst.Name == "" {
		inst.Name = inst.ID
	}
	if in.Env != nil {
		env, err := envFrom(*in.Env, nil)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		inst.Env = env
	}
	if err := app.AddProvider(s.d.Store, s.d.Registry, inst); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.writeOne(w, r, inst.ID, http.StatusCreated)
}

// updateProvider changes one instance. Its kind is fixed: a configured instance
// keeps the harness it was created for, and switching that would silently
// re-point its tasks and sessions at a different CLI.
func (s *server) updateProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	err := app.EditProvider(s.d.Store, s.d.Registry, id, func(inst *provider.Instance) error {
		if in.Kind != "" && provider.Kind(in.Kind) != inst.Kind {
			return errors.New("a provider's kind cannot be changed; remove it and add a new one")
		}
		if in.Execution != nil && *in.Execution != inst.Execution {
			return fmt.Errorf("provider %q runs in %s; that cannot be changed. To run in another environment, add a new provider", inst.ID, inst.Execution.Label())
		}
		inst.Name = in.Name
		if inst.Name == "" {
			inst.Name = inst.ID
		}
		inst.BinaryPath = in.BinaryPath
		inst.ConfigDir = in.ConfigDir
		inst.DefaultModel = in.DefaultModel
		inst.FallbackProvider = in.FallbackProvider
		inst.FallbackModel = in.FallbackModel
		if in.Env != nil {
			env, err := envFrom(*in.Env, inst.Env)
			if err != nil {
				return err
			}
			inst.Env = env
		}
		if in.Enabled != nil {
			inst.Enabled = *in.Enabled
		}
		return nil
	})
	if err != nil {
		writeErr(w, statusForProviderErr(err), err)
		return
	}
	// The configuration changed, so the verdict recorded for the old one no
	// longer describes what would run.
	s.d.Providers.Forget(id)
	s.writeOne(w, r, id, http.StatusOK)
}

func (s *server) enableProvider(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := app.SetProviderEnabled(s.d.Store, id, enabled); err != nil {
			writeErr(w, statusForProviderErr(err), err)
			return
		}
		s.d.Providers.Forget(id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := app.RemoveProvider(s.d.Store, id); err != nil {
		writeErr(w, statusForProviderErr(err), err)
		return
	}
	s.d.Providers.Forget(id)
	w.WriteHeader(http.StatusNoContent)
}

// setDefaultProvider chooses which instance a task runs on when it names none.
func (s *server) setDefaultProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := app.SetDefaultProvider(s.d.Store, id); err != nil {
		writeErr(w, statusForProviderErr(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// statusForProviderErr separates "no such provider" from "that change is not
// allowed", so the dashboard can tell a stale id from a rejected edit.
func statusForProviderErr(err error) int {
	if errors.Is(err, provider.ErrUnknownProvider) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
