package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A provider entry written before execution environments existed has no
// [providers.execution] table and must stay exactly that on a rewrite: it is a
// native instance, and nothing migrates.
func TestProviderWithoutExecutionStaysNative(t *testing.T) {
	s := openTemp(t)
	if err := s.UpdateConfig(func(cfg *Config) error {
		cfg.Providers = []Provider{{ID: "claude", Kind: DefaultProviderKind, Enabled: true}}
		cfg.Settings.DefaultProvider = "claude"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Home(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "execution") {
		t.Fatalf("a native provider gained an execution table:\n%s", raw)
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].Execution != nil {
		t.Fatalf("execution = %+v, want nil", cfg.Providers[0].Execution)
	}
}

func TestProviderExecutionRoundTrips(t *testing.T) {
	s := openTemp(t)
	want := ProviderExecution{Type: "wsl", Distro: "Ubuntu-24.04", User: "dm"}
	if err := s.UpdateConfig(func(cfg *Config) error {
		cfg.Providers = []Provider{
			{ID: "claude", Kind: DefaultProviderKind, Enabled: true},
			{ID: "claude-wsl", Kind: DefaultProviderKind, BinaryPath: "/home/dm/.local/bin/claude", Enabled: true, Execution: &want},
		}
		cfg.Settings.DefaultProvider = "claude"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Providers[1].Execution
	if got == nil || *got != want {
		t.Fatalf("execution = %+v, want %+v", got, want)
	}
	if cfg.Providers[0].Execution != nil {
		t.Fatal("the native instance picked up an execution table")
	}
}
