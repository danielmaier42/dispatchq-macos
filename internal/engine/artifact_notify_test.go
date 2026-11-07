package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/clock"
	"github.com/danielmaier42/claudeq/internal/store"
)

// publish appends an artifact to the store's list, the way `dispatchq publish`
// does from inside a run.
func publish(t *testing.T, st *store.Store, a store.Artifact) {
	t.Helper()
	if err := st.UpdateArtifacts(func(list *[]store.Artifact) error {
		*list = append(*list, a)
		return nil
	}); err != nil {
		t.Fatalf("UpdateArtifacts: %v", err)
	}
}

func artifact(id, title, taskName string) store.Artifact {
	return store.Artifact{
		ID: id, Title: title, FileName: "report.html", RelPath: id + "/report.html",
		ContentType: "text/html", TaskName: taskName, TaskID: "t1", RunID: "r1",
		PublishedAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
	}
}

// newArtifactEngine builds an engine with a capturing notifier, plus its store.
func newArtifactEngine(t *testing.T) (*Engine, *store.Store, *capturingNotifier) {
	t.Helper()
	e, st := newTestEngine(t, &stub{}, clock.NewFake(time.Now()))
	n := &capturingNotifier{}
	e.SetNotifier(n)
	return e, st, n
}

func TestArtifactNotifyPrimesWithoutAnnouncingTheBacklog(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	publish(t, st, artifact("a-old-1", "Old report", "Nightly"))
	publish(t, st, artifact("a-old-2", "Older report", "Nightly"))

	e.notifyNewArtifacts()

	if got := n.all(); len(got) != 0 {
		t.Fatalf("pre-existing artifacts must stay silent, got %d notification(s): %+v", len(got), got)
	}

	// The next publish does notify — priming only covers what was already there.
	publish(t, st, artifact("a-new", "Fresh report", "Nightly"))
	e.notifyNewArtifacts()

	got := n.all()
	if len(got) != 1 {
		t.Fatalf("expected 1 notification for the new artifact, got %d: %+v", len(got), got)
	}
	if got[0].ArtifactID != "a-new" {
		t.Fatalf("notification should carry the artifact id, got %q", got[0].ArtifactID)
	}
	if !strings.Contains(got[0].Message, "Fresh report") || !strings.Contains(got[0].Message, "Nightly") {
		t.Fatalf("message should name the artifact and its task, got %q", got[0].Message)
	}
	if got[0].Title == "" {
		t.Fatal("notification should have a title")
	}
}

// An empty store must still be primed on the first pass, or the very first
// artifact ever published would be swallowed as "backlog".
func TestArtifactNotifyFirstEverArtifactNotifies(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	e.notifyNewArtifacts() // nothing published yet

	publish(t, st, artifact("a-1", "First report", "Nightly"))
	e.notifyNewArtifacts()

	if got := n.all(); len(got) != 1 || got[0].ArtifactID != "a-1" {
		t.Fatalf("expected one notification for a-1, got %+v", got)
	}
}

func TestArtifactNotifyOnlyOncePerArtifact(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	e.notifyNewArtifacts()
	publish(t, st, artifact("a-1", "Report", "Nightly"))

	for i := 0; i < 3; i++ {
		e.notifyNewArtifacts()
	}

	if got := n.all(); len(got) != 1 {
		t.Fatalf("expected exactly 1 notification across repeated ticks, got %d: %+v", len(got), got)
	}
}

func TestArtifactNotifyEachOfSeveralPublishedInOnePass(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	e.notifyNewArtifacts()
	publish(t, st, artifact("a-1", "One", "Nightly"))
	publish(t, st, artifact("a-2", "Two", "Nightly"))

	e.notifyNewArtifacts()

	got := n.all()
	if len(got) != 2 {
		t.Fatalf("expected 2 notifications, got %d: %+v", len(got), got)
	}
	if got[0].ArtifactID != "a-1" || got[1].ArtifactID != "a-2" {
		t.Fatalf("expected oldest-first order, got %q then %q", got[0].ArtifactID, got[1].ArtifactID)
	}
}

// Without a notifier (tests, or a channel-less setup) the pass must be a no-op —
// and must not consume the artifacts, so nothing is silently dropped.
func TestArtifactNotifyWithoutNotifierIsANoop(t *testing.T) {
	e, st := newTestEngine(t, &stub{}, clock.NewFake(time.Now()))
	publish(t, st, artifact("a-1", "Report", "Nightly"))
	e.notifyNewArtifacts()

	state, err := st.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if state.IsArtifactNotifyPrimed() {
		t.Fatal("a pass without a notifier must not prime the state")
	}
}

func TestArtifactNotifyBody(t *testing.T) {
	cases := []struct {
		name string
		art  store.Artifact
		want []string
	}{
		{
			name: "title task and description",
			art: store.Artifact{ID: "a", Title: "Nightly summary", TaskName: "Repo hygiene",
				Description: "3 findings, 1 fixed"},
			want: []string{"Nightly summary", "Repo hygiene", "3 findings, 1 fixed"},
		},
		{
			name: "falls back to the file name",
			art:  store.Artifact{ID: "a", FileName: "report.html"},
			want: []string{"report.html"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := artifactNotifyBody(tc.art)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Fatalf("body %q should contain %q", body, w)
				}
			}
		})
	}
}

func TestArtifactNotifyBodyIsTruncated(t *testing.T) {
	body := artifactNotifyBody(store.Artifact{
		ID: "a", Title: "Report", Description: strings.Repeat("x", 800),
	})
	if n := len([]rune(body)); n > artifactNotifyBodyLimit+1 {
		t.Fatalf("body should be capped at %d runes (+ ellipsis), got %d", artifactNotifyBodyLimit, n)
	}
	if !strings.HasSuffix(body, "…") {
		t.Fatalf("a truncated body should end in an ellipsis, got %q", body[len(body)-10:])
	}
}

// The pass runs on every tick of an otherwise idle daemon, so "nothing new" must
// not touch state.json: each write is an fsync plus a cross-process lock. The
// atomic write renames a temp file into place, so a rewrite shows up as a new
// inode.
func TestArtifactNotifyIdleTickDoesNotRewriteState(t *testing.T) {
	e, st, _ := newArtifactEngine(t)
	publish(t, st, artifact("a-1", "Report", "Nightly"))
	e.notifyNewArtifacts() // primes
	e.notifyNewArtifacts() // notifies a-1

	statePath := filepath.Join(st.Home(), "state.json")
	before := inodeOf(t, statePath)
	for i := 0; i < 3; i++ {
		e.notifyNewArtifacts()
	}
	if after := inodeOf(t, statePath); after != before {
		t.Fatalf("state.json was rewritten with nothing to record (inode %d → %d)", before, after)
	}

	// A new artifact still gets through.
	publish(t, st, artifact("a-2", "Second", "Nightly"))
	e.notifyNewArtifacts()
	if after := inodeOf(t, statePath); after == before {
		t.Fatal("state.json should have been rewritten after a new artifact")
	}
}

// With artifact notifications switched off, a new artifact raises nothing on
// any channel and leaves no inbox entry, and switching them back on does not
// deliver what was published in the meantime.
func TestArtifactNotifyDisabledStaysSilentAndDoesNotCatchUp(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	e.notifyNewArtifacts() // prime on an empty store

	setArtifactNotifyDisabled(t, st, true)
	publish(t, st, artifact("a-quiet", "Quiet report", "Nightly"))
	e.notifyNewArtifacts()
	if got := n.all(); len(got) != 0 {
		t.Fatalf("disabled artifact notifications must stay silent, got %+v", got)
	}
	inbox, err := st.Inbox()
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 0 {
		t.Fatalf("disabled artifact notifications must not reach the inbox, got %+v", inbox)
	}

	setArtifactNotifyDisabled(t, st, false)
	e.notifyNewArtifacts()
	if got := n.all(); len(got) != 0 {
		t.Fatalf("re-enabling must not announce artifacts published while off, got %+v", got)
	}

	publish(t, st, artifact("a-loud", "Loud report", "Nightly"))
	e.notifyNewArtifacts()
	got := n.all()
	if len(got) != 1 || got[0].ArtifactID != "a-loud" {
		t.Fatalf("expected one notification for a-loud after re-enabling, got %+v", got)
	}
}

func setArtifactNotifyDisabled(t *testing.T, st *store.Store, off bool) {
	t.Helper()
	if err := st.UpdateConfig(func(cfg *store.Config) error {
		cfg.Settings.ArtifactNotifyDisabled = off
		return nil
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
}

// An unreadable config must not consume pending artifacts: they stay pending
// and are announced once the config can be read again.
func TestArtifactNotifyUnreadableConfigKeepsArtifactsPending(t *testing.T) {
	e, st, n := newArtifactEngine(t)
	e.notifyNewArtifacts() // prime on an empty store

	cfgPath := filepath.Join(st.Home(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("not = [valid toml"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	publish(t, st, artifact("a-wait", "Waiting report", "Nightly"))
	e.notifyNewArtifacts()
	if got := n.all(); len(got) != 0 {
		t.Fatalf("nothing may be sent while the config is unreadable, got %+v", got)
	}

	if err := os.Remove(cfgPath); err != nil {
		t.Fatalf("remove broken config: %v", err)
	}
	e.notifyNewArtifacts()
	got := n.all()
	if len(got) != 1 || got[0].ArtifactID != "a-wait" {
		t.Fatalf("the artifact should be announced once the config reads again, got %+v", got)
	}
}
