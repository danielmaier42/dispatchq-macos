package aside

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// stubAdapter answers asides with whatever the test set, without running
// anything.
type stubAdapter struct {
	kind   provider.Kind
	caps   provider.Capabilities
	binary string
	cmd    provider.Command
	cmdErr error
	saw    provider.AsideRequest
}

func (a *stubAdapter) Kind() provider.Kind                    { return a.kind }
func (a *stubAdapter) Capabilities() provider.Capabilities    { return a.caps }
func (a *stubAdapter) Describe() provider.Description         { return provider.Description{Name: "Stub"} }
func (a *stubAdapter) DetectBinary() string                   { return a.binary }
func (a *stubAdapter) ResolveBinary(provider.Instance) string { return a.binary }

func (a *stubAdapter) CheckHealth(context.Context, provider.Instance, provider.Host) provider.Health {
	return provider.Health{State: provider.HealthReady}
}

func (a *stubAdapter) ListModels(context.Context, provider.Instance, provider.Host) []provider.Model {
	return nil
}

func (a *stubAdapter) Command(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, nil
}

func (a *stubAdapter) InteractiveResumeCommand(provider.Instance, provider.Request) (provider.Command, error) {
	return provider.Command{}, provider.ErrUnsupported
}

func (a *stubAdapter) NewParser() provider.Parser { return nil }

func (a *stubAdapter) AsideCommand(_ provider.Instance, req provider.AsideRequest) (provider.Command, error) {
	a.saw = req
	return a.cmd, a.cmdErr
}

func (a *stubAdapter) ParseAside(out []byte) (provider.Aside, error) {
	if strings.HasPrefix(string(out), "!") {
		return provider.Aside{}, errors.New("unreadable answer")
	}
	return provider.Aside{Text: string(out)}, nil
}

const stubKind = provider.Kind("stub")

// newRunner returns a runner over one stub adapter and the instance to use.
func newRunner(t *testing.T, a *stubAdapter, out string, execErr error) (*Runner, provider.Instance) {
	t.Helper()
	a.kind = stubKind
	return &Runner{
		Registry: provider.NewRegistry(a),
		NewID:    func() string { return "issued-1" },
		Exec: func(context.Context, string, provider.Command) ([]byte, error) {
			return []byte(out), execErr
		},
	}, provider.Instance{ID: "stub", Kind: stubKind, Name: "Stub", Enabled: true}
}

// TestAskIssuesASessionID: a caller that has no conversation yet gets one, so
// the harness is never asked to invent a name dispatchq then cannot match.
func TestAskIssuesASessionID(t *testing.T) {
	a := &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"}
	r, inst := newRunner(t, a, "answer", nil)

	got, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if a.saw.SessionID != "issued-1" {
		t.Errorf("adapter saw session %q, want the issued one", a.saw.SessionID)
	}
	if got.SessionID != "issued-1" {
		t.Errorf("answer session = %q, want the issued one reported back", got.SessionID)
	}
	if got.Text != "answer" {
		t.Errorf("text = %q", got.Text)
	}
}

// TestAskKeepsTheHarnessesOwnSessionID: a harness that names its own session
// wins, because that is the name the next turn has to use.
func TestAskKeepsTheHarnessesOwnSessionID(t *testing.T) {
	a := &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"}
	r, inst := newRunner(t, a, "thread-7", nil)
	r.Exec = func(context.Context, string, provider.Command) ([]byte, error) { return []byte("thread-7"), nil }
	// The stub parser reports the output as text; wrap it so it reports a session.
	r.Registry = provider.NewRegistry(&sessionNamingAdapter{stubAdapter: a})

	got, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got.SessionID != "thread-7" {
		t.Errorf("session = %q, want the harness's own", got.SessionID)
	}
}

type sessionNamingAdapter struct{ *stubAdapter }

func (a *sessionNamingAdapter) ParseAside(out []byte) (provider.Aside, error) {
	return provider.Aside{SessionID: string(out), Text: string(out)}, nil
}

// TestAskRefusesAHarnessThatCannotAnswer: the three ways a provider is no use
// for an aside all come back as ErrUnavailable, so the caller can tell "this
// cannot work" from "this went wrong".
func TestAskRefusesAHarnessThatCannotAnswer(t *testing.T) {
	tests := []struct {
		name string
		a    *stubAdapter
		inst provider.Instance
	}{
		{
			name: "no such adapter",
			a:    &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"},
			inst: provider.Instance{ID: "other", Kind: provider.Kind("nope")},
		},
		{
			name: "the adapter does not do asides",
			a:    &stubAdapter{binary: "/bin/stub"},
		},
		{
			name: "no CLI to run",
			a:    &stubAdapter{caps: provider.Capabilities{Asides: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, inst := newRunner(t, tc.a, "answer", nil)
			if tc.inst.ID != "" {
				inst = tc.inst
			}
			if _, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
		})
	}
}

// TestAskReportsAnUnsupportedCommandAsUnavailable: an adapter that claims the
// capability but refuses the command is still "cannot", not "went wrong".
func TestAskReportsAnUnsupportedCommandAsUnavailable(t *testing.T) {
	a := &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub", cmdErr: provider.ErrUnsupported}
	r, inst := newRunner(t, a, "", nil)
	if _, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// TestAskPassesOnRealFailures: a CLI that failed, or an answer that could not
// be read, is a problem to report — not a provider to write off.
func TestAskPassesOnRealFailures(t *testing.T) {
	a := &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"}
	r, inst := newRunner(t, a, "", errors.New("exit status 1"))
	_, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"})
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want the failure itself", err)
	}

	r2, inst2 := newRunner(t, &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"}, "!garbage", nil)
	if _, err := r2.Ask(context.Background(), inst2, provider.AsideRequest{Text: "hi"}); err == nil {
		t.Fatal("an unreadable answer must be an error")
	}
}

// TestAskRunsOutsideAnyProject: the directory an aside runs in is empty and its
// own, so a repository's instructions and history cannot reach a question about
// a prompt that merely mentions it.
func TestAskRunsOutsideAnyProject(t *testing.T) {
	a := &stubAdapter{caps: provider.Capabilities{Asides: true}, binary: "/bin/stub"}
	r, inst := newRunner(t, a, "answer", nil)
	var dirs []string
	r.Exec = func(_ context.Context, dir string, _ provider.Command) ([]byte, error) {
		dirs = append(dirs, dir)
		return []byte("answer"), nil
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Ask(context.Background(), inst, provider.AsideRequest{Text: "hi"}); err != nil {
			t.Fatalf("Ask: %v", err)
		}
	}
	if dirs[0] == "" || dirs[0] != dirs[1] {
		t.Fatalf("dirs = %q, want one stable directory", dirs)
	}
	// It is dispatchq's own directory, under the user's temporary one — not the
	// process's cwd, and not anything an operator keeps a project in.
	if !strings.HasPrefix(dirs[0], os.TempDir()) {
		t.Fatalf("asides run in %q, want a throwaway directory under %q", dirs[0], os.TempDir())
	}
	if entries, err := os.ReadDir(dirs[0]); err != nil || len(entries) != 0 {
		t.Fatalf("the aside directory is not empty (%v, %v)", entries, err)
	}
}
