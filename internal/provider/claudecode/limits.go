package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// UsageURL is the endpoint Claude Code's own `/usage` screen reads the plan
// limits from. It is not a documented API: it answers with the account's
// windows and costs no model usage, and if Anthropic changes it the dashboard
// says the limits are unavailable and nothing else is affected.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

// usageBeta is the beta header the endpoint requires for OAuth callers.
const usageBeta = "oauth-2025-04-20"

// keychainService is the macOS keychain item Claude Code keeps its login in.
// An instance with its own configuration directory gets an item of its own,
// suffixed with the start of the directory's SHA-256 — the same name the CLI
// derives, so dispatchq reads exactly the account the instance's runs use.
func keychainService(configDir string) string {
	const base = "Claude Code-credentials"
	if configDir == "" {
		return base
	}
	sum := sha256.Sum256([]byte(configDir))
	return base + "-" + hex.EncodeToString(sum[:])[:8]
}

// credentials is the part of Claude Code's stored login dispatchq reads. The
// refresh token is deliberately not parsed: dispatchq never renews a login
// itself, because a renewal rotates the token and would log the CLI out.
type credentials struct {
	OAuth *struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // milliseconds since the epoch
		// SubscriptionType and RateLimitTier say which plan the login is on
		// ("max", "default_claude_max_20x"); see planOf.
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

// usageWindow is one window of the usage endpoint's answer.
type usageWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// usageReport is the part of the usage endpoint's answer dispatchq shows: the
// five-hour session window and the weekly window across all models.
type usageReport struct {
	FiveHour *usageWindow `json:"five_hour"`
	SevenDay *usageWindow `json:"seven_day"`
}

// ReadLimits implements provider.LimitReader: the account's five-hour and
// weekly windows, read with the login the CLI stored for this instance.
func (a *Adapter) ReadLimits(ctx context.Context, inst provider.Instance, h provider.Host) (provider.LimitReading, error) {
	if inst.UsesAPIKey() {
		// The CLI bills an API key per token and ignores any plan login the
		// configuration directory also holds, so the plan's windows would
		// describe an allowance this instance does not spend.
		return provider.LimitReading{}, provider.ErrNoPlanLimits
	}
	login, err := a.login(ctx, inst, h)
	if err != nil {
		return provider.LimitReading{}, err
	}
	windows, err := a.readUsage(ctx, login.AccessToken)
	if err != nil {
		return provider.LimitReading{}, err
	}
	plan, capacity := planOf(login.SubscriptionType, login.RateLimitTier)
	return provider.LimitReading{Windows: windows, Plan: plan, Capacity: capacity}, nil
}

// tierRe finds the multiplier in a rate-limit tier ("default_claude_max_20x").
var tierRe = regexp.MustCompile(`_(\d+)x$`)

// planOf names the plan a login is on and its size relative to Pro. The tier
// carries the multiplier; a Team seat reports a Max tier of its own, which is
// exactly the allowance it has. An unrecognised plan has capacity 0: unknown,
// and a pool then weighs it as 1 unless the operator says otherwise.
func planOf(subscription, tier string) (string, float64) {
	name := strings.ToUpper(subscription[:min(1, len(subscription))]) + subscription[min(1, len(subscription)):]
	if m := tierRe.FindStringSubmatch(tier); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return strings.TrimSpace(name + " " + m[1] + "x"), float64(n)
		}
	}
	if subscription == "pro" {
		return "Pro", 1
	}
	return name, 0
}

// readUsage asks the usage endpoint for the account's windows.
func (a *Adapter) readUsage(ctx context.Context, token string) ([]provider.LimitWindow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.usageURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", usageBeta)
	resp, err := a.httpClient().Do(req)
	if err != nil {
		// The client's error names the URL, never the header, so it is safe to
		// pass on; it is cut to a line all the same.
		return nil, fmt.Errorf("the Claude usage endpoint could not be reached (%s)", redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Claude's usage answer: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("the stored Claude login was refused; it is renewed on the next run on this account")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("the Claude usage endpoint asked dispatchq to slow down; the next reading tries again")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the Claude usage endpoint answered %s", resp.Status)
	}
	return parseUsage(body)
}

// parseUsage turns the endpoint's answer into the two windows the dashboard
// shows. A window the account does not have is left out; an answer with
// neither is an error, because "no limits" is not something Claude says.
func parseUsage(body []byte) ([]provider.LimitWindow, error) {
	var rep usageReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return nil, fmt.Errorf("the Claude usage answer is not in a format dispatchq understands: %w", err)
	}
	var out []provider.LimitWindow
	for _, w := range []struct {
		id, label string
		src       *usageWindow
	}{
		{"five_hour", "5 hours", rep.FiveHour},
		{"week", "Week", rep.SevenDay},
	} {
		if w.src == nil || w.src.Utilization == nil {
			continue
		}
		lw := provider.LimitWindow{ID: w.id, Label: w.label, UsedPercent: *w.src.Utilization}
		if at, err := time.Parse(time.RFC3339Nano, w.src.ResetsAt); err == nil {
			lw.ResetsAt = &at
		}
		out = append(out, lw)
	}
	if len(out) == 0 {
		return nil, errors.New("the Claude usage answer named no limit windows")
	}
	return out, nil
}

// credentialsLimit bounds the credentials file read from another environment.
const credentialsLimit = 1 << 20

// oauthLogin is the part of a stored login ReadLimits uses.
type oauthLogin struct {
	AccessToken, SubscriptionType, RateLimitTier string
}

// login returns the instance's stored OAuth login: from the keychain where
// macOS Claude Code keeps it, else from the credentials file the CLI writes
// where there is no keychain. The token is never logged or wrapped into an
// error.
//
// An instance that runs in another environment (Linux under WSL) keeps its
// login in that environment's credentials file, which is read through its
// host into memory only, exactly as the native file is: nothing is copied.
func (a *Adapter) login(ctx context.Context, inst provider.Instance, h provider.Host) (oauthLogin, error) {
	var raw []byte
	var err error
	if h != nil && !h.Execution().IsNative() {
		dir := inst.ConfigDir
		if dir == "" {
			dir = "~/.claude"
		}
		raw, err = h.ReadFile(ctx, path.Join(dir, ".credentials.json"), credentialsLimit)
		if err != nil {
			return oauthLogin{}, errors.New("no Claude login was found for this account inside Linux; open the distribution and run `claude auth login` there")
		}
	} else if raw, err = a.readKeychain(ctx, keychainService(inst.ConfigDir)); err != nil {
		dir := inst.ConfigDir
		if dir == "" {
			home, herr := os.UserHomeDir()
			if herr != nil {
				return oauthLogin{}, fmt.Errorf("locate Claude's configuration directory: %w", herr)
			}
			dir = filepath.Join(home, ".claude")
		}
		var ferr error
		raw, ferr = os.ReadFile(filepath.Join(dir, ".credentials.json")) // #nosec G304 — the instance's own configuration directory
		if ferr != nil {
			return oauthLogin{}, errors.New("no Claude login was found for this account; run `claude auth login`")
		}
	}
	var c credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return oauthLogin{}, errors.New("the stored Claude login is not in a format dispatchq understands")
	}
	if c.OAuth == nil || c.OAuth.AccessToken == "" {
		return oauthLogin{}, provider.ErrNoPlanLimits
	}
	if c.OAuth.ExpiresAt > 0 && a.now().After(time.UnixMilli(c.OAuth.ExpiresAt)) {
		return oauthLogin{}, errors.New("the stored Claude login has expired; it is renewed on the next run on this account")
	}
	return oauthLogin{AccessToken: c.OAuth.AccessToken, SubscriptionType: c.OAuth.SubscriptionType, RateLimitTier: c.OAuth.RateLimitTier}, nil
}

// readKeychain returns the secret of a generic keychain item, through the same
// `security` tool Claude Code itself uses to store it — which is why reading it
// raises no keychain prompt.
func (a *Adapter) readKeychain(ctx context.Context, service string) ([]byte, error) {
	if a.keychain != nil {
		return a.keychain(ctx, service)
	}
	if runtime.GOOS != "darwin" {
		// No keychain: Claude Code keeps its login in .credentials.json there.
		return nil, errors.New("no keychain on this platform")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-w")
	// Only stdout: it holds the secret and nothing else, and stderr is not
	// needed to know the item is missing.
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keychain item %q: %w", service, err)
	}
	return out, nil
}

func (a *Adapter) usageURL() string {
	if a.usageEndpoint != "" {
		return a.usageEndpoint
	}
	return UsageURL
}

func (a *Adapter) httpClient() *http.Client {
	if a.http != nil {
		return a.http
	}
	return &http.Client{Timeout: provider.LimitsTimeout}
}

func (a *Adapter) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}
