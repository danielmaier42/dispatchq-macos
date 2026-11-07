package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielmaier42/claudeq/internal/feedback"
	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
)

// feedbackStatus tells the dashboard whether the guided feedback chat can run,
// and pre-fills the two environment details that go into the issue.
type feedbackStatus struct {
	// Available is false when no provider can answer; the dashboard then offers
	// the plain form instead of the chat.
	Available bool `json:"available"`
	// Reason explains an unavailable chat in one sentence.
	Reason string `json:"reason,omitempty"`
	// Repo is the "owner/name" the issue is filed against.
	Repo string `json:"repo"`
	// AppVersion is this build's version. It is appended to the issue body and
	// named in the review step, so the user knows what travels along.
	AppVersion string `json:"app_version"`
	// OSVersion names the system and its version ("macOS 15.6"), appended the
	// same way.
	OSVersion string `json:"os_version,omitempty"`
	// MaxTurns is how many messages the user may send before the assistant has
	// to deliver a draft.
	MaxTurns int `json:"max_turns"`
}

func (s *server) getFeedback(w http.ResponseWriter, _ *http.Request) {
	st := feedbackStatus{
		Repo:       update.DefaultRepo,
		AppVersion: version.String(),
		OSVersion:  s.osVersion(),
		MaxTurns:   feedback.MaxUserTurns,
	}
	_, _, why := s.feedbackTarget()
	switch {
	case s.d.Feedback == nil:
		st.Reason = "the feedback assistant is not available in this build"
	case why != "":
		st.Reason = why
	default:
		st.Available = true
	}
	writeJSON(w, http.StatusOK, st)
}

type feedbackTurnReq struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

func (s *server) feedbackTurn(w http.ResponseWriter, r *http.Request) {
	if s.d.Feedback == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("the feedback assistant is not available"))
		return
	}
	var req feedbackTurnReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	inst, model, why := s.feedbackTarget()
	if why != "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New(why))
		return
	}
	d, err := s.d.Feedback.Turn(r.Context(), inst, model, req.SessionID, req.Text)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type feedbackURLReq struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

// feedbackURL builds the prefilled GitHub "new issue" page from the text the
// user reviewed. Nothing is sent anywhere here: the dashboard opens the URL in
// the browser, where the user presses "Create" themselves.
func (s *server) feedbackURL(w http.ResponseWriter, r *http.Request) {
	var req feedbackURLReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The title is capped as well as collapsed: IssueURL can trim an oversized
	// body, but a pasted wall of text in the title would push the URL past
	// GitHub's request-URI limit with nothing left to give.
	title := oneLine(req.Title, 200)
	if title == "" {
		writeErr(w, http.StatusBadRequest, errors.New("the issue needs a title"))
		return
	}
	body := strings.TrimSpace(req.Body) + envFooter(version.String(), s.osVersion())
	writeJSON(w, http.StatusOK, map[string]string{
		"url": feedback.IssueURL(update.DefaultRepo, title, body, req.Labels),
	})
}

// envFooter renders the two environment details that ride along with the issue.
// They are stated in the review step and land in the prefilled page, where the
// user can still delete the line before pressing Create. An unknown value is
// simply left out.
func envFooter(app, osv string) string {
	var parts []string
	if app = oneLine(app, 60); app != "" {
		parts = append(parts, "DispatchQ "+app)
	}
	if osv = oneLine(osv, 60); osv != "" {
		parts = append(parts, osv)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n---\n" + strings.Join(parts, " · ")
}

// oneLine collapses whitespace and caps the length, so an edited environment
// field cannot smuggle extra Markdown into the issue.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > limit {
		s = strings.TrimSpace(string(r[:limit]))
	}
	return s
}

// osVersion reports the macOS product version, "" when the daemon cannot ask.
func (s *server) osVersion() string {
	if s.d.OSVersion == nil {
		return ""
	}
	return s.d.OSVersion()
}

// feedbackTarget resolves the provider instance and model a feedback turn runs
// on: the assistant's (see assistantTarget).
func (s *server) feedbackTarget() (provider.Instance, string, string) {
	return s.assistantTarget("draft an issue")
}
