package api

import (
	"context"
	"net/http"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// stubAdapter stands in for a real harness: it resolves the binary a provider
// instance names and reports whatever readiness the test asked for. No CLI is
// ever run, so the tests say the same thing on a machine with Claude Code
// installed and on one without.
type stubAdapter struct {
	health provider.Health
	caps   provider.Capabilities
}

func (stubAdapter) Kind() provider.Kind                   { return provider.KindClaudeCode }
func (s stubAdapter) Capabilities() provider.Capabilities { return s.caps }

func (stubAdapter) Describe() provider.Description {
	return provider.Description{Name: "Claude", DefaultConfigDir: "~/.claude"}
}
func (stubAdapter) DetectBinary() string { return "" }

func (stubAdapter) ResolveBinary(inst provider.Instance) string { return inst.BinaryPath }

func (s stubAdapter) CheckHealth(context.Context, provider.Instance, provider.Host) provider.Health {
	return s.health
}

func (stubAdapter) ListModels(context.Context, provider.Instance, provider.Host) []provider.Model {
	return []provider.Model{{ID: "sonnet", Label: "Sonnet (latest)"}}
}

func (stubAdapter) InteractiveResumeCommand(inst provider.Instance, req provider.Request) (provider.Command, error) {
	args := []string{"--resume", req.SessionID}
	if req.AccessMode.OrDefault() == provider.AccessFullAccess {
		args = append(args, "--dangerously-skip-permissions")
	}
	return provider.Command{Path: inst.BinaryPath, Args: args}, nil
}

func (stubAdapter) Command(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}
func (stubAdapter) NewParser() provider.Parser { return nil }

func (stubAdapter) AsideCommand(provider.Instance, provider.AsideRequest) (provider.Command, error) {
	return provider.Command{}, provider.ErrUnsupported
}

func (stubAdapter) ParseAside([]byte) (provider.Aside, error) {
	return provider.Aside{}, provider.ErrUnsupported
}

// stubProviders is the ready-harness default the tests run against.
func stubProviders() (*provider.Registry, *provider.Checker) {
	reg := provider.NewRegistry(stubAdapter{
		health: provider.Health{State: provider.HealthReady},
		caps:   provider.Capabilities{InteractiveResume: true, Asides: true},
	})
	return reg, provider.NewChecker(reg)
}

// handler builds the API handler for a test, filling in the provider
// dependencies every request path needs unless the test supplied its own.
func handler(d Deps) http.Handler {
	if d.Registry == nil || d.Providers == nil {
		reg, checker := stubProviders()
		if d.Registry == nil {
			d.Registry = reg
		}
		if d.Providers == nil {
			d.Providers = checker
		}
	}
	return Handler(d)
}
