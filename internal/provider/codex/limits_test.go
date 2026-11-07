package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// fakeAppServer writes a stand-in for `codex app-server`: it records its
// arguments and CODEX_HOME, reads the three requests, answers the handshake
// with a notification in between, then answers the question with reply. Like
// the real server it keeps running until its input closes.
func fakeAppServer(t *testing.T, reply string) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	script := `#!/bin/sh
echo "$* home=$CODEX_HOME" > '` + record + `'
read -r init
echo '{"id":1,"result":{"userAgent":"fake"}}'
read -r initialized
echo '{"method":"account/updated","params":{"authMode":"chatgpt"}}'
read -r question
case "$question" in *account/rateLimits/read*) echo '` + reply + `' ;; esac
cat > /dev/null
`
	bin = filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { // #nosec G306 — a test executable
		t.Fatal(err)
	}
	return bin, record
}

func TestReadLimitsAsksTheInstancesAppServer(t *testing.T) {
	bin, record := fakeAppServer(t, `{"id":2,"result":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":37,"windowDurationMins":300,"resetsAt":1791350308},"secondary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1791900000},"planType":"prolite"}}}`)
	a := New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reading, err := a.ReadLimits(ctx, provider.Instance{BinaryPath: bin, ConfigDir: "/tmp/codex-b"}, provider.NativeHost{})
	if err != nil {
		t.Fatalf("ReadLimits: %v", err)
	}
	if reading.Plan != "Pro Lite" || reading.Capacity != 0 {
		t.Fatalf("plan = %q (capacity %v), want Pro Lite with no capacity", reading.Plan, reading.Capacity)
	}
	got := reading.Windows
	if len(got) != 2 || got[0].ID != "five_hour" || got[0].UsedPercent != 37 || got[1].ID != "week" || got[1].UsedPercent != 100 {
		t.Fatalf("windows = %+v", got)
	}
	if got[0].ResetsAt == nil || got[0].ResetsAt.Unix() != 1791350308 {
		t.Fatalf("five-hour reset = %v", got[0].ResetsAt)
	}
	rec, err := os.ReadFile(record) // #nosec G304 — the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if want := "app-server home=/tmp/codex-b"; strings.TrimSpace(string(rec)) != want {
		t.Fatalf("ran with %q, want %q", strings.TrimSpace(string(rec)), want)
	}
}

func TestReadLimitsReportsTheServersError(t *testing.T) {
	bin, _ := fakeAppServer(t, `{"id":2,"error":{"code":-32600,"message":"not logged in"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := New().ReadLimits(ctx, provider.Instance{BinaryPath: bin}, provider.NativeHost{})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v, want the server's message", err)
	}
}

func TestReadLimitsWhenTheServerNeverAnswers(t *testing.T) {
	bin, _ := fakeAppServer(t, `{"id":3,"result":{}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := New().ReadLimits(ctx, provider.Instance{BinaryPath: bin}, provider.NativeHost{}); err == nil {
		t.Fatal("a server that never answers must be an error")
	}
}

func TestParseRateLimits(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantIDs []string
		wantErr bool
	}{
		{"week only", `{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1}}}`, []string{"week"}, false},
		{"two windows", `{"rateLimits":{"primary":{"usedPercent":1,"windowDurationMins":300},"secondary":{"usedPercent":2,"windowDurationMins":10080}}}`, []string{"five_hour", "week"}, false},
		{"no limits", `{"rateLimits":null}`, nil, true},
		{"no windows", `{"rateLimits":{"primary":null,"secondary":null}}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := parseRateLimits(json.RawMessage(tc.body))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			var ids []string
			for _, w := range got {
				ids = append(ids, w.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("ids = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

func TestWindowName(t *testing.T) {
	cases := map[int]string{300: "5 hours", 10080: "Week", 1440: "1 day", 2880: "2 days", 60: "1 hour", 120: "2 hours", 45: "45 minutes", 0: "Limit"}
	for mins, want := range cases {
		if _, got := windowName(mins); got != want {
			t.Errorf("windowName(%d) = %q, want %q", mins, got, want)
		}
	}
}
