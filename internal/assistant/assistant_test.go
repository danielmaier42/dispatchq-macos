package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

var claude = provider.Instance{ID: "claude", Kind: provider.KindClaudeCode, Name: "Claude", Enabled: true}

// stubAsker records the asides made and replays canned answers.
type stubAsker struct {
	answers []provider.Aside
	err     error
	reqs    []provider.AsideRequest
}

func (s *stubAsker) Ask(_ context.Context, _ provider.Instance, req provider.AsideRequest) (provider.Aside, error) {
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return provider.Aside{}, s.err
	}
	if len(s.answers) == 0 {
		return provider.Aside{}, errors.New("stub: no answer left")
	}
	out := s.answers[0]
	s.answers = s.answers[1:]
	return out, nil
}

func validated(structured string) provider.Aside {
	return provider.Aside{Text: "ignored", Structured: json.RawMessage(structured)}
}

// stubSource serves a fixed overview, echoes lookups and passes actions on.
type stubSource struct {
	overview string
	lookups  []Lookup
}

func (s *stubSource) Overview(context.Context) (string, error) { return s.overview, nil }

func (s *stubSource) Lookup(_ context.Context, l Lookup) (string, string) {
	s.lookups = append(s.lookups, l)
	return "Read " + l.Tool + " " + l.Arg, "details of " + l.Arg
}

func (s *stubSource) Check(_ context.Context, a []Action) ([]Action, error) { return a, nil }

func TestTurnAnswersAndCarriesTheState(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{
		validated(`{"reply":"Geöffnet.","actions":[{"type":"open_view","view":"artifacts"}]}`),
	}}
	svc := New(ask, &stubSource{overview: "Tasks in the queue (0)"}, nil, Runtime{})

	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "zeig mir die artefakte", Focus: Focus{View: "queue"}})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if r.Text != "Geöffnet." || len(r.Actions) != 1 || r.Actions[0].View != "artifacts" {
		t.Fatalf("reply = %+v", r)
	}
	if r.SessionID == "" {
		t.Fatal("no session id")
	}
	req := ask.reqs[0]
	if req.Resume || req.System != systemPrompt(Runtime{}) || req.Model != DefaultModel || !req.Continues || req.Schema == "" {
		t.Fatalf("first request = %+v, want a new, continuing session on the default model", req)
	}
	for _, want := range []string{"[DispatchQ state]\nTasks in the queue (0)", "the queue view", "[Operator]\nzeig mir die artefakte"} {
		if !strings.Contains(req.Text, want) {
			t.Fatalf("message lacks %q:\n%s", want, req.Text)
		}
	}
}

// "sonnet" is a Claude name; a Codex turn with no model named runs on Codex's
// own default instead of a model it would reject.
func TestTurnLeavesOtherHarnessesOnTheirOwnDefault(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{validated(`{"reply":"ok"}`)}}
	codex := provider.Instance{ID: "codex", Kind: provider.KindCodex}
	if _, err := New(ask, &stubSource{}, nil, Runtime{}).Turn(context.Background(), codex, "", Request{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if ask.reqs[0].Model != "" {
		t.Errorf("model = %q, want the harness default", ask.reqs[0].Model)
	}
}

func TestTurnResumesAndSkipsAnUnchangedState(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{
		validated(`{"reply":"a"}`),
		validated(`{"reply":"b"}`),
		validated(`{"reply":"c"}`),
	}}
	src := &stubSource{overview: "Now: 12:00\nstate one"}
	svc := New(ask, src, nil, Runtime{})
	ctx := context.Background()

	r1, err := svc.Turn(ctx, claude, "opus", Request{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Turn(ctx, claude, "opus", Request{SessionID: r1.SessionID, Text: "two"}); err != nil {
		t.Fatal(err)
	}
	second := ask.reqs[1]
	if !second.Resume || second.System != "" || second.SessionID != r1.SessionID || second.Model != "opus" {
		t.Fatalf("second request = %+v, want a resume without instructions", second)
	}
	if !strings.Contains(second.Text, "unchanged since the previous message. Now: 12:00") || strings.Contains(second.Text, "state one") {
		t.Fatalf("unchanged state sent again:\n%s", second.Text)
	}

	src.overview = "Now: 12:05\nstate two"
	if _, err := svc.Turn(ctx, claude, "opus", Request{SessionID: r1.SessionID, Text: "three"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ask.reqs[2].Text, "state two") {
		t.Fatalf("changed state not sent:\n%s", ask.reqs[2].Text)
	}
}

func TestTurnTreatsAClockChangeAsUnchanged(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{validated(`{"reply":"a"}`), validated(`{"reply":"b"}`)}}
	src := &stubSource{overview: "Now: 12:00\ntasks"}
	svc := New(ask, src, nil, Runtime{})
	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	src.overview = "Now: 12:07\ntasks"
	if _, err := svc.Turn(context.Background(), claude, "", Request{SessionID: r.SessionID, Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if got := ask.reqs[1].Text; !strings.Contains(got, "unchanged since the previous message. Now: 12:07") || strings.Contains(got, "tasks") {
		t.Fatalf("second message = %q", got)
	}
}

func TestTurnReplaysHistoryForAForgottenSession(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{validated(`{"reply":"ok"}`)}}
	svc := New(ask, &stubSource{overview: "s"}, nil, Runtime{})
	_, err := svc.Turn(context.Background(), claude, "", Request{
		SessionID: "gone",
		Text:      "and now?",
		History:   []Message{{Role: "user", Text: "plan a job"}, {Role: "assistant", Text: "Which folder?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := ask.reqs[0]
	if req.Resume || req.SessionID == "gone" {
		t.Fatalf("request = %+v, want a fresh session", req)
	}
	if !strings.Contains(req.Text, "Operator: plan a job\nYou: Which folder?") {
		t.Fatalf("history not replayed:\n%s", req.Text)
	}
}

func TestTurnRunsLookupsBeforeAnswering(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{
		validated(`{"reply":"","lookups":[{"tool":"run","arg":"r1"}]}`),
		validated(`{"reply":"Run r1 failed on a missing file."}`),
	}}
	src := &stubSource{overview: "s"}
	svc := New(ask, src, nil, Runtime{})

	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "why did it fail?"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != "Run r1 failed on a missing file." {
		t.Fatalf("reply = %+v", r)
	}
	if len(src.lookups) != 1 || src.lookups[0].Arg != "r1" {
		t.Fatalf("lookups = %+v", src.lookups)
	}
	if len(r.Steps) != 1 || r.Steps[0] != "Read run r1" {
		t.Fatalf("steps = %v", r.Steps)
	}
	second := ask.reqs[1]
	if !second.Resume || !strings.Contains(second.Text, "[Lookup results]") || !strings.Contains(second.Text, "details of r1") {
		t.Fatalf("lookup results not sent back: %+v", second)
	}
}

func TestTurnStopsLookingUpAfterTheLastRound(t *testing.T) {
	again := validated(`{"reply":"","lookups":[{"tool":"runs","arg":""}]}`)
	ask := &stubAsker{answers: []provider.Aside{again, again, again, validated(`{"reply":"done","lookups":[{"tool":"runs","arg":""}]}`)}}
	svc := New(ask, &stubSource{overview: "s"}, nil, Runtime{})

	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ask.reqs) != 1+maxLookupRounds {
		t.Fatalf("%d calls, want %d", len(ask.reqs), 1+maxLookupRounds)
	}
	if !strings.Contains(ask.reqs[maxLookupRounds].Text, "no further lookups") {
		t.Fatalf("last round not told to answer:\n%s", ask.reqs[maxLookupRounds].Text)
	}
	if r.Text != "done" {
		t.Fatalf("reply = %+v", r)
	}
}

func TestTurnCapsLookupsPerRound(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{
		validated(`{"reply":"","lookups":[{"tool":"run","arg":"1"},{"tool":"run","arg":"2"},{"tool":"run","arg":"3"},{"tool":"run","arg":"4"},{"tool":"run","arg":"5"}]}`),
		validated(`{"reply":"ok"}`),
	}}
	src := &stubSource{overview: "s"}
	if _, err := New(ask, src, nil, Runtime{}).Turn(context.Background(), claude, "", Request{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(src.lookups) != maxLookupsPerRound {
		t.Fatalf("%d lookups, want %d", len(src.lookups), maxLookupsPerRound)
	}
}

func TestTurnReadsAnUnvalidatedAnswer(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"```json\n{\"reply\":\"fine\"}\n```", "fine"},
		{"Just prose.", "Just prose."},
	} {
		ask := &stubAsker{answers: []provider.Aside{{Text: tc.text}}}
		r, err := New(ask, &stubSource{}, nil, Runtime{}).Turn(context.Background(), claude, "", Request{Text: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if r.Text != tc.want {
			t.Fatalf("%q -> %q, want %q", tc.text, r.Text, tc.want)
		}
	}
}

func TestTurnFailures(t *testing.T) {
	if _, err := New(&stubAsker{}, &stubSource{}, nil, Runtime{}).Turn(context.Background(), claude, "", Request{Text: "  "}); err == nil {
		t.Fatal("empty message accepted")
	}
	ask := &stubAsker{err: errors.New("limit reached")}
	svc := New(ask, &stubSource{}, nil, Runtime{})
	if _, err := svc.Turn(context.Background(), claude, "", Request{Text: "x"}); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("err = %v, want the harness error", err)
	}
	if len(svc.sessions) != 0 {
		t.Fatal("a failed session was kept")
	}
	if _, err := New(&stubAsker{answers: []provider.Aside{{}}}, &stubSource{}, nil, Runtime{}).Turn(context.Background(), claude, "", Request{Text: "x"}); err == nil {
		t.Fatal("an empty answer was accepted")
	}
}

func TestTurnAdoptsTheHarnessSessionID(t *testing.T) {
	a := validated(`{"reply":"ok"}`)
	a.SessionID = "theirs"
	ask := &stubAsker{answers: []provider.Aside{a, validated(`{"reply":"again"}`)}}
	svc := New(ask, &stubSource{}, nil, Runtime{})
	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "x"})
	if err != nil || r.SessionID != "theirs" {
		t.Fatalf("reply = %+v, %v; want the harness's id", r, err)
	}
	if _, err := svc.Turn(context.Background(), claude, "", Request{SessionID: "theirs", Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if !ask.reqs[1].Resume {
		t.Fatal("renamed session not resumed")
	}
}

func TestSessionsExpire(t *testing.T) {
	ask := &stubAsker{answers: []provider.Aside{validated(`{"reply":"a"}`), validated(`{"reply":"b"}`)}}
	svc := New(ask, &stubSource{}, nil, Runtime{})
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	r, err := svc.Turn(context.Background(), claude, "", Request{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(sessionTTL + time.Minute)
	if _, err := svc.Turn(context.Background(), claude, "", Request{SessionID: r.SessionID, Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if ask.reqs[1].Resume {
		t.Fatal("an expired session was resumed")
	}
}
