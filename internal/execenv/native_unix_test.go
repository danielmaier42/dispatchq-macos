//go:build unix

package execenv

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielmaier42/claudeq/internal/aside"
	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/adapters"
	"github.com/danielmaier42/claudeq/internal/task"
)

// With the WSL service installed, as on Windows, nothing a native instance
// does starts wsl.exe: readiness, models, a run, an aside, the folder
// questions. The fake wsl.exe records every start; the log must stay empty.
func TestNativeInstancesNeverStartWSL(t *testing.T) {
	f := newFixture(t)
	reg := adapters.Default()
	reg.SetWSL(f.svc)
	cli := filepath.Join(f.tmp, "native-claude")
	write(t, cli, `#!/bin/sh
case "$1" in
--version) echo "2.1.0 (Claude Code)";;
auth) echo '{"loggedIn":true,"authMethod":"claude.ai"}';;
*) cat >/dev/null; echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}';;
esac
`, 0o755)
	cfg := filepath.Join(f.tmp, "native-config")
	if err := os.Mkdir(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	inst := provider.Instance{ID: "native", Kind: provider.KindClaudeCode, BinaryPath: cli, ConfigDir: cfg, Enabled: true}
	ctx := context.Background()

	h, err := reg.HostFor(inst)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.(provider.NativeHost); !ok {
		t.Fatalf("native instance got host %T", h)
	}
	if got := provider.NewChecker(reg).CheckFresh(ctx, inst); !got.Ready() {
		t.Logf("health %+v (readiness itself is not under test)", got)
	}
	ad, _ := reg.Lookup(inst.Kind)
	_ = ad.ListModels(ctx, inst, h)
	var log bytes.Buffer
	ex := &executor.Executor{Registry: reg, Home: filepath.Join(f.tmp, "data"), QueueBin: "/usr/local/bin/dispatchq"}
	if _, err := ex.Run(ctx, executor.Request{
		Task: task.Task{ID: "t1", Prompt: "hello", WorkingDir: f.tmp}, Provider: inst, Log: &log, SessionID: "11111111-1111-4111-8111-111111111111",
	}); err != nil {
		t.Fatalf("native run: %v\n%s", err, log.String())
	}
	_, _ = (&aside.Runner{Registry: reg, Dir: filepath.Join(f.tmp, "aside")}).Ask(ctx, inst, provider.AsideRequest{Text: "q"})
	_ = h.StatDir(ctx, cfg)
	_, _ = h.WorkDir(ctx, f.tmp)

	if got := f.argv(t); len(got) != 0 {
		t.Fatalf("a native instance started wsl.exe: %q", got)
	}
	// The same fixture does start wsl.exe for a WSL instance, so an empty log
	// above is not a fixture that cannot record.
	if _, err := f.svc.Host(ubuntu).Probe(ctx, sh("true")); err != nil {
		t.Fatal(err)
	}
	if len(f.argv(t)) == 0 {
		t.Fatal("the recording fake did not record a WSL start")
	}
}
