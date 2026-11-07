package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// The provider dialog asks about WSL only after the operator picks "Linux
// (WSL)". GET /api/wsl answers whether that choice exists at all without
// starting anything; every other endpoint here starts wsl.exe, and nothing
// else in the dashboard calls them.

// wslStatus says whether this build can run providers in WSL.
type wslStatus struct {
	// Supported is DispatchQ for Windows.
	Supported bool `json:"supported"`
	// Available is wsl.exe present. It may still have no distribution.
	Available bool `json:"available"`
}

func (s *server) wslService(w http.ResponseWriter) (provider.WSLService, bool) {
	svc := s.d.Registry.WSL()
	if svc == nil {
		writeErr(w, http.StatusNotFound, provider.ErrWSLUnsupported)
		return nil, false
	}
	return svc, true
}

func (s *server) wslStatus(w http.ResponseWriter, _ *http.Request) {
	svc := s.d.Registry.WSL()
	writeJSON(w, http.StatusOK, wslStatus{Supported: svc != nil, Available: svc != nil && svc.Available()})
}

func (s *server) wslDistros(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.wslService(w)
	if !ok {
		return
	}
	list, err := svc.Distros(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) wslUsers(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.wslService(w)
	if !ok {
		return
	}
	distro := r.URL.Query().Get("distro")
	if err := provider.CheckDistroName(distro); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	users, err := svc.Users(r.Context(), distro)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if users == nil {
		users = []string{}
	}
	writeJSON(w, http.StatusOK, users)
}

// wslDetectInput names the environment and the harness to look for.
type wslDetectInput struct {
	Distro string `json:"distro"`
	User   string `json:"user"`
	Kind   string `json:"kind"`
}

func (s *server) wslDetect(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.wslService(w)
	if !ok {
		return
	}
	var in wslDetectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e := provider.Execution{Type: provider.ExecWSL, Distro: in.Distro, User: in.User}
	if err := provider.CheckExecution(s.d.Registry, e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.d.Registry.Lookup(provider.Kind(in.Kind)); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("unknown provider kind"))
		return
	}
	found, err := svc.Detect(r.Context(), e, provider.Kind(in.Kind))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}
