package claudecode

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

func apiKeyInstance() provider.Instance {
	inst := instance()
	inst.ConfigDir = "/tmp/claude-api"
	inst.Env = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-secret"}
	return inst
}

// TestEnvironmentTravelsWithEveryCommand: the key is what makes an instance an
// API-billed account, so every process that touches the account carries it —
// the run, the readiness probe, the aside and the terminal resume.
func TestEnvironmentTravelsWithEveryCommand(t *testing.T) {
	inst := apiKeyInstance()
	a := New()
	run, err := a.Command(inst, provider.Request{Prompt: "p", SessionID: "S"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	resume, err := a.InteractiveResumeCommand(inst, provider.Request{SessionID: "S"})
	if err != nil {
		t.Fatalf("InteractiveResumeCommand: %v", err)
	}
	aside, err := a.AsideCommand(inst, provider.AsideRequest{Text: "t", SessionID: "S"})
	if err != nil {
		t.Fatalf("AsideCommand: %v", err)
	}
	probe := a.probeCommand(inst, "/bin/claude", "--version")
	for name, cmd := range map[string]provider.Command{"run": run, "resume": resume, "aside": aside, "probe": probe} {
		for _, want := range []string{ConfigDirEnv + "=/tmp/claude-api", "ANTHROPIC_API_KEY=sk-ant-secret"} {
			if !slices.Contains(cmd.Env, want) {
				t.Errorf("%s env = %v, want it to contain %q", name, cmd.Env, "ANTHROPIC_API_KEY=… and the config dir")
				break
			}
		}
	}
}

func TestAnAPIKeyInstanceHasNoPlanLimits(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	// A plan login sits in the keychain, but the key wins in the CLI, so its
	// windows would describe an allowance the instance does not spend.
	a := limitsAdapter(t, map[string][]byte{
		keychainService("/tmp/claude-api"): creds("plan-token", now.Add(time.Hour)),
	}, now, func(http.ResponseWriter, *http.Request) { t.Error("the usage endpoint must not be asked") })
	_, err := a.ReadLimits(context.Background(), apiKeyInstance())
	if !errors.Is(err, provider.ErrNoPlanLimits) {
		t.Fatalf("err = %v, want ErrNoPlanLimits", err)
	}
}
