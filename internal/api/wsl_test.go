package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// fakeWSL answers the dashboard's WSL questions without a wsl.exe.
type fakeWSL struct{ provider.WSLService }

func (fakeWSL) Available() bool { return true }

func (fakeWSL) Distros(context.Context) ([]provider.WSLDistro, error) {
	return []provider.WSLDistro{{Name: "Ubuntu", Default: true}}, nil
}

func (fakeWSL) Users(context.Context, string) ([]string, error) { return []string{"dm"}, nil }

// The provider dialog's "Runs in: Linux (WSL)" reads these routes. A build
// that lost them (an integration once dropped them from the mux) would answer
// the SPA fallback or 404, and the dialog could not list a distribution.
func TestWSLRoutesAreServed(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	reg, checker := stubProviders()
	reg.SetWSL(fakeWSL{})
	srv := httptest.NewServer(handler(Deps{Store: st, Registry: reg, Providers: checker}))
	t.Cleanup(srv.Close)

	var status wslStatus
	do(t, srv, http.MethodGet, "/api/wsl", nil).into(t, &status)
	if !status.Supported || !status.Available {
		t.Fatalf("GET /api/wsl = %+v, want supported and available", status)
	}
	var distros []provider.WSLDistro
	do(t, srv, http.MethodGet, "/api/wsl/distros", nil).into(t, &distros)
	if len(distros) != 1 || distros[0].Name != "Ubuntu" {
		t.Fatalf("GET /api/wsl/distros = %+v", distros)
	}
	var users []string
	do(t, srv, http.MethodGet, "/api/wsl/users?distro=Ubuntu", nil).into(t, &users)
	if strings.Join(users, ",") != "dm" {
		t.Fatalf("GET /api/wsl/users = %v", users)
	}
}

// Without a WSL service (macOS, Linux) the status says unsupported and the
// other routes refuse instead of starting anything.
func TestWSLRoutesWithoutService(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	var status wslStatus
	do(t, srv, http.MethodGet, "/api/wsl", nil).into(t, &status)
	if status.Supported || status.Available {
		t.Fatalf("GET /api/wsl = %+v, want unsupported", status)
	}
	resp, err := http.Get(srv.URL + "/api/wsl/distros")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/wsl/distros = %d, want 404", resp.StatusCode)
	}
}
