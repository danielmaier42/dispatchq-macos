package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// updateDaemon stands in for the daemon: POST /api/update/check answers with
// status, anything else is not found.
func updateDaemon(t *testing.T, status int, body any) *int {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/update/check" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls++
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	withDaemon(t, srv.URL)
	return &calls
}

func TestCmdUpdateCheckAsksTheDaemon(t *testing.T) {
	calls := updateDaemon(t, http.StatusOK, map[string]any{
		"current": "2.0.4", "supported": true, "available": true, "latest": "2.0.5",
		"html_url": "https://example.test/v2.0.5",
	})
	out := captureStdout(t, func() error { return cmdUpdate([]string{"check"}) })
	if *calls != 1 {
		t.Fatalf("daemon asked %d times, want 1", *calls)
	}
	if !strings.Contains(out, "DispatchQ 2.0.5 is available (running 2.0.4)") || !strings.Contains(out, "https://example.test/v2.0.5") {
		t.Fatalf("out = %q", out)
	}
}

func TestCmdUpdateCheckJSONPrintsTheDaemonsAnswer(t *testing.T) {
	updateDaemon(t, http.StatusOK, map[string]any{"current": "2.0.5", "supported": true, "latest": "2.0.5", "checking": false})
	out := captureStdout(t, func() error { return cmdUpdate([]string{"check", "--json"}) })
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if v["latest"] != "2.0.5" || v["checking"] != false {
		t.Fatalf("v = %v", v)
	}
}

func TestCmdUpdateCheckReportsAFailedCheck(t *testing.T) {
	updateDaemon(t, http.StatusOK, map[string]any{"current": "2.0.4", "supported": true, "error": "github: 503"})
	err := cmdUpdate([]string{"check"})
	if err == nil || !strings.Contains(err.Error(), "github: 503") {
		t.Fatalf("err = %v", err)
	}
}

func TestCmdUpdateCheckPassesOnTheDaemonsError(t *testing.T) {
	updateDaemon(t, http.StatusServiceUnavailable, map[string]string{"error": "update checks are not available"})
	err := cmdUpdate([]string{"check"})
	if err == nil || err.Error() != "update checks are not available" {
		t.Fatalf("err = %v", err)
	}
}

func TestCmdUpdateCheckWithoutDaemon(t *testing.T) {
	withoutDaemon(t)
	err := cmdUpdate([]string{"check"})
	if !errors.Is(err, errNoDaemon) || !strings.Contains(err.Error(), "is the DispatchQ app running?") {
		t.Fatalf("err = %v", err)
	}
}

func TestCmdUpdateRejectsOtherUse(t *testing.T) {
	withoutDaemon(t)
	for _, args := range [][]string{nil, {"install"}, {"check", "extra"}} {
		if err := cmdUpdate(args); err == nil {
			t.Fatalf("%q: want an error", args)
		}
	}
}

func TestUpdateLine(t *testing.T) {
	cases := []struct {
		name string
		v    updateView
		want string
	}{
		{"up to date", updateView{Current: "2.0.5", Supported: true, Latest: "2.0.5"}, "DispatchQ 2.0.5 is up to date."},
		{"dismissed", updateView{Current: "2.0.4", Supported: true, Latest: "2.0.5", Dismissed: "v2.0.5"}, "DispatchQ 2.0.5 is available but was dismissed (running 2.0.4)."},
		{"no installer yet", updateView{Current: "2.0.4", Supported: true, Latest: "2.0.5"}, "DispatchQ 2.0.5 is released but has no installer yet (running 2.0.4); check again shortly."},
		{"dev build", updateView{Current: "dev-main", Latest: "2.0.5"}, "This is a development build (dev-main); it is not offered updates. Latest release: 2.0.5."},
		{"restart", updateView{Current: "2.0.4", Supported: true, Installed: "2.0.5", Restart: true}, "DispatchQ 2.0.5 is installed; restart the app to run it (running 2.0.4)."},
	}
	for _, c := range cases {
		if got := updateLine(c.v); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
