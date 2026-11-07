package claudecode

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

func TestKeychainServiceMatchesClaudeCode(t *testing.T) {
	// The suffix is what Claude Code itself derives; these pairs were read off
	// a real keychain.
	cases := map[string]string{
		"":                       "Claude Code-credentials",
		"/Users/dm/.claude":      "Claude Code-credentials-7548c0d1",
		"/Users/dm/.claude_team": "Claude Code-credentials-290c83f0",
	}
	for dir, want := range cases {
		if got := keychainService(dir); got != want {
			t.Errorf("keychainService(%q) = %q, want %q", dir, got, want)
		}
	}
}

// usageBody is a trimmed real answer of the usage endpoint.
const usageBody = `{"five_hour":{"utilization":22.0,"resets_at":"2026-10-02T18:19:59.686827+00:00"},
"seven_day":{"utilization":83.0,"resets_at":"2026-10-05T00:59:59.686853+00:00"},
"seven_day_opus":null,"extra_usage":{"utilization":null},
"limits":[{"kind":"session","percent":22}]}`

func creds(token string, expires time.Time) []byte {
	return []byte(`{"claudeAiOauth":{"accessToken":"` + token + `","refreshToken":"r","expiresAt":` +
		strconv.FormatInt(expires.UnixMilli(), 10) +
		`,"subscriptionType":"team","rateLimitTier":"default_claude_max_5x"}}`)
}

func TestPlanOf(t *testing.T) {
	cases := []struct {
		sub, tier, plan string
		capacity        float64
	}{
		{"max", "default_claude_max_20x", "Max 20x", 20},
		{"team", "default_claude_max_5x", "Team 5x", 5},
		{"pro", "default_claude_ai", "Pro", 1},
		{"enterprise", "", "Enterprise", 0},
		{"", "", "", 0},
	}
	for _, tc := range cases {
		plan, capacity := planOf(tc.sub, tc.tier)
		if plan != tc.plan || capacity != tc.capacity {
			t.Errorf("planOf(%q, %q) = %q, %v; want %q, %v", tc.sub, tc.tier, plan, capacity, tc.plan, tc.capacity)
		}
	}
}

// limitsAdapter returns an adapter whose keychain holds items, whose clock
// reads now, and whose usage endpoint is handler.
func limitsAdapter(t *testing.T, items map[string][]byte, now time.Time, handler http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := New()
	a.keychain = func(_ context.Context, service string) ([]byte, error) {
		if v, ok := items[service]; ok {
			return v, nil
		}
		return nil, errors.New("not found")
	}
	a.http = srv.Client()
	a.usageEndpoint = srv.URL
	a.clock = func() time.Time { return now }
	return a
}

func TestReadLimitsReadsTheInstancesOwnAccount(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	dir := "/Users/dm/.claude_team"
	var gotAuth, gotBeta string
	a := limitsAdapter(t, map[string][]byte{
		keychainService(""):  creds("default-token", now.Add(time.Hour)),
		keychainService(dir): creds("team-token", now.Add(time.Hour)),
	}, now, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(usageBody))
	})

	reading, err := a.ReadLimits(context.Background(), provider.Instance{ID: "team", ConfigDir: dir}, provider.NativeHost{})
	if err != nil {
		t.Fatalf("ReadLimits: %v", err)
	}
	if reading.Plan != "Team 5x" || reading.Capacity != 5 {
		t.Fatalf("plan = %q (capacity %v), want the team seat's Max 5x tier", reading.Plan, reading.Capacity)
	}
	got := reading.Windows
	if gotAuth != "Bearer team-token" || gotBeta != usageBeta {
		t.Fatalf("request headers auth=%q beta=%q, want the team account's token", gotAuth, gotBeta)
	}
	if len(got) != 2 {
		t.Fatalf("windows = %+v, want 5 hours and week", got)
	}
	if got[0].ID != "five_hour" || got[0].UsedPercent != 22 || got[1].ID != "week" || got[1].UsedPercent != 83 {
		t.Fatalf("windows = %+v", got)
	}
	wantReset := time.Date(2026, 10, 5, 0, 59, 59, 686853000, time.UTC)
	if got[1].ResetsAt == nil || !got[1].ResetsAt.Equal(wantReset) {
		t.Fatalf("week resets at %v, want %v", got[1].ResetsAt, wantReset)
	}
}

func TestReadLimitsFallsBackToTheCredentialsFile(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), creds("file-token", now.Add(time.Hour)), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	a := limitsAdapter(t, nil, now, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(usageBody))
	})
	if _, err := a.ReadLimits(context.Background(), provider.Instance{ConfigDir: dir}, provider.NativeHost{}); err != nil {
		t.Fatalf("ReadLimits: %v", err)
	}
	if gotAuth != "Bearer file-token" {
		t.Fatalf("auth = %q, want the file's token", gotAuth)
	}
}

func TestReadLimitsFailures(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	ok := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(usageBody)) }
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
	}
	cases := []struct {
		name    string
		item    []byte
		handler http.HandlerFunc
		want    string
	}{
		{"no login at all", nil, ok, "no Claude login was found"},
		{"API-key login", []byte(`{"mcpOAuth":{}}`), ok, "not logged in with a Claude plan"},
		{"expired token", creds("t", now.Add(-time.Minute)), ok, "has expired"},
		{"refused token", creds("t", now.Add(time.Hour)), status(http.StatusUnauthorized), "was refused"},
		{"rate limited", creds("t", now.Add(time.Hour)), status(http.StatusTooManyRequests), "slow down"},
		{"server error", creds("t", now.Add(time.Hour)), status(http.StatusBadGateway), "502"},
		{"unknown answer", creds("t", now.Add(time.Hour)), func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }, "no limit windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			items := map[string][]byte{}
			if tc.item != nil {
				items[keychainService(dir)] = tc.item
			}
			a := limitsAdapter(t, items, now, tc.handler)
			_, err := a.ReadLimits(context.Background(), provider.Instance{ConfigDir: dir}, provider.NativeHost{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), `"t"`) || strings.Contains(err.Error(), "Bearer") {
				t.Fatalf("err %q carries the token", err)
			}
		})
	}
}
