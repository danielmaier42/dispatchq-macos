package claudecode

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// AsideCommand implements provider.Adapter. It is the narrowest invocation
// dispatchq makes of Claude Code: --tools "" removes every tool, --safe-mode
// drops CLAUDE.md, skills, plugins, hooks and MCP servers so the answer costs
// the same everywhere and cannot be steered by a project's own configuration,
// --strict-mcp-config and --disable-slash-commands close the remaining two
// doors, and --output-format json returns one object instead of a stream. A
// one-question aside adds --no-session-persistence, so it leaves nothing in the
// operator's session history.
//
// The system prompt goes with the first turn only. The CLI records it when a
// session starts and replays it on every resume, so sending it again would
// either be ignored or contradict what the session already believes.
func (a *Adapter) AsideCommand(inst provider.Instance, req provider.AsideRequest) (provider.Command, error) {
	if strings.TrimSpace(req.Text) == "" {
		return provider.Command{}, errors.New("claude-code: an aside needs something to say")
	}
	if req.SessionID == "" {
		return provider.Command{}, errors.New("claude-code: an aside needs a session id")
	}

	args := []string{"-p",
		"--tools", "",
		"--strict-mcp-config",
		"--disable-slash-commands",
		"--safe-mode",
		"--output-format", "json",
	}
	// A conversation that ends here is not worth keeping. One that continues
	// has to be: the next turn resumes it by id.
	if !req.Continues && !req.Resume {
		args = append(args, "--no-session-persistence")
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Schema != "" {
		args = append(args, "--json-schema", req.Schema)
	}
	if req.Resume {
		args = append(args, "--resume", req.SessionID)
	} else {
		args = append(args, "--session-id", req.SessionID)
		if req.System != "" {
			args = append(args, "--system-prompt", req.System)
		}
	}
	args = append(args, req.Text)

	return provider.Command{Path: a.binary(inst), Args: args, Env: a.env(inst)}, nil
}

// asideEnvelope is the part of `claude -p --output-format json` an aside reads.
type asideEnvelope struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	SessionID        string          `json:"session_id"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// ParseAside implements provider.Adapter.
func (a *Adapter) ParseAside(out []byte) (provider.Aside, error) {
	var env asideEnvelope
	if err := json.Unmarshal(out, &env); err != nil {
		return provider.Aside{}, fmt.Errorf("parse claude output: %w", err)
	}
	if env.IsError {
		detail := strings.TrimSpace(env.Result)
		if detail == "" {
			detail = env.Subtype
		}
		if block := refusalBlock(detail); block != "" {
			return provider.Aside{}, &provider.AccountBlockedError{Block: block, Detail: detail}
		}
		return provider.Aside{}, fmt.Errorf("claude reported an error: %s", detail)
	}
	return provider.Aside{
		SessionID:  env.SessionID,
		Text:       env.Result,
		Structured: env.StructuredOutput,
	}, nil
}

// refusalBlock names the account block an aside's error text reports: the API's
// own "Credit balance is too low", or a billing refusal that does not say why.
// The json envelope carries only the message, not the error name a run's
// stream has, so the words are all there is to go by.
func refusalBlock(detail string) provider.Block {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, creditExhaustedText):
		return provider.BlockCreditExhausted
	case strings.Contains(lower, "billing"):
		return provider.BlockBillingRefused
	}
	return ""
}
