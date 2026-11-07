package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielmaier42/claudeq/internal/store"
)

// The dashboard words its system instructions after GET /api/host: the
// operating system, and whether this build has an uninstaller to open.
func TestHostReportsSystemAndUninstaller(t *testing.T) {
	orig := hostOS
	t.Cleanup(func() { hostOS = orig })
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	for _, tc := range []struct {
		name      string
		os        string
		uninstall func(context.Context) error
		want      hostInfo
	}{
		{"macOS with the Installer", "darwin", func(context.Context) error { return nil }, hostInfo{OS: "darwin", Uninstall: true}},
		{"Windows without an in-app uninstaller", "windows", nil, hostInfo{OS: "windows", Uninstall: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostOS = tc.os
			srv := httptest.NewServer(handler(Deps{Store: st, Uninstall: tc.uninstall}))
			t.Cleanup(srv.Close)
			var got hostInfo
			do(t, srv, http.MethodGet, "/api/host", nil).into(t, &got)
			if got != tc.want {
				t.Fatalf("GET /api/host = %+v, want %+v", got, tc.want)
			}
		})
	}
}
