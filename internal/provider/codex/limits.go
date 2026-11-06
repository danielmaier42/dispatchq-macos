package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/provider/binpath"
	"github.com/danielmaier42/claudeq/internal/system"
)

// rateLimitsRequest is the conversation ReadLimits holds with `codex
// app-server`: the JSON-RPC handshake, then the one question. The app server is
// what Codex's own editor integrations talk to; asking it for the account's
// rate limits costs no model usage.
var rateLimitsRequest = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"dispatchq","version":"1"}}}`,
	`{"jsonrpc":"2.0","method":"initialized"}`,
	`{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read"}`,
}

// rateLimitsID is the request id of the question in rateLimitsRequest.
const rateLimitsID = 2

// rpcReply is one JSON-RPC message from the app server. Notifications carry no
// id and are skipped.
type rpcReply struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// rateLimitWindow is one window of the app server's answer.
type rateLimitWindow struct {
	UsedPercent        *float64 `json:"usedPercent"`
	WindowDurationMins int      `json:"windowDurationMins"`
	ResetsAt           int64    `json:"resetsAt"` // seconds since the epoch
}

// rateLimitsResult is the part of `account/rateLimits/read` dispatchq shows: the
// account's main limit, which has up to two windows, and its plan.
type rateLimitsResult struct {
	RateLimits *struct {
		Primary   *rateLimitWindow `json:"primary"`
		Secondary *rateLimitWindow `json:"secondary"`
		PlanType  string           `json:"planType"`
	} `json:"rateLimits"`
}

// ReadLimits implements provider.LimitReader by asking the instance's own
// `codex app-server` — same binary, same CODEX_HOME as a run.
//
// Codex says which plan the account is on but not how the plans compare, so
// the reading carries no capacity: a pool weighs a Codex member as 1 unless the
// operator sets its weight.
func (a *Adapter) ReadLimits(ctx context.Context, inst provider.Instance) (provider.LimitReading, error) {
	bin := a.ResolveBinary(inst)
	if bin == "" {
		return provider.LimitReading{}, errors.New("the Codex CLI was not found, so its limits cannot be read")
	}
	res, err := askAppServer(ctx, provider.Command{Path: bin, Args: []string{"app-server"}, Env: a.env(inst)})
	if err != nil {
		return provider.LimitReading{}, err
	}
	windows, plan, err := parseRateLimits(res)
	if err != nil {
		return provider.LimitReading{}, err
	}
	return provider.LimitReading{Windows: windows, Plan: plan}, nil
}

// askAppServer runs the app server, holds the rateLimitsRequest conversation
// and returns the answer to the question. The server stays up for as long as
// its input is open, so the process is stopped as soon as the answer is in.
func askAppServer(ctx context.Context, c provider.Command) (json.RawMessage, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := binpath.CommandContext(ctx, c.Path, c.Args...) // a configured binary path, no shell
	if err != nil {
		return nil, err
	}
	system.NoWindow(cmd)
	if len(c.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.Env...)
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
	}()
	if _, err := io.WriteString(stdin, strings.Join(rateLimitsRequest, "\n")+"\n"); err != nil {
		return nil, fmt.Errorf("write to codex app-server: %w", err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		var r rpcReply
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.ID == nil || *r.ID != rateLimitsID {
			continue
		}
		if r.Error != nil {
			return nil, fmt.Errorf("the Codex CLI could not report its limits (%s)", redact(r.Error.Message))
		}
		return r.Result, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read codex app-server: %w", err)
	}
	return nil, errors.New("codex app-server ended without reporting its limits")
}

// parseRateLimits turns the app server's answer into limit windows.
func parseRateLimits(raw json.RawMessage) ([]provider.LimitWindow, string, error) {
	var res rateLimitsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", fmt.Errorf("the Codex limits are not in a format dispatchq understands: %w", err)
	}
	if res.RateLimits == nil {
		return nil, "", errors.New("the Codex CLI reported no limits for this account")
	}
	var out []provider.LimitWindow
	for _, w := range []*rateLimitWindow{res.RateLimits.Primary, res.RateLimits.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}
		id, label := windowName(w.WindowDurationMins)
		lw := provider.LimitWindow{ID: id, Label: label, UsedPercent: *w.UsedPercent}
		if w.ResetsAt > 0 {
			at := time.Unix(w.ResetsAt, 0)
			lw.ResetsAt = &at
		}
		out = append(out, lw)
	}
	if len(out) == 0 {
		return nil, "", errors.New("the Codex CLI reported no limit windows for this account")
	}
	return out, planName(res.RateLimits.PlanType), nil
}

// planName turns Codex's plan id ("prolite") into a label.
func planName(id string) string {
	switch id {
	case "":
		return ""
	case "prolite":
		return "Pro Lite"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// windowName names a window by its length, using the names Claude's windows
// have where the lengths match, so the dashboard reads the same for both.
func windowName(mins int) (id, label string) {
	switch {
	case mins == 5*60:
		return "five_hour", "5 hours"
	case mins == 7*24*60:
		return "week", "Week"
	case mins > 0 && mins%(24*60) == 0:
		return fmt.Sprintf("window_%d", mins), count(mins/(24*60), "day")
	case mins > 0 && mins%60 == 0:
		return fmt.Sprintf("window_%d", mins), count(mins/60, "hour")
	case mins > 0:
		return fmt.Sprintf("window_%d", mins), count(mins, "minute")
	}
	return "window", "Limit"
}

func count(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
