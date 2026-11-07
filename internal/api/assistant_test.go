package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/assistant"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/store"
)

// newAssistantServer wires a server whose assistant answers through ask, on a
// claude provider with an explicit binary so availability does not depend on
// what is installed here.
func newAssistantServer(t *testing.T, ask assistant.Asker) (*httptest.Server, *store.Store) {
	t.Helper()
	_, st := newFeedbackServer(t, nil)
	var svc *assistant.Service
	if ask != nil {
		svc = assistant.New(ask, &assistant.StoreSource{Store: st}, nil, assistant.Runtime{})
	}
	srv := httptest.NewServer(handler(Deps{Store: st, Assistant: svc}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestAssistantStatus(t *testing.T) {
	srv, _ := newAssistantServer(t, fakeAsker{})
	var st assistantStatus
	do(t, srv, http.MethodGet, "/api/assistant", nil).into(t, &st)
	if !st.Available || st.Reason != "" || st.Provider == "" {
		t.Fatalf("status = %+v, want available with the provider named", st)
	}

	srv, _ = newAssistantServer(t, nil)
	do(t, srv, http.MethodGet, "/api/assistant", nil).into(t, &st)
	if st.Available || st.Reason == "" {
		t.Fatalf("status = %+v, want unavailable with a reason", st)
	}
}

func TestAssistantStatusNamesADefaultThatCannotAnswer(t *testing.T) {
	_, st := newAssistantServer(t, nil)
	reg := provider.NewRegistry(stubAdapter{
		health: provider.Health{State: provider.HealthReady},
		caps:   provider.Capabilities{InteractiveResume: true},
	})
	srv := httptest.NewServer(handler(Deps{
		Store: st, Assistant: assistant.New(fakeAsker{}, &assistant.StoreSource{Store: st}, nil, assistant.Runtime{}),
		Registry: reg, Providers: provider.NewChecker(reg),
	}))
	t.Cleanup(srv.Close)

	var got assistantStatus
	do(t, srv, http.MethodGet, "/api/assistant", nil).into(t, &got)
	if got.Available || !strings.Contains(got.Reason, "the default provider") || !strings.Contains(got.Reason, "assistant provider") {
		t.Fatalf("status = %+v, want the default provider named as unable to answer", got)
	}
	r := do(t, srv, http.MethodPost, "/api/assistant/turn", map[string]string{"text": "hi"})
	if r.Status != http.StatusServiceUnavailable {
		t.Fatalf("turn status = %d, want 503", r.Status)
	}
}

func TestAssistantTurn(t *testing.T) {
	srv, _ := newAssistantServer(t, fakeAsker{answer: validated(`{"reply":"Opened.","actions":[{"type":"open_view","view":"log"},{"type":"open_run","id":"invented"}]}`)})
	var got assistant.Reply
	r := do(t, srv, http.MethodPost, "/api/assistant/turn", map[string]any{"text": "show me the log", "focus": map[string]string{"view": "queue"}})
	if r.Status != http.StatusOK {
		t.Fatalf("status = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &got)
	if got.Text != "Opened." || got.SessionID == "" || len(got.Actions) != 1 || got.Actions[0].View != "log" {
		t.Fatalf("reply = %+v, want the invented run dropped", got)
	}
}

func TestAssistantTurnFailures(t *testing.T) {
	srv, _ := newAssistantServer(t, fakeAsker{err: errors.New("limit reached")})
	if r := do(t, srv, http.MethodPost, "/api/assistant/turn", map[string]string{"text": "hi"}); r.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", r.Status)
	}
	srv, _ = newAssistantServer(t, nil)
	if r := do(t, srv, http.MethodPost, "/api/assistant/turn", map[string]string{"text": "hi"}); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", r.Status)
	}
}

// recordingCLI stands in for the dispatchq binary.
type recordingCLI struct{ runs [][]string }

func (c *recordingCLI) Run(_ context.Context, argv []string) (string, error) {
	c.runs = append(c.runs, argv)
	return "ok: " + strings.Join(argv, " "), nil
}

func postCLI(t *testing.T, srv *httptest.Server, host, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/api/assistant/cli", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", contentType)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func TestAssistantCLIRunsConfirmedCommands(t *testing.T) {
	_, st := newAssistantServer(t, nil)
	cli := &recordingCLI{}
	srv := httptest.NewServer(handler(Deps{Store: st, Assistant: assistant.New(fakeAsker{}, &assistant.StoreSource{Store: st}, cli, assistant.Runtime{})}))
	t.Cleanup(srv.Close)

	code, body := postCLI(t, srv, "", "application/json", `{"argv":["notify","--title","T","--message","M ü"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"output":"ok: notify --title=T --message=M ü"`) {
		t.Fatalf("status %d, body %s", code, body)
	}
	for _, tc := range []struct {
		host, ct, body string
		status         int
	}{
		{"evil.example:10765", "application/json", `{"argv":["read-all"]}`, http.StatusForbidden},
		{"", "text/plain", `{"argv":["read-all"]}`, http.StatusUnsupportedMediaType},
		{"", "application/json", `{"argv":["settings","--pushover-token","x"]}`, http.StatusUnprocessableEntity},
		{"", "application/json", `{"argv":["rm","t1"]}`, http.StatusUnprocessableEntity},
		{"", "application/json", `{"argv":["/bin/sh","-c","id"]}`, http.StatusUnprocessableEntity},
	} {
		if code, body := postCLI(t, srv, tc.host, tc.ct, tc.body); code != tc.status {
			t.Errorf("%s via %q: status %d (%s), want %d", tc.body, tc.host, code, body, tc.status)
		}
	}
	if len(cli.runs) != 1 {
		t.Fatalf("ran %q, want only the first command", cli.runs)
	}
}
