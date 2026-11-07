package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/feedback"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
)

// fakeAsker replays one canned answer from a harness.
type fakeAsker struct {
	answer provider.Aside
	err    error
}

func (f fakeAsker) Ask(context.Context, provider.Instance, provider.AsideRequest) (provider.Aside, error) {
	if f.err != nil {
		return provider.Aside{}, f.err
	}
	return f.answer, nil
}

// validated is an answer the harness checked against the schema itself.
func validated(structured string) provider.Aside {
	return provider.Aside{Structured: json.RawMessage(structured)}
}

// newFeedbackServer wires a server whose feedback chat answers through ask. The
// claude provider gets an explicit binary path so availability does not depend
// on what is installed on the machine running the tests.
func newFeedbackServer(t *testing.T, ask feedback.Asker) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cfg, err := st.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Providers[0].BinaryPath = "/usr/local/bin/claude"
	if err := st.SaveConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	var svc *feedback.Service
	if ask != nil {
		svc = feedback.New(ask)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, Feedback: svc, OSVersion: func() string { return "macOS 15.6" }}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestFeedbackStatusReportsAvailability(t *testing.T) {
	srv, _ := newFeedbackServer(t, fakeAsker{answer: validated(`{"status":"ready","title":"T","body":"B"}`)})
	var st feedbackStatus
	do(t, srv, http.MethodGet, "/api/feedback", nil).into(t, &st)
	if !st.Available || st.Reason != "" {
		t.Fatalf("status = %+v, want available", st)
	}
	if st.Repo != update.DefaultRepo || st.AppVersion != version.String() || st.OSVersion != "macOS 15.6" {
		t.Fatalf("status = %+v, want repo/versions filled in", st)
	}
	if st.MaxTurns != feedback.MaxUserTurns {
		t.Fatalf("max_turns = %d, want %d", st.MaxTurns, feedback.MaxUserTurns)
	}
}

func TestFeedbackStatusExplainsAnUnavailableAssistant(t *testing.T) {
	srv, _ := newFeedbackServer(t, nil)
	var st feedbackStatus
	do(t, srv, http.MethodGet, "/api/feedback", nil).into(t, &st)
	if st.Available || st.Reason == "" {
		t.Fatalf("status = %+v, want unavailable with a reason", st)
	}
	// The dashboard still needs the versions for its manual form.
	if st.AppVersion == "" || st.OSVersion != "macOS 15.6" {
		t.Fatalf("status = %+v, want the environment details anyway", st)
	}
}

// A "Default" in Settings that points at a harness without asides used to read
// as if no provider were set up at all; the reason names the provider instead.
func TestFeedbackStatusNamesADefaultThatCannotDraft(t *testing.T) {
	_, st := newFeedbackServer(t, fakeAsker{})
	reg := provider.NewRegistry(stubAdapter{
		health: provider.Health{State: provider.HealthReady},
		caps:   provider.Capabilities{InteractiveResume: true},
	})
	srv := httptest.NewServer(handler(Deps{
		Store: st, Feedback: feedback.New(fakeAsker{}), Registry: reg, Providers: provider.NewChecker(reg),
	}))
	t.Cleanup(srv.Close)

	var got feedbackStatus
	do(t, srv, http.MethodGet, "/api/feedback", nil).into(t, &got)
	if got.Available || !strings.Contains(got.Reason, "the default provider") || !strings.Contains(got.Reason, "cannot draft") {
		t.Fatalf("status = %+v, want the default provider named as unable to draft", got)
	}
}

func TestFeedbackTurnReturnsTheDraft(t *testing.T) {
	out := validated(`{"status":"ready","title":"T","body":"B","labels":["bug"]}`)
	srv, _ := newFeedbackServer(t, fakeAsker{answer: out})
	var d feedback.Draft
	r := do(t, srv, http.MethodPost, "/api/feedback/turn", map[string]string{"text": "something broke"})
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &d)
	if d.Status != "ready" || d.Title != "T" || d.SessionID == "" {
		t.Fatalf("draft = %+v", d)
	}
}

func TestFeedbackTurnReportsAFailingAssistant(t *testing.T) {
	srv, _ := newFeedbackServer(t, fakeAsker{err: errors.New("exit status 1")})
	r := do(t, srv, http.MethodPost, "/api/feedback/turn", map[string]string{"text": "hi"})
	if r.Status != http.StatusBadGateway {
		t.Fatalf("status = %d (%s), want 502", r.Status, r.Body)
	}
}

func TestFeedbackTurnIsUnavailableWithoutTheService(t *testing.T) {
	srv, _ := newFeedbackServer(t, nil)
	r := do(t, srv, http.MethodPost, "/api/feedback/turn", map[string]string{"text": "hi"})
	if r.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s), want 503", r.Status, r.Body)
	}
}

func TestFeedbackURLAppendsTheEnvironment(t *testing.T) {
	srv, _ := newFeedbackServer(t, fakeAsker{answer: validated(`{"status":"ready","title":"T","body":"B"}`)})
	var got map[string]string
	do(t, srv, http.MethodPost, "/api/feedback/url", map[string]any{
		"title": "  A  title ", "body": "The report.", "labels": []string{"bug"},
	}).into(t, &got)
	u, err := url.Parse(got["url"])
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Query().Get("title") != "A title" {
		t.Fatalf("title = %q", u.Query().Get("title"))
	}
	body := u.Query().Get("body")
	if !strings.HasPrefix(body, "The report.") {
		t.Fatalf("body = %q", body)
	}
	if !strings.Contains(body, "DispatchQ "+version.String()) || !strings.Contains(body, "macOS 15.6") {
		t.Fatalf("body does not carry the environment line: %q", body)
	}
}

func TestFeedbackURLLeavesOutAnUnknownOSVersion(t *testing.T) {
	// A daemon that cannot read sw_vers must not write "macOS " with nothing
	// after it into a public issue.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	var got map[string]string
	do(t, srv, http.MethodPost, "/api/feedback/url", map[string]any{"title": "T", "body": "B"}).into(t, &got)
	u, _ := url.Parse(got["url"])
	body := u.Query().Get("body")
	if strings.Contains(body, "macOS") {
		t.Fatalf("body = %q, want no macOS line", body)
	}
	if !strings.Contains(body, "DispatchQ "+version.String()) {
		t.Fatalf("body = %q, want the app version", body)
	}
}

func TestFeedbackURLKeepsTheSystemNameAsGiven(t *testing.T) {
	// The platform names its own system; a Windows daemon must not end up as
	// "macOS Windows ..." in a public issue.
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, OSVersion: func() string { return "Windows 10.0 (build 26100)" }}))
	t.Cleanup(srv.Close)
	var got map[string]string
	do(t, srv, http.MethodPost, "/api/feedback/url", map[string]any{"title": "T", "body": "B"}).into(t, &got)
	u, _ := url.Parse(got["url"])
	body := u.Query().Get("body")
	if !strings.Contains(body, " · Windows 10.0 (build 26100)") || strings.Contains(body, "macOS") {
		t.Fatalf("body = %q, want the Windows line without macOS", body)
	}
}

func TestFeedbackURLNeedsATitle(t *testing.T) {
	srv, _ := newFeedbackServer(t, fakeAsker{answer: validated(`{"status":"ready","title":"T","body":"B"}`)})
	r := do(t, srv, http.MethodPost, "/api/feedback/url", map[string]any{"title": "   ", "body": "B"})
	if r.Status != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", r.Status, r.Body)
	}
}

func TestFeedbackURLCapsAnOversizedTitle(t *testing.T) {
	// A title long enough to blow the URL budget on its own would make GitHub
	// answer with 414, and trimming the body cannot save it.
	srv, _ := newFeedbackServer(t, fakeAsker{answer: validated(`{"status":"ready","title":"T","body":"B"}`)})
	var got map[string]string
	do(t, srv, http.MethodPost, "/api/feedback/url", map[string]any{
		"title": strings.Repeat("very long title ", 600), "body": "B",
	}).into(t, &got)
	if len(got["url"]) > feedback.MaxURLLen {
		t.Fatalf("url is %d bytes, want at most %d", len(got["url"]), feedback.MaxURLLen)
	}
}
