//go:build unix

package aside

import (
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/procown/owntest"
	"github.com/danielmaier42/claudeq/internal/provider"
)

// TestAskCancelStopsTheHarnessTree runs a real aside through the native host:
// the stand-in harness starts a tool that holds its output and then hangs. A
// cancelled assistant or prompt-review request must return promptly and leave
// no tool running.
func TestAskCancelStopsTheHarnessTree(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := owntest.Script(t, "harness", owntest.Spawn(pidFile, true)+"\nwait")
	a := &stubAdapter{kind: stubKind, caps: provider.Capabilities{Asides: true}, binary: bin, cmd: provider.Command{Path: bin}}
	r := &Runner{Registry: provider.NewRegistry(a), NewID: func() string { return "issued-1" }, Dir: t.TempDir()}
	inst := provider.Instance{ID: "stub", Kind: stubKind, Name: "Stub", Enabled: true}

	ctx, cancel := owntest.CancelWhenRecorded(pidFile, 10*time.Second) // the tool runs: now the user cancels
	defer cancel()
	start := time.Now()
	err := owntest.Within(t, 15*time.Second, func() error {
		_, err := r.Ask(ctx, inst, provider.AsideRequest{Text: "hello"})
		return err
	})
	if err == nil {
		t.Fatal("a cancelled aside reported an answer")
	}
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("the cancelled aside returned only after %s", took)
	}
	if child := owntest.PID(t, pidFile); !owntest.Gone(child) {
		t.Fatalf("tool %d survived the cancelled aside", child)
	}
}
