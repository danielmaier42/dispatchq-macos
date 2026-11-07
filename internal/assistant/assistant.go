// Package assistant is the chat that operates DispatchQ: the operator asks in
// plain words about runs, artifacts and the queue, or for a job to be planned,
// and gets a short answer plus the actions that carry it out.
//
// It is an aside like the feedback assistant (tool-free, project-free, in a
// directory of its own), so the model can never touch the machine. What it
// knows comes from DispatchQ: every message carries a compact picture of the
// current state, and the model may ask for details (one run's output, one
// task's prompt) through lookups the daemon answers read-only before the model
// writes its reply. What it does is limited to actions: moving the app to a
// view, a run or an artifact happens at once; anything that changes the queue
// comes back as a proposal the operator confirms in the app.
package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/randid"
)

// DefaultModel is what the chat runs on Claude Code when Settings names no
// model; other harnesses do not know the name and use their own default. Planning
// a job means writing a prompt and a schedule someone relies on overnight,
// which is worth more than the smallest model, and still far from the
// expensive one picked for real tasks.
const DefaultModel = "sonnet"

// maxLookupRounds bounds how often one message may go back for details before
// the model has to answer with what it has.
const maxLookupRounds = 3

// maxLookupsPerRound bounds how many details one round may ask for.
const maxLookupsPerRound = 4

// callTimeout bounds one CLI call; turnTimeout the whole message, lookups and
// all.
const (
	callTimeout = 2 * time.Minute
	turnTimeout = 4 * time.Minute
)

// sessionTTL is how long an idle conversation stays resumable in memory. A
// message after that starts a new session, seeded with the history the app
// sends along.
const sessionTTL = 2 * time.Hour

// maxInputChars bounds one message, and maxHistory the replay of an expired
// conversation.
const (
	maxInputChars = 6000
	maxHistory    = 12
)

// Message is one line of the conversation as the app shows it, sent along so a
// conversation the daemon has forgotten can be picked up again.
type Message struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// Focus is what the operator has open in the app while asking, so "this run"
// or "the artifact" can be resolved without asking back.
type Focus struct {
	View       string `json:"view,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
}

// Request is one message from the app.
type Request struct {
	SessionID string    `json:"session_id"`
	Text      string    `json:"text"`
	History   []Message `json:"history,omitempty"`
	Focus     Focus     `json:"focus"`
}

// Reply is what the app gets back for one message.
type Reply struct {
	SessionID string `json:"session_id"`
	// Text is the answer, short Markdown in the operator's language.
	Text string `json:"text"`
	// Actions carry the answer out; see Action.
	Actions []Action `json:"actions"`
	// Steps name what the assistant looked up on the way, so the operator can
	// see where an answer came from.
	Steps []string `json:"steps,omitempty"`
}

// Asker puts one question to a provider instance (satisfied by *aside.Runner).
type Asker interface {
	Ask(ctx context.Context, inst provider.Instance, req provider.AsideRequest) (provider.Aside, error)
}

// Source is what the assistant knows about DispatchQ and how it checks what it
// proposes (satisfied by *StoreSource).
type Source interface {
	// Overview is the compact state every message carries.
	Overview(ctx context.Context) (string, error)
	// Lookup answers one detail. It never fails: a lookup that finds nothing
	// says so in its text, which is what the model needs to hear. label names
	// the step for the operator.
	Lookup(ctx context.Context, l Lookup) (label, text string)
	// Check drops actions that name nothing real, labels the rest, and marks
	// proposals that could not be confirmed as they stand.
	Check(ctx context.Context, actions []Action) ([]Action, error)
}

// Service holds the in-flight conversations.
type Service struct {
	ask Asker
	src Source
	cli CLI
	// system is the system prompt, which names this machine.
	system string
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	seen time.Time
	// state is the digest of the overview the model saw last, so an unchanged
	// state is not sent again on every message.
	state [32]byte
}

// New returns a Service that asks through a, reads DispatchQ through src, runs
// confirmed dispatchq commands through cli (nil: it cannot) and describes rt
// to the model.
func New(a Asker, src Source, cli CLI, rt Runtime) *Service {
	return &Service{ask: a, src: src, cli: cli, system: systemPrompt(rt), now: time.Now, sessions: map[string]*session{}}
}

// Turn answers one message. inst is the provider that answers, and model the
// model it answers with ("" uses DefaultModel on Claude Code and the harness's
// own default elsewhere).
func (s *Service) Turn(ctx context.Context, inst provider.Instance, model string, req Request) (Reply, error) {
	text := clip(strings.TrimSpace(req.Text), maxInputChars)
	if text == "" {
		return Reply{}, errors.New("write something first")
	}
	if s.ask == nil || s.src == nil {
		return Reply{}, errors.New("the assistant is not available")
	}
	if model == "" && inst.Kind == provider.KindClaudeCode {
		model = DefaultModel
	}
	ctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	overview, err := s.src.Overview(ctx)
	if err != nil {
		return Reply{}, fmt.Errorf("reading the queue: %w", err)
	}
	id, sess, resume := s.begin(req.SessionID)
	// The clock line changes every minute; the rest only when something
	// happened, which is what decides whether the state is sent again.
	now, rest, _ := strings.Cut(overview, "\n")
	digest := sha256.Sum256([]byte(rest))

	var b strings.Builder
	if !resume {
		writeHistory(&b, req.History)
	}
	if resume && digest == sess.state {
		b.WriteString("[DispatchQ state: unchanged since the previous message. " + now + "]\n\n")
	} else {
		b.WriteString("[DispatchQ state]\n" + overview + "\n\n")
	}
	if f := focusLine(req.Focus); f != "" {
		b.WriteString("[The operator has open] " + f + "\n\n")
	}
	b.WriteString("[Operator]\n" + text)

	ans, id, err := s.call(ctx, inst, model, id, resume, b.String())
	if err != nil {
		return Reply{}, err
	}
	s.mark(id, digest)

	var steps []string
	for round := 1; len(ans.Lookups) > 0 && round <= maxLookupRounds; round++ {
		var res strings.Builder
		res.WriteString("[Lookup results]\n")
		for _, l := range limit(ans.Lookups, maxLookupsPerRound) {
			label, out := s.src.Lookup(ctx, l)
			steps = append(steps, label)
			fmt.Fprintf(&res, "\n### %s %s\n%s\n", l.Tool, strings.TrimSpace(l.Arg+" "+strings.Join(l.Argv, " ")), out)
		}
		if round == maxLookupRounds {
			res.WriteString("\n[System note: no further lookups are possible for this message. Answer now with what you have.]")
		}
		if ans, id, err = s.call(ctx, inst, model, id, true, res.String()); err != nil {
			return Reply{}, err
		}
	}

	reply := Reply{SessionID: id, Text: strings.TrimSpace(ans.Reply), Steps: steps}
	if reply.Actions, err = s.src.Check(ctx, ans.Actions); err != nil {
		return Reply{}, fmt.Errorf("checking the answer against the queue: %w", err)
	}
	if reply.Actions == nil {
		reply.Actions = []Action{}
	}
	if reply.Text == "" && len(reply.Actions) == 0 {
		reply.Text = "I could not work that out. Try asking more specifically."
	}
	return reply, nil
}

// answer is the shape every model turn has to come back in.
type answer struct {
	Reply   string   `json:"reply"`
	Lookups []Lookup `json:"lookups"`
	Actions []Action `json:"actions"`
}

// call runs one turn and parses it. It returns the session id to continue
// with, which a harness that names its own sessions may have changed.
func (s *Service) call(ctx context.Context, inst provider.Instance, model, id string, resume bool, text string) (answer, string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req := provider.AsideRequest{
		Model:     model,
		Text:      text,
		Schema:    schema,
		SessionID: id,
		Resume:    resume,
		Continues: true,
	}
	if !resume {
		req.System = s.system
	}
	out, err := s.ask.Ask(ctx, inst, req)
	if err != nil {
		s.forget(id)
		return answer{}, "", fmt.Errorf("the assistant could not be reached: %w", err)
	}
	if out.SessionID != "" {
		id = s.rename(id, out.SessionID)
	}
	var a answer
	if err := json.Unmarshal(out.JSON(), &a); err != nil {
		// A harness that ignored the schema still said something; showing it
		// beats an error.
		if t := strings.TrimSpace(out.Text); t != "" {
			return answer{Reply: t}, id, nil
		}
		return answer{}, "", errors.New("the assistant returned no usable answer")
	}
	return a, id, nil
}

func writeHistory(b *strings.Builder, h []Message) {
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	var lines []string
	for _, m := range h {
		t := clip(strings.TrimSpace(m.Text), 1500)
		if t == "" {
			continue
		}
		who := "Operator"
		if m.Role == "assistant" {
			who = "You"
		}
		lines = append(lines, who+": "+t)
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("[Earlier in this conversation]\n" + strings.Join(lines, "\n") + "\n\n")
}

func focusLine(f Focus) string {
	var parts []string
	if v := oneLine(f.View, 40); v != "" {
		parts = append(parts, "the "+v+" view")
	}
	if r := oneLine(f.RunID, 80); r != "" {
		parts = append(parts, "run "+r)
	}
	if a := oneLine(f.ArtifactID, 80); a != "" {
		parts = append(parts, "artifact "+a)
	}
	return strings.Join(parts, ", ")
}

func limit[T any](in []T, n int) []T {
	if len(in) > n {
		return in[:n]
	}
	return in
}

// clip cuts s to at most n runes.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// oneLine collapses whitespace and caps the length.
func oneLine(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// begin looks up (or opens) a conversation. It reports whether the harness has
// to resume an existing session rather than start one.
func (s *Service) begin(sessionID string) (string, session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if sess, ok := s.sessions[sessionID]; ok {
		sess.seen = s.now()
		return sessionID, *sess, true
	}
	// An unknown id (daemon restarted, session expired) starts over; the
	// history the app sent along carries the conversation into the new one.
	id := randid.UUID()
	s.sessions[id] = &session{seen: s.now()}
	return id, session{}, false
}

func (s *Service) mark(id string, digest [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[id]; ok {
		sess.state = digest
		sess.seen = s.now()
	}
}

func (s *Service) rename(from, to string) string {
	if from == to {
		return to
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[from]; ok {
		delete(s.sessions, from)
		s.sessions[to] = sess
	}
	return to
}

func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *Service) pruneLocked() {
	cutoff := s.now().Add(-sessionTTL)
	for id, sess := range s.sessions {
		if sess.seen.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}
