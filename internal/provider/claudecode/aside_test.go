package claudecode

import (
	"errors"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// argValue returns the value following flag, and whether the flag is present.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestAsideIsSealedOff: an aside must not be able to touch the machine or read
// the project it is talking about. That is the whole reason it is a separate
// shape from a run.
func TestAsideIsSealedOff(t *testing.T) {
	a := &Adapter{detect: func() string { return "/usr/local/bin/claude" }}
	cmd, err := a.AsideCommand(provider.Instance{}, provider.AsideRequest{
		SessionID: "s-1", System: "be brief", Text: "check this", Model: "haiku",
	})
	if err != nil {
		t.Fatalf("AsideCommand: %v", err)
	}
	if tools, ok := argValue(cmd.Args, "--tools"); !ok || tools != "" {
		t.Fatalf("--tools = %q (present=%v), want it emptied", tools, ok)
	}
	for _, flag := range []string{"--safe-mode", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		if !hasArg(cmd.Args, flag) {
			t.Errorf("%s missing: the aside is not sealed off", flag)
		}
	}
	if hasArg(cmd.Args, "--dangerously-skip-permissions") {
		t.Fatal("an aside must never carry the skip-permissions flag")
	}
	if model, _ := argValue(cmd.Args, "--model"); model != "haiku" {
		t.Fatalf("--model = %q, want haiku", model)
	}
	if sys, _ := argValue(cmd.Args, "--system-prompt"); sys != "be brief" {
		t.Fatalf("--system-prompt = %q", sys)
	}
	if id, _ := argValue(cmd.Args, "--session-id"); id != "s-1" {
		t.Fatalf("--session-id = %q", id)
	}
	if cmd.Args[len(cmd.Args)-1] != "check this" {
		t.Fatalf("the message must be the last argument, got %q", cmd.Args[len(cmd.Args)-1])
	}
}

// TestAsideResumeDoesNotRepeatTheSystemPrompt: the CLI records the system prompt
// when a session starts and replays it on every resume, so sending it again is
// at best ignored and at worst a second, contradicting set of instructions.
func TestAsideThatContinuesKeepsItsSession(t *testing.T) {
	a := &Adapter{detect: func() string { return "claude" }}
	cmd, err := a.AsideCommand(provider.Instance{}, provider.AsideRequest{
		SessionID: "s-1", Text: "first", Continues: true,
	})
	if err != nil {
		t.Fatalf("AsideCommand: %v", err)
	}
	if hasArg(cmd.Args, "--no-session-persistence") {
		t.Fatal("a conversation that continues cannot discard its session")
	}
}

func TestAsideResumeDoesNotRepeatTheSystemPrompt(t *testing.T) {
	a := &Adapter{detect: func() string { return "claude" }}
	cmd, err := a.AsideCommand(provider.Instance{}, provider.AsideRequest{
		SessionID: "s-1", System: "be brief", Text: "and now?", Resume: true,
	})
	if err != nil {
		t.Fatalf("AsideCommand: %v", err)
	}
	if hasArg(cmd.Args, "--system-prompt") {
		t.Fatal("a resumed aside must not send the system prompt again")
	}
	if id, _ := argValue(cmd.Args, "--resume"); id != "s-1" {
		t.Fatalf("--resume = %q, want s-1", id)
	}
	if hasArg(cmd.Args, "--session-id") {
		t.Fatal("a resume names the session with --resume, not --session-id")
	}
}

// TestAsideCarriesTheInstancesAccount: an aside asks the same account a run
// would, so a second Claude instance answers as itself.
func TestAsideCarriesTheInstancesAccount(t *testing.T) {
	a := &Adapter{detect: func() string { return "claude" }}
	cmd, err := a.AsideCommand(provider.Instance{ConfigDir: "/tmp/acct"}, provider.AsideRequest{
		SessionID: "s-1", Text: "hello",
	})
	if err != nil {
		t.Fatalf("AsideCommand: %v", err)
	}
	if want := ConfigDirEnv + "=/tmp/acct"; len(cmd.Env) != 1 || cmd.Env[0] != want {
		t.Fatalf("env = %v, want %q", cmd.Env, want)
	}
}

func TestAsideNeedsAMessageAndASession(t *testing.T) {
	a := &Adapter{detect: func() string { return "claude" }}
	if _, err := a.AsideCommand(provider.Instance{}, provider.AsideRequest{SessionID: "s-1", Text: "  "}); err == nil {
		t.Error("an empty message should be refused")
	}
	if _, err := a.AsideCommand(provider.Instance{}, provider.AsideRequest{Text: "hi"}); err == nil {
		t.Error("a missing session id should be refused")
	}
}

func TestParseAsideReadsBothShapes(t *testing.T) {
	a := &Adapter{}
	got, err := a.ParseAside([]byte(`{"session_id":"s-9","result":"{\"ok\":true}","structured_output":{"ok":true}}`))
	if err != nil {
		t.Fatalf("ParseAside: %v", err)
	}
	if got.SessionID != "s-9" {
		t.Fatalf("session = %q, want s-9", got.SessionID)
	}
	if got.Text != `{"ok":true}` {
		t.Fatalf("text = %q", got.Text)
	}
	if string(got.Structured) != `{"ok":true}` {
		t.Fatalf("structured = %s", got.Structured)
	}
}

// TestParseAsideReportsTheHarnessesOwnFailure: an error the CLI reports inside a
// successful exit is still an error, and must not be handed on as an answer.
func TestParseAsideReportsTheHarnessesOwnFailure(t *testing.T) {
	a := &Adapter{}
	_, err := a.ParseAside([]byte(`{"is_error":true,"subtype":"error_max_turns","result":"out of turns"}`))
	if err == nil {
		t.Fatal("an is_error answer must not be treated as a result")
	}
	if !strings.Contains(err.Error(), "out of turns") {
		t.Fatalf("error does not say what happened: %v", err)
	}
	if _, err := a.ParseAside([]byte("not json")); err == nil {
		t.Fatal("unparseable output must be an error")
	}
}

// TestClaudeCodeClaimsAsides: the capability and the method have to agree, or a
// caller only finds out at the point of use.
func TestClaudeCodeClaimsAsides(t *testing.T) {
	if !New().Capabilities().Asides {
		t.Fatal("Claude Code implements asides, so it must claim the capability")
	}
}

// TestParseAsideNamesACreditRefusal: an API key without credit answers a ping
// with the envelope Claude Code prints for an empty account, and that has to
// read as the account block, not as just another error.
func TestParseAsideNamesACreditRefusal(t *testing.T) {
	a := &Adapter{}
	for _, tc := range []struct {
		out  string
		want provider.Block
	}{
		{`{"type":"result","subtype":"success","is_error":true,"result":"Credit balance is too low","terminal_reason":"api_error","api_error_status":400}`, provider.BlockCreditExhausted},
		{`{"type":"result","subtype":"success","is_error":true,"result":"billing_error: your request was refused"}`, provider.BlockBillingRefused},
	} {
		_, err := a.ParseAside([]byte(tc.out))
		var blocked *provider.AccountBlockedError
		if !errors.As(err, &blocked) || blocked.Block != tc.want {
			t.Fatalf("ParseAside(%s) = %v, want block %q", tc.out, err, tc.want)
		}
	}
	_, err := a.ParseAside([]byte(`{"is_error":true,"result":"out of turns"}`))
	var blocked *provider.AccountBlockedError
	if errors.As(err, &blocked) {
		t.Fatalf("an ordinary error read as an account block: %v", err)
	}
}
