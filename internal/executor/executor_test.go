package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/claudecode"
	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

func sampleTask() task.Task {
	return task.Task{
		ID: "t1", Name: "t1", Prompt: "do the thing",
		WorkingDir: ".", Trigger: task.TriggerASAP,
		Enabled: true, Permissions: task.PermissionsDefault,
	}
}

// claudeInstance is the provider instance every migrated dispatchq configuration
// has, pointed at a binary of the test's choosing.
func claudeInstance(bin string) provider.Instance {
	return provider.Instance{
		ID:         provider.DefaultInstanceID,
		Kind:       provider.KindClaudeCode,
		Name:       provider.DefaultInstanceName,
		BinaryPath: bin,
		Enabled:    true,
	}
}

// claudeExecutor returns an executor whose registry holds the real Claude Code
// adapter; the binary is chosen per request through the provider instance.
func claudeExecutor() *Executor {
	return &Executor{Registry: provider.NewRegistry(claudecode.New())}
}

func TestSystemPromptIsAppendedAsOneValue(t *testing.T) {
	e := claudeExecutor()
	tk := sampleTask()
	cmd, err := e.Command(Request{Task: tk, Provider: claudeInstance(""), SessionID: "SID"})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	args := cmd.Args
	if args[len(args)-1] != tk.Prompt {
		t.Fatalf("prompt must be the last arg, got %q", args[len(args)-1])
	}
	if args[len(args)-3] != "--append-system-prompt" {
		t.Fatalf("expected --append-system-prompt before the prompt, got %v", args[len(args)-3:])
	}
	if got := args[len(args)-2]; got != builtinSystemPrompt {
		t.Fatalf("append-system-prompt value = %q, want the built-in prompt", got)
	}
	if !strings.Contains(selfQueueSystemPrompt, "queue --prompt") {
		t.Fatal("self-queue prompt must document the `queue --prompt` command")
	}
	for _, f := range []string{"--model <name>", "--parallel=true|false", "--skip-permissions=true|false", "--notify=true|false", "--quiet-history=true|false"} {
		if !strings.Contains(selfQueueSystemPrompt, f) {
			t.Fatalf("self-queue prompt must document the %s override", f)
		}
	}
	if !strings.Contains(builtinSystemPrompt, "publish --file") {
		t.Fatal("built-in prompt must document the `publish --file` command")
	}
	if !strings.Contains(builtinSystemPrompt, "notify --title") {
		t.Fatal("built-in prompt must document the `notify --title` command")
	}
}

func TestBuiltinSystemPromptFramesTheHeadlessRun(t *testing.T) {
	// The headless framing must come first: everything else in the built-in
	// prompt (queueing, publishing, notifying) assumes the run is unattended and
	// ends when the harness stops writing.
	if !strings.HasPrefix(builtinSystemPrompt, headlessSystemPrompt) {
		t.Fatal("the headless framing must open the built-in prompt")
	}
	for _, want := range []string{
		"no next turn",          // there is nobody to hand work back to
		"ScheduleWakeup",        // deferring to a later turn does not work
		"Monitor",               // nor does leaving something in the background
		"waiting for something", // so a run must not end by announcing a wait
		"ids and links",         // unfinished work is named concretely instead
		"whole disk",            // no open-ended scans that raise privacy prompts
		"a path the task",       // while named paths (e.g. OneDrive files) stay allowed
	} {
		if !strings.Contains(headlessSystemPrompt, want) {
			t.Fatalf("headless prompt must mention %q", want)
		}
	}
	// It points at the self-queue block for the "stop and continue later" route,
	// which therefore has to follow it rather than precede it.
	if strings.Index(builtinSystemPrompt, "queue --prompt") < strings.Index(builtinSystemPrompt, "queue a follow-up dispatchq task") {
		t.Fatal("the self-queue instructions must follow the headless framing that refers to them")
	}
}

func TestCommandAppendsCustomSystemPrompt(t *testing.T) {
	e := claudeExecutor()
	tk := sampleTask()
	const custom = "Always run the tests before finishing."
	cmd, err := e.Command(Request{Task: tk, Provider: claudeInstance(""), SessionID: "SID", CustomSystemPrompt: custom})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	args := cmd.Args

	// Still a single --append-system-prompt value, right before the prompt.
	if args[len(args)-1] != tk.Prompt {
		t.Fatalf("prompt must be the last arg, got %q", args[len(args)-1])
	}
	if args[len(args)-3] != "--append-system-prompt" {
		t.Fatalf("expected --append-system-prompt before the prompt, got %v", args[len(args)-3:])
	}
	got := args[len(args)-2]
	// Built-in first, custom last.
	if !strings.HasPrefix(got, builtinSystemPrompt) {
		t.Fatal("built-in prompt must come first")
	}
	if !strings.HasSuffix(got, custom) {
		t.Fatalf("custom prompt must come last, got %q", got)
	}
	if !strings.Contains(got, customSystemPromptIntro) {
		t.Fatal("custom prompt must be introduced by customSystemPromptIntro")
	}
}

func TestSystemPromptBlankCustomIsUnchanged(t *testing.T) {
	// A blank or whitespace-only custom prompt must not alter the built-in value,
	// so runs without one behave exactly as before.
	for _, custom := range []string{"", "   ", "\n\t "} {
		if got := systemPrompt(custom); got != builtinSystemPrompt {
			t.Fatalf("systemPrompt(%q) = %q, want the unmodified built-in prompt", custom, got)
		}
	}
}

func TestRunEnvCarriesSelfQueueContext(t *testing.T) {
	e := &Executor{Home: "/data/home", QueueBin: "/opt/dispatchq"}
	tk := sampleTask()
	tk.Model = "claude-opus-4-8"
	tk.Permissions = task.PermissionsSkip
	tk.Parallel = true

	got := envMap(e.runEnv(Request{Task: tk}, nil))
	if got[store.EnvHome] != "/data/home" {
		t.Fatalf("%s = %q, want /data/home", store.EnvHome, got[store.EnvHome])
	}
	if got[EnvQueueBin] != "/opt/dispatchq" {
		t.Fatalf("%s = %q, want /opt/dispatchq", EnvQueueBin, got[EnvQueueBin])
	}
	var parent task.Task
	if err := json.Unmarshal([]byte(got[EnvParentTask]), &parent); err != nil {
		t.Fatalf("parent task env is not valid JSON: %v", err)
	}
	if parent.Model != "claude-opus-4-8" || parent.Permissions != task.PermissionsSkip || !parent.Parallel {
		t.Fatalf("parent task did not round-trip inheritable settings: %+v", parent)
	}
}

// TestRunEnvNamesTheProviderTheRunIsOn: a task that names no provider still runs
// on one, and a follow-up it queues has to inherit that account — not whatever
// the default has become by the time the follow-up starts.
func TestRunEnvNamesTheProviderTheRunIsOn(t *testing.T) {
	e := &Executor{}
	tk := sampleTask()
	tk.Provider = "" // "whatever the default is"
	got := envMap(e.runEnv(Request{Task: tk, Provider: provider.Instance{ID: "codex"}}, nil))

	var parent task.Task
	if err := json.Unmarshal([]byte(got[EnvParentTask]), &parent); err != nil {
		t.Fatalf("parent task env is not valid JSON: %v", err)
	}
	if parent.Provider != "codex" {
		t.Fatalf("parent provider = %q, want the instance this run is on", parent.Provider)
	}
}

// TestRunEnvKeepsAnExplicitProvider: a task that names its provider keeps it,
// resolved instance or not.
func TestRunEnvKeepsAnExplicitProvider(t *testing.T) {
	e := &Executor{}
	tk := sampleTask()
	tk.Provider = "claude-work"
	got := envMap(e.runEnv(Request{Task: tk, Provider: provider.Instance{ID: "claude-work"}}, nil))

	var parent task.Task
	if err := json.Unmarshal([]byte(got[EnvParentTask]), &parent); err != nil {
		t.Fatalf("parent task env is not valid JSON: %v", err)
	}
	if parent.Provider != "claude-work" {
		t.Fatalf("parent provider = %q", parent.Provider)
	}
}

func TestRunEnvCarriesRunAndTaskIDs(t *testing.T) {
	e := &Executor{}
	tk := sampleTask() // sampleTask sets a non-empty ID
	got := envMap(e.runEnv(Request{Task: tk, RunID: "20260721T030000-abcd"}, nil))
	if got[EnvRunID] != "20260721T030000-abcd" {
		t.Fatalf("%s = %q, want the run id", EnvRunID, got[EnvRunID])
	}
	if got[EnvTaskID] != tk.ID {
		t.Fatalf("%s = %q, want %q", EnvTaskID, got[EnvTaskID], tk.ID)
	}
}

func TestRunEnvOmitsUnsetRunID(t *testing.T) {
	e := &Executor{}
	env := e.runEnv(Request{Task: sampleTask()}, nil) // no RunID
	if strings.Contains(strings.Join(env, "\n"), EnvRunID+"=") {
		t.Fatalf("%s must be omitted when RunID is unset", EnvRunID)
	}
}

func TestRunEnvOmitsUnsetPaths(t *testing.T) {
	e := &Executor{} // no Home / QueueBin configured
	env := e.runEnv(Request{Task: sampleTask()}, nil)
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, store.EnvHome+"=") {
		t.Fatal("DISPATCHQ_HOME must be omitted when Home is unset")
	}
	if strings.Contains(joined, EnvQueueBin+"=") {
		t.Fatal("DISPATCHQ_BIN must be omitted when QueueBin is unset")
	}
	if !strings.Contains(joined, EnvParentTask+"=") {
		t.Fatal("the parent task should always be exported")
	}
}

func TestRunEnvAppliesAdapterEnvLast(t *testing.T) {
	// An adapter's own variables (a configuration directory, say) must win over
	// whatever the daemon inherited under the same name.
	t.Setenv("PROVIDER_CONFIG_DIR", "/inherited")
	e := &Executor{}
	env := e.runEnv(Request{Task: sampleTask()}, []string{"PROVIDER_CONFIG_DIR=/instance"})
	if got := envMap(env)["PROVIDER_CONFIG_DIR"]; got != "/instance" {
		t.Fatalf("adapter env = %q, want the adapter's value to win", got)
	}
}

// envMap collapses an environment slice the way exec does: a later duplicate
// wins.
func envMap(env []string) map[string]string {
	got := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			got[k] = v
		}
	}
	return got
}

// fakeClaude writes an executable script to a temp dir that prints the given
// stdout lines and exits with exitCode, standing in for the real CLI.
func fakeClaude(t *testing.T, stdout string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binary is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncat <<'EOF'\n" + stdout + "\nEOF\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func runFake(t *testing.T, stdout string, exitCode int) provider.Result {
	t.Helper()
	bin := fakeClaude(t, stdout, exitCode)
	e := claudeExecutor()
	var log bytes.Buffer
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	res, err := e.Run(context.Background(), Request{
		Task: tk, Provider: claudeInstance(bin), SessionID: "assigned-sid", Log: &log,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if log.Len() == 0 {
		t.Fatal("expected output to be streamed to the log")
	}
	return res
}

func TestRunUsesTheInstanceBinary(t *testing.T) {
	// The registry's adapter would detect or fall back to a bare name; the
	// instance's configured path is what must actually be executed.
	out := `{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"real-sid"}`
	res := runFake(t, out, 0)
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %q, want success (the instance binary should have run)", res.Status)
	}
}

func TestRunDeliversEffortToClaudeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binary is POSIX-only")
	}
	for _, resume := range []bool{false, true} {
		t.Run(strconv.FormatBool(resume), func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "claude")
			script := `#!/bin/sh
printf '%s\n' "$@" > args.txt
printf '%s\n' '{"type":"result","is_error":false,"result":"OK","session_id":"assigned-sid"}'
`
			if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			tk := sampleTask()
			tk.WorkingDir = dir
			res, err := claudeExecutor().Run(context.Background(), Request{
				Task: tk, Provider: claudeInstance(bin), SessionID: "assigned-sid",
				Resume: resume, ReasoningEffort: "medium",
			})
			if err != nil || res.Status != store.StatusSuccess {
				t.Fatalf("Run = %+v, %v", res, err)
			}
			args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "--effort\nmedium\n") {
				t.Fatalf("Claude binary received no effort setting: %s", args)
			}
		})
	}
}

func TestRunRejectsAnUnregisteredProviderKind(t *testing.T) {
	e := &Executor{Registry: provider.NewRegistry(claudecode.New())}
	inst := claudeInstance("/nonexistent/claude")
	inst.Kind = "gremlin"
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	_, err := e.Run(context.Background(), Request{Task: tk, Provider: inst, SessionID: "sid"})
	if err == nil {
		t.Fatal("a provider whose kind has no adapter must not run")
	}
	if !strings.Contains(err.Error(), "gremlin") {
		t.Fatalf("error %q should name the unregistered kind", err)
	}
}

func TestRunRejectsAnAccessModeTheProviderCannotEnforce(t *testing.T) {
	// Claude Code has no read-only sandbox flag, so dispatchq must refuse the run
	// rather than start one that silently has more authority than asked for.
	bin := fakeClaude(t, `{"type":"result","is_error":false,"result":"ok","session_id":"s"}`, 0)
	e := claudeExecutor()
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	_, err := e.Run(context.Background(), Request{
		Task: tk, Provider: claudeInstance(bin), SessionID: "sid",
		AccessMode: provider.AccessReadOnly,
	})
	if !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("err = %v, want provider.ErrUnsupported", err)
	}
}

func TestRunIdleTimeoutKillsHungRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fake")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	// Produces no output and sleeps well past the idle timeout: a hung run.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := claudeExecutor()
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	var log bytes.Buffer
	start := time.Now()
	res, err := e.Run(context.Background(), Request{
		Task: tk, Provider: claudeInstance(path), SessionID: "sid",
		IdleTimeout: 150 * time.Millisecond, Log: &log,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if !strings.Contains(res.Message, "no output") {
		t.Fatalf("message = %q, want an inactivity note", res.Message)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("watchdog took %s, expected to kill quickly", d)
	}
}

func TestRunNoIdleTimeoutCompletesNormally(t *testing.T) {
	// A quick run with the watchdog enabled must still succeed (no false kill).
	bin := fakeClaude(t, `{"type":"result","is_error":false,"result":"ok","session_id":"s"}`, 0)
	e := claudeExecutor()
	tk := sampleTask()
	tk.WorkingDir = t.TempDir()
	var log bytes.Buffer
	res, err := e.Run(context.Background(), Request{
		Task: tk, Provider: claudeInstance(bin), SessionID: "sid",
		IdleTimeout: time.Hour, Log: &log,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %q, want success", res.Status)
	}
}

func TestRunSuccess(t *testing.T) {
	out := `{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"real-sid"}`
	res := runFake(t, out, 0)
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if res.SessionID != "real-sid" {
		t.Fatalf("session id = %q, want the one reported by the CLI", res.SessionID)
	}
}

func TestRunRateLimited(t *testing.T) {
	out := `{"type":"system","subtype":"api_retry","error_status":429,"error":"rate_limit","retry_delay_ms":5000,"session_id":"real-sid"}`
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
	if res.RetryAfter.Milliseconds() != 5000 {
		t.Fatalf("retry after = %v, want 5s", res.RetryAfter)
	}
}

func TestRunRateLimitEventCarriesResetTime(t *testing.T) {
	// A five-hour session-limit hit, as emitted by the real CLI: the
	// rate_limit_event carries the absolute reset time (unix seconds), the
	// synthetic assistant message the "rate_limit" error. ResetAt must surface
	// on the result so the engine can wait for the actual reset instead of a
	// blind backoff.
	out := strings.Join([]string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784655600,"rateLimitType":"five_hour","overageStatus":"rejected","isUsingOverage":false},"session_id":"real-sid"}`,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"You've hit your session limit"}]},"error":"rate_limit","session_id":"real-sid"}`,
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your session limit","session_id":"real-sid"}`,
	}, "\n")
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
	if want := time.Unix(1784655600, 0); !res.ResetAt.Equal(want) {
		t.Fatalf("reset at = %v, want %v", res.ResetAt, want)
	}
}

func TestRunOrgBlockedPausesLikeALimit(t *testing.T) {
	// The real output of an account whose organisation has switched Claude
	// subscription access off: the CLI answers in under a second, with a 403 and
	// its own error code. It must not come back as a failed run — the account
	// cannot spend anything, which is what the provider's pause and its fallback
	// are for.
	out := strings.Join([]string{
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Your organization has disabled Claude subscription access for Claude Code"}]},"session_id":"real-sid","error":"oauth_org_not_allowed","is_api_error_message":true,"api_error_code":"oauth_not_allowed_for_organization"}`,
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"api_error_code":"oauth_not_allowed_for_organization","result":"Your organization has disabled Claude subscription access for Claude Code","session_id":"real-sid","num_turns":1,"duration_ms":426}`,
	}, "\n")
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
	if res.RetryAfter != time.Hour {
		t.Fatalf("retry after = %v, want an hour", res.RetryAfter)
	}
	if !strings.Contains(res.Message, "disabled Claude subscription access") {
		t.Fatalf("message = %q, want it to name the organisation block", res.Message)
	}
}

func TestRunAuthError(t *testing.T) {
	out := `{"type":"result","subtype":"error","is_error":true,"error":"authentication_failed"}`
	res := runFake(t, out, 1)
	if res.Status != store.StatusAuthError {
		t.Fatalf("status = %q, want auth_error", res.Status)
	}
	// The message names the provider instance, so an operator with more than one
	// configured account knows which login to fix.
	if !strings.Contains(res.Message, provider.DefaultInstanceName) {
		t.Fatalf("message = %q, want it to name the provider", res.Message)
	}
}

func TestRunFailure(t *testing.T) {
	out := `{"type":"result","subtype":"error","is_error":true,"result":"boom"}`
	res := runFake(t, out, 2)
	if res.Status != store.StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit code = %d, want 2", res.ExitCode)
	}
}

func TestRunCapturesMetricsAndFinalOutput(t *testing.T) {
	out := `{"type":"result","subtype":"success","is_error":false,"result":"All done.","session_id":"s","total_cost_usd":0.012,"num_turns":2,"duration_ms":3400,"usage":{"input_tokens":1200,"output_tokens":300}}`
	res := runFake(t, out, 0)
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if res.FinalOutput != "All done." {
		t.Fatalf("final output = %q, want %q", res.FinalOutput, "All done.")
	}
	if res.Metrics == nil || res.Metrics.CostUSD != 0.012 || res.Metrics.InputTokens != 1200 || res.Metrics.OutputTokens != 300 {
		t.Fatalf("unexpected metrics: %+v", res.Metrics)
	}
}

func TestRunMalformedOutputIsNotMistakenForSuccess(t *testing.T) {
	// Garbage on stdout carries no terminal result, so even a zero exit must not
	// be reported as a successful run.
	res := runFake(t, "not json at all\n{\"type\":", 0)
	if res.Status != store.StatusFailed {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}

func TestRunRateLimitWindowReportedAfterTheRejection(t *testing.T) {
	// The reset time may arrive on a line after the one that rejects the
	// request. It must still reach the result, so the engine waits for the real
	// reopening instead of a blind backoff.
	out := strings.Join([]string{
		`{"type":"system","subtype":"api_retry","error_status":429,"error":"rate_limit","session_id":"real-sid"}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600},"session_id":"real-sid"}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"aborted","session_id":"real-sid"}`,
	}, "\n")
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
	if want := time.Unix(1784655600, 0); !res.ResetAt.Equal(want) {
		t.Fatalf("reset at = %v, want %v", res.ResetAt, want)
	}
}

func TestRunRateLimitWinsOverPartialResult(t *testing.T) {
	// A rate-limit event followed by an errored result must classify as
	// rate-limited (so the gate waits), not a plain failure.
	out := strings.Join([]string{
		`{"type":"system","subtype":"api_retry","error_status":429,"retry_delay_ms":1000}`,
		`{"type":"result","is_error":true,"result":"aborted"}`,
	}, "\n")
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
}

func TestRunAllowedRateLimitEventDoesNotClassify(t *testing.T) {
	// A non-rejected rate_limit_event (approaching-limit info) must not turn a
	// successful run into a rate-limited one.
	out := strings.Join([]string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784655600},"session_id":"real-sid"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"real-sid"}`,
	}, "\n")
	res := runFake(t, out, 0)
	if res.Status != store.StatusSuccess {
		t.Fatalf("status = %q, want success", res.Status)
	}
}

func TestRunRejectedRateLimitEventAloneClassifies(t *testing.T) {
	// Even without a 429 or an error field elsewhere, a rejected
	// rate_limit_event means the run is rate-limited.
	out := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784655600},"session_id":"real-sid"}`
	res := runFake(t, out, 1)
	if res.Status != store.StatusRateLimited {
		t.Fatalf("status = %q, want rate_limited_waiting", res.Status)
	}
}

func TestRunSubagentLimitDoesNotStopSuccessfulParent(t *testing.T) {
	for _, scope := range []string{`"parent_tool_use_id":"tool-1"`, `"agent_id":"agent-1"`} {
		t.Run(scope, func(t *testing.T) {
			out := strings.Join([]string{
				`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1791544800}}`,
				`{"type":"result","is_error":true,"api_error_status":429,` + scope + `}`,
				`{"type":"result","is_error":false,"result":"Parent finished","session_id":"parent-session"}`,
			}, "\n")
			res := runFake(t, out, 0)
			if res.Status != store.StatusSuccess || res.FinalOutput != "Parent finished" || res.SessionID != "parent-session" {
				t.Fatalf("subagent failure stopped parent: %+v", res)
			}
		})
	}
}

func TestRunEnvKeepsClaudeQsVariableNames(t *testing.T) {
	e := &Executor{Home: "/data/home", QueueBin: "/bin/dispatchq"}
	got := envMap(e.runEnv(Request{Task: sampleTask(), RunID: "r1"}, nil))
	for _, pair := range [][2]string{
		{"CLAUDEQ_HOME", "/data/home"},
		{"CLAUDEQ_BIN", "/bin/dispatchq"},
		{"CLAUDEQ_RUN_ID", "r1"},
	} {
		if got[pair[0]] != pair[1] {
			t.Errorf("%s = %q, want %q", pair[0], got[pair[0]], pair[1])
		}
	}
}
