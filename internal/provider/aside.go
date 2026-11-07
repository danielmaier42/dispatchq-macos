package provider

import (
	"bytes"
	"encoding/json"
	"strings"
)

// An aside is a question dispatchq asks a harness on its own behalf rather than
// to carry out the operator's work: the prompt review that checks a task before
// it is queued, the assistant chat, and the feedback assistant that drafts a
// GitHub issue.
//
// It is not a small run. A run does the user's work in the user's directory
// with the user's tools; an aside is dispatchq talking to a model about dispatchq,
// so it gets no tools, no project configuration, and a directory of its own.
// Keeping the two apart is what stops a review of a prompt from being steered
// by the repository the prompt talks about.

// AsideRequest is one turn of such a conversation.
type AsideRequest struct {
	// Model is the model to answer with. Empty leaves the choice to the harness.
	Model string
	// System is the instruction set for the whole conversation. Harnesses record
	// it when a session starts and replay it on every resume, so it belongs to
	// the first turn; later turns leave it empty.
	System string
	// Text is what to say this turn.
	Text string
	// Schema is a JSON schema the answer must satisfy. Adapters that cannot
	// enforce one state the shape in System instead and parse what comes back.
	Schema string
	// SessionID identifies the conversation. DispatchQ issues it when the harness
	// accepts one; otherwise the adapter reports the harness's own id in the
	// answer and the caller sends that back.
	SessionID string
	// Resume continues the named session instead of starting it.
	Resume bool
	// Continues says another turn will follow, so the session has to outlive
	// this call. A single-question aside leaves it false and the harness is told
	// not to keep the conversation at all, which is what stops prompt review
	// from filling the operator's session history with throwaway turns.
	Continues bool
}

// Aside is what a harness answered.
type Aside struct {
	// SessionID is the conversation to send the next turn to. It may differ from
	// the requested one when the harness names its sessions itself.
	SessionID string
	// Text is the answer as the model wrote it.
	Text string
	// Structured is the answer as an object, set only when the harness itself
	// validated it against the requested schema. A caller that asked for JSON
	// still has to be able to read it out of Text.
	Structured json.RawMessage
}

// JSON is the answer as a JSON object: the validated one when the harness
// checked it, otherwise the first complete object in what the model wrote,
// which tolerates the code fence and the odd sentence models wrap it in. It is
// nil when the answer holds no object at all.
func (a Aside) JSON() []byte {
	if s := bytes.TrimSpace(a.Structured); len(s) > 0 && s[0] == '{' {
		return s
	}
	// Each '{' could open the object; the first one that decodes as a whole
	// object wins, so a brace in the prose before or after it does no harm.
	for i := strings.IndexByte(a.Text, '{'); i >= 0; {
		var obj json.RawMessage
		if err := json.NewDecoder(strings.NewReader(a.Text[i:])).Decode(&obj); err == nil {
			return obj
		}
		next := strings.IndexByte(a.Text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return nil
}

// AccountBlockedError is an aside the account refused before any work was
// done, for a reason a run would be refused for too: a used-up credit balance,
// say. It is how a ping learns what only a refused run could tell before.
type AccountBlockedError struct {
	Block Block
	// Detail is the harness's own words for the refusal.
	Detail string
}

func (e *AccountBlockedError) Error() string {
	return "the account refused the request: " + e.Detail
}
