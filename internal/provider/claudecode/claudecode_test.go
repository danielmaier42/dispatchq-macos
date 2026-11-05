package claudecode

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

func instance() provider.Instance {
	return provider.Instance{
		ID:      provider.DefaultInstanceID,
		Kind:    provider.KindClaudeCode,
		Name:    provider.DefaultInstanceName,
		Enabled: true,
	}
}

// TestCommandGoldenArgs pins the exact invocation dispatchq has always produced.
// Moving Claude execution behind an adapter must not change a single argument,
// so these are compared whole rather than by substring.
func TestCommandGoldenArgs(t *testing.T) {
	const sys = "SYSTEM"
	const prompt = "do the thing"

	tests := []struct {
		name string
		inst provider.Instance
		req  provider.Request
		want []string
	}{
		{
			name: "fresh run, provider default access, no model",
			inst: instance(),
			req: provider.Request{
				Prompt: prompt, SessionID: "SID",
				AccessMode: provider.AccessProviderDefault, SystemPrompt: sys,
			},
			want: []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--session-id", "SID",
				"--append-system-prompt", sys,
				prompt,
			},
		},
		{
			name: "model override",
			inst: instance(),
			req: provider.Request{
				Prompt: prompt, SessionID: "SID", Model: "claude-opus-4-8",
				AccessMode: provider.AccessProviderDefault, SystemPrompt: sys,
			},
			want: []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--model", "claude-opus-4-8",
				"--session-id", "SID",
				"--append-system-prompt", sys,
				prompt,
			},
		},
		{
			name: "full access bypasses the permission prompts",
			inst: instance(),
			req: provider.Request{
				Prompt: prompt, SessionID: "SID", Model: "claude-haiku-4-5-20251001",
				AccessMode: provider.AccessFullAccess, SystemPrompt: sys,
			},
			want: []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--model", "claude-haiku-4-5-20251001",
				"--dangerously-skip-permissions",
				"--session-id", "SID",
				"--append-system-prompt", sys,
				prompt,
			},
		},
		{
			name: "resume continues the session instead of assigning one",
			inst: instance(),
			req: provider.Request{
				Prompt: prompt, SessionID: "SID", Resume: true,
				AccessMode: provider.AccessProviderDefault, SystemPrompt: sys,
			},
			want: []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--resume", "SID",
				"--append-system-prompt", sys,
				prompt,
			},
		},
	}

	a := New()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := a.Command(tc.inst, tc.req)
			if err != nil {
				t.Fatalf("Command: %v", err)
			}
			if !reflect.DeepEqual(cmd.Args, tc.want) {
				t.Fatalf("args =\n  %q\nwant\n  %q", cmd.Args, tc.want)
			}
			if len(cmd.Env) != 0 {
				t.Fatalf("env = %q, want none for an instance without a config dir", cmd.Env)
			}
		})
	}
}

func TestCommandForwardsEffortOnFreshAndResumedSessions(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		for _, resume := range []bool{false, true} {
			t.Run(effort+"/"+map[bool]string{false: "fresh", true: "resume"}[resume], func(t *testing.T) {
				cmd, err := New().Command(instance(), provider.Request{
					Prompt: "task", SessionID: "sid", Resume: resume, ReasoningEffort: effort,
				})
				if err != nil {
					t.Fatalf("Command: %v", err)
				}
				index := slices.Index(cmd.Args, "--effort")
				if effort == "" {
					if index != -1 {
						t.Fatal("an empty effort must preserve the CLI default")
					}
				} else if index == -1 || index+1 >= len(cmd.Args) || cmd.Args[index+1] != effort {
					t.Fatalf("effort %q missing from args %q", effort, cmd.Args)
				}
			})
		}
	}
}

func TestCommandOmitsAnEmptyModelAndAccessFlag(t *testing.T) {
	// Compare whole arguments: the appended system prompt legitimately mentions
	// "--model" when it documents the queue overrides.
	cmd, err := New().Command(instance(), provider.Request{
		Prompt: "p", SessionID: "S", SystemPrompt: "mentions --model and --dangerously-skip-permissions",
		AccessMode: provider.AccessProviderDefault,
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	isFlag := func(name string) func(string) bool {
		return func(a string) bool { return a == name || strings.HasPrefix(a, name+"=") }
	}
	if slices.ContainsFunc(cmd.Args, isFlag("--model")) {
		t.Fatalf("no model should be passed when empty, got %q", cmd.Args)
	}
	if slices.ContainsFunc(cmd.Args, isFlag("--dangerously-skip-permissions")) {
		t.Fatalf("skip should not be set by default, got %q", cmd.Args)
	}
}

func TestCommandPassesTheInstanceConfigDirectory(t *testing.T) {
	// A second Claude subscription is a second instance with its own
	// configuration directory; that is how its account and sessions stay apart.
	inst := instance()
	inst.ID = "claude-secondary"
	inst.ConfigDir = "/tmp/claude-secondary"
	cmd, err := New().Command(inst, provider.Request{Prompt: "p", SessionID: "S"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	want := ConfigDirEnv + "=/tmp/claude-secondary"
	if !slices.Contains(cmd.Env, want) {
		t.Fatalf("env = %q, want it to contain %q", cmd.Env, want)
	}
}

func TestCommandRefusesAccessModesItCannotEnforce(t *testing.T) {
	for _, mode := range []provider.AccessMode{provider.AccessReadOnly, provider.AccessWorkspaceWrite} {
		_, err := New().Command(instance(), provider.Request{Prompt: "p", SessionID: "S", AccessMode: mode})
		if !errors.Is(err, provider.ErrUnsupported) {
			t.Fatalf("Command(%q) err = %v, want provider.ErrUnsupported", mode, err)
		}
	}
}

func TestCommandRefusesARunWithoutASessionID(t *testing.T) {
	if _, err := New().Command(instance(), provider.Request{Prompt: "p"}); err == nil {
		t.Fatal("a run without a session id must be refused, not started unresumable")
	}
}

func TestBinaryPrefersTheInstancePathThenDetection(t *testing.T) {
	a := &Adapter{detect: func() string { return "/detected/claude" }}
	inst := instance()
	inst.BinaryPath = "/configured/claude"
	if got := a.binary(inst); got != "/configured/claude" {
		t.Fatalf("binary = %q, want the configured path", got)
	}
	if got := a.binary(instance()); got != "/detected/claude" {
		t.Fatalf("binary = %q, want the detected path", got)
	}

	none := &Adapter{detect: func() string { return "" }}
	if got := none.binary(instance()); got != BinaryName {
		t.Fatalf("binary = %q, want the bare %q for the exec lookup to try", got, BinaryName)
	}
}

func TestDetectBinaryProbesAtMostOnce(t *testing.T) {
	// Locating the CLI sources the user's shell profile, so it must happen once
	// per process and never again before a run — including when it found
	// nothing, which is the expensive case.
	calls := 0
	found := &Adapter{detect: func() string { calls++; return "/opt/claude" }}
	for range 3 {
		if got := found.DetectBinary(); got != "/opt/claude" {
			t.Fatalf("DetectBinary() = %q, want /opt/claude", got)
		}
	}
	if calls != 1 {
		t.Fatalf("probed %d times, want 1", calls)
	}

	misses := 0
	missing := &Adapter{detect: func() string { misses++; return "" }}
	for range 3 {
		if got := missing.DetectBinary(); got != "" {
			t.Fatalf("DetectBinary() = %q, want empty", got)
		}
	}
	if misses != 1 {
		t.Fatalf("probed %d times after a miss, want 1", misses)
	}
}

func TestCapabilitiesDoNotClaimSandboxesClaudeCodeCannotEnforce(t *testing.T) {
	caps := New().Capabilities()
	for _, want := range []provider.AccessMode{provider.AccessProviderDefault, provider.AccessFullAccess} {
		if !caps.SupportsAccess(want) {
			t.Fatalf("capabilities must include %q", want)
		}
	}
	for _, unsupported := range []provider.AccessMode{provider.AccessReadOnly, provider.AccessWorkspaceWrite} {
		if caps.SupportsAccess(unsupported) {
			t.Fatalf("capabilities must not claim %q", unsupported)
		}
	}
	if !caps.ReasoningEffort {
		t.Fatal("Claude Code accepts a per-session effort setting")
	}
	if !caps.SessionResume || !caps.StructuredOutput || !caps.RateLimitResume {
		t.Fatalf("capabilities understate the adapter: %+v", caps)
	}
}

// TestParseTranslatesStreamJSON covers every line shape dispatchq classifies,
// including the ones that must *not* change the outcome.
func TestParseTranslatesStreamJSON(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []provider.Event
	}{
		{
			name: "non-json line is ignored",
			line: "not json at all",
			want: nil,
		},
		{
			name: "session id is reported",
			line: `{"type":"system","subtype":"init","session_id":"real-sid"}`,
			want: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "real-sid"}},
		},
		{
			name: "the run's init names the model that answers",
			line: `{"type":"system","subtype":"init","session_id":"real-sid","model":"claude-opus-5-5"}`,
			want: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "real-sid", Model: "claude-opus-5-5"}},
		},
		{
			name: "a subagent's init does not name the run's model",
			line: `{"type":"system","subtype":"init","session_id":"real-sid","model":"claude-haiku-5-5","parent_tool_use_id":"toolu_1"}`,
			want: []provider.Event{{Type: provider.EventSessionStarted, SessionID: "real-sid"}},
		},
		{
			name: "successful result carries the final output and metrics",
			line: `{"type":"result","is_error":false,"result":"All done.","total_cost_usd":0.012,"num_turns":2,"duration_ms":3400,"usage":{"input_tokens":1200,"output_tokens":300}}`,
			want: []provider.Event{{
				Type:        provider.EventCompleted,
				FinalOutput: "All done.",
				Metrics: &provider.Metrics{
					CostUSD: 0.012, NumTurns: 2, DurationMS: 3400,
					InputTokens: 1200, OutputTokens: 300,
				},
			}},
		},
		{
			name: "api retry with a 429 and a retry delay",
			line: `{"type":"system","subtype":"api_retry","error_status":429,"error":"rate_limit","retry_delay_ms":5000}`,
			want: []provider.Event{{Type: provider.EventRateLimited, RetryAfter: 5 * time.Second}},
		},
		{
			name: "rejected rate-limit event alone is a rate limit",
			line: `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784655600}}`,
			want: []provider.Event{{Type: provider.EventRateLimited, ResetAt: time.Unix(1784655600, 0)}},
		},
		{
			name: "allowed rate-limit event is only a warning",
			line: `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`,
			want: nil,
		},
		{
			name: "authentication failure",
			line: `{"type":"result","subtype":"error","is_error":true,"error":"authentication_failed"}`,
			want: []provider.Event{
				{Type: provider.EventAuthFailed},
				{Type: provider.EventCompleted, IsError: true, Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "401 is an authentication failure too",
			line: `{"type":"system","api_error_status":401}`,
			want: []provider.Event{{Type: provider.EventAuthFailed}},
		},
		{
			name: "an organisation that disabled subscription access is a limit, not an auth error",
			line: `{"type":"assistant","session_id":"sid","error":"oauth_org_not_allowed","is_api_error_message":true,"api_error_code":"oauth_not_allowed_for_organization"}`,
			want: []provider.Event{
				{Type: provider.EventSessionStarted, SessionID: "sid"},
				{Type: provider.EventRateLimited, RetryAfter: AccountBlockedRetryAfter, Block: provider.BlockOrgDisallowed, Detail: orgBlockedDetail},
			},
		},
		{
			name: "the result envelope of an organisation block pauses the provider too",
			line: `{"type":"result","is_error":true,"api_error_status":403,"api_error_code":"oauth_not_allowed_for_organization","result":"Your organization has disabled Claude subscription access for Claude Code"}`,
			want: []provider.Event{
				{Type: provider.EventRateLimited, RetryAfter: AccountBlockedRetryAfter, Block: provider.BlockOrgDisallowed, Detail: orgBlockedDetail},
				{Type: provider.EventCompleted, IsError: true, FinalOutput: "Your organization has disabled Claude subscription access for Claude Code", Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "an organisation block refused as a 401 is still a limit, not a login problem",
			line: `{"type":"result","is_error":true,"api_error_status":401,"api_error_code":"oauth_not_allowed_for_organization"}`,
			want: []provider.Event{
				{Type: provider.EventRateLimited, RetryAfter: AccountBlockedRetryAfter, Block: provider.BlockOrgDisallowed, Detail: orgBlockedDetail},
				{Type: provider.EventCompleted, IsError: true, Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "a used-up credit balance is a limit, not a plain failure",
			line: `{"type":"result","subtype":"success","is_error":true,"api_error_status":400,"terminal_reason":"api_error","result":"Credit balance is too low"}`,
			want: []provider.Event{
				{Type: provider.EventRateLimited, RetryAfter: AccountBlockedRetryAfter, Block: provider.BlockCreditExhausted, Detail: creditExhaustedDetail},
				{Type: provider.EventCompleted, IsError: true, FinalOutput: "Credit balance is too low", Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "the billing error on the assistant line pauses the provider too",
			line: `{"type":"assistant","session_id":"sid","error":"billing_error","is_api_error_message":true}`,
			want: []provider.Event{
				{Type: provider.EventSessionStarted, SessionID: "sid"},
				{Type: provider.EventRateLimited, RetryAfter: AccountBlockedRetryAfter, Block: provider.BlockCreditExhausted, Detail: creditExhaustedDetail},
			},
		},
		{
			name: "a successful answer that quotes the credit message is still a success",
			line: `{"type":"result","subtype":"success","is_error":false,"result":"The API said: Credit balance is too low"}`,
			want: []provider.Event{
				{Type: provider.EventCompleted, FinalOutput: "The API said: Credit balance is too low", Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "any other 400 stays a plain failure",
			line: `{"type":"result","subtype":"success","is_error":true,"api_error_status":400,"result":"invalid_request_error: prompt is too long"}`,
			want: []provider.Event{
				{Type: provider.EventCompleted, IsError: true, FinalOutput: "invalid_request_error: prompt is too long", Metrics: &provider.Metrics{}},
			},
		},
		{
			name: "a rate-limited final result reports both",
			line: `{"type":"result","is_error":true,"api_error_status":429,"result":"You've hit your session limit"}`,
			want: []provider.Event{
				{Type: provider.EventRateLimited},
				{Type: provider.EventCompleted, IsError: true, FinalOutput: "You've hit your session limit", Metrics: &provider.Metrics{}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := New().NewParser().Parse([]byte(tc.line))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse(%s) =\n  %+v\nwant\n  %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestParserRemembersTheResetTimeAcrossLines(t *testing.T) {
	// The CLI can report the window's reset time on an approaching-limit event
	// and only reject the request later. The rejection must still name the real
	// reset time, or the engine falls back to a blind backoff.
	p := New().NewParser()
	if got := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`)); got != nil {
		t.Fatalf("an allowed event must produce no event, got %+v", got)
	}
	got := p.Parse([]byte(`{"type":"result","is_error":true,"api_error_status":429}`))
	if len(got) == 0 || got[0].Type != provider.EventRateLimited {
		t.Fatalf("expected a rate-limit event, got %+v", got)
	}
	if want := time.Unix(1784655600, 0); !got[0].ResetAt.Equal(want) {
		t.Fatalf("ResetAt = %v, want %v carried over from the earlier event", got[0].ResetAt, want)
	}
}

func TestParserIsPerRun(t *testing.T) {
	// Two runs must not share remembered rate-limit timing.
	a := New()
	first := a.NewParser()
	first.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`))
	second := a.NewParser()
	got := second.Parse([]byte(`{"type":"result","is_error":true,"api_error_status":429}`))
	if len(got) == 0 || !got[0].ResetAt.IsZero() {
		t.Fatalf("a fresh parser must not inherit the previous run's reset time: %+v", got)
	}
}

func TestKindIsTheRegistryKey(t *testing.T) {
	if got := New().Kind(); got != provider.KindClaudeCode {
		t.Fatalf("Kind() = %q, want %q", got, provider.KindClaudeCode)
	}
}

func TestParserPicksUpARateLimitWindowReportedAfterTheRejection(t *testing.T) {
	// The CLI may reject first and describe the allowance window afterwards. The
	// reset time must still reach the engine, or it waits a blind backoff
	// instead of resuming when the window actually reopens.
	p := New().NewParser()
	first := p.Parse([]byte(`{"type":"system","subtype":"api_retry","error_status":429,"error":"rate_limit"}`))
	if len(first) != 1 || first[0].Type != provider.EventRateLimited || !first[0].ResetAt.IsZero() {
		t.Fatalf("first line = %+v, want a rate limit with no window yet", first)
	}
	second := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`))
	if len(second) != 1 || second[0].Type != provider.EventRateLimited {
		t.Fatalf("second line = %+v, want the rate limit restated with its window", second)
	}
	if want := time.Unix(1784655600, 0); !second[0].ResetAt.Equal(want) {
		t.Fatalf("ResetAt = %v, want %v", second[0].ResetAt, want)
	}
}

func TestParserDoesNotInventARateLimitFromWindowInformationAlone(t *testing.T) {
	// Window details without any rejection are just an approaching-limit notice.
	p := New().NewParser()
	p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`))
	if got := p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784659200}}`)); got != nil {
		t.Fatalf("got %+v, want no event while nothing was ever rejected", got)
	}
}

func TestParserRepeatedTimingDoesNotRestateTheLimit(t *testing.T) {
	// Unchanged timing is not news; only a refinement is re-reported.
	p := New().NewParser()
	p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784655600}}`))
	if got := p.Parse([]byte(`{"type":"assistant","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`)); got != nil {
		t.Fatalf("got %+v, want nothing for an unchanged window", got)
	}
}

func TestParserKeepsAReportedWindowOverTheOrgBlockFallback(t *testing.T) {
	// An organisation block names no window, so dispatchq waits a fixed hour. A
	// real rate limit that arrived first does name one, and that is the better
	// answer: the fallback must not overwrite it.
	p := New().NewParser()
	p.Parse([]byte(`{"type":"system","subtype":"api_retry","error_status":429,"error":"rate_limit","retry_delay_ms":5000}`))
	got := p.Parse([]byte(`{"type":"result","is_error":true,"api_error_status":403,"api_error_code":"oauth_not_allowed_for_organization"}`))
	if len(got) != 2 || got[0].Type != provider.EventRateLimited {
		t.Fatalf("got %+v, want a rate limit and the result", got)
	}
	if got[0].RetryAfter != 5*time.Second {
		t.Fatalf("RetryAfter = %v, want the 5s the CLI reported", got[0].RetryAfter)
	}
}

func TestParserDoesNotShortenTheOrgBlockWithAnUnrelatedBackoff(t *testing.T) {
	// A transient API error carries its own retry delay. It says nothing about
	// an allowance, so the organisation block must still pause for its full
	// hour — a one-second pause would have the queue burning runs in a loop.
	p := New().NewParser()
	if got := p.Parse([]byte(`{"type":"system","subtype":"api_retry","error_status":529,"retry_delay_ms":1000}`)); got != nil {
		t.Fatalf("got %+v, want nothing for a retry that reported no limit", got)
	}
	got := p.Parse([]byte(`{"type":"result","is_error":true,"api_error_status":403,"api_error_code":"oauth_not_allowed_for_organization"}`))
	if len(got) != 2 || got[0].Type != provider.EventRateLimited {
		t.Fatalf("got %+v, want a pause and the result", got)
	}
	if got[0].RetryAfter != AccountBlockedRetryAfter || !got[0].ResetAt.IsZero() {
		t.Fatalf("pause = %+v, want the full %v and no window", got[0], AccountBlockedRetryAfter)
	}
}

func TestParserIgnoresAnApproachingWindowForAnOrgBlock(t *testing.T) {
	// The reset time of an allowance the run never exhausted is not when the
	// organisation block lifts, and the engine prefers an absolute time over a
	// delay — so carrying it over would reopen the provider far too early.
	p := New().NewParser()
	p.Parse([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600}}`))
	got := p.Parse([]byte(`{"type":"assistant","error":"oauth_org_not_allowed"}`))
	if len(got) != 1 || got[0].Type != provider.EventRateLimited {
		t.Fatalf("got %+v, want the organisation block as a pause", got)
	}
	if !got[0].ResetAt.IsZero() || got[0].RetryAfter != AccountBlockedRetryAfter {
		t.Fatalf("pause = %+v, want the fixed hour and no window", got[0])
	}
}

func TestParserDoesNotShortenACreditPauseWithAnUnrelatedBackoff(t *testing.T) {
	// Credit runs out without any allowance window, so a transient retry delay
	// seen earlier must not turn the hour into a one-second retry loop.
	p := New().NewParser()
	p.Parse([]byte(`{"type":"system","subtype":"api_retry","error_status":529,"retry_delay_ms":1000}`))
	got := p.Parse([]byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":400,"result":"Credit balance is too low"}`))
	if len(got) != 2 || got[0].Type != provider.EventRateLimited {
		t.Fatalf("got %+v, want a pause and the result", got)
	}
	if got[0].RetryAfter != AccountBlockedRetryAfter || !got[0].ResetAt.IsZero() {
		t.Fatalf("pause = %+v, want the full %v and no window", got[0], AccountBlockedRetryAfter)
	}
}
