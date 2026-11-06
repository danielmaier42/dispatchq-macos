package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
)

// stubFetcher implements update.Fetcher with canned releases.
type stubFetcher struct {
	rels []update.Release
	err  error
}

func (f stubFetcher) Releases(context.Context) ([]update.Release, error) { return f.rels, f.err }

// newUpdateServer wires an API server with an update service backed by stub
// releases and a stub installer download, and pins the running version. The
// first release's PkgURL "usePkgSrv" is rewritten to a live test asset server.
func newUpdateServer(t *testing.T, current string, rels ...update.Release) (*httptest.Server, *store.Store, *string) {
	t.Helper()
	orig := version.Version
	version.Version = current
	t.Cleanup(func() { version.Version = orig })

	// A fake GitHub asset endpoint for the download test.
	pkgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("installer-bytes"))
	}))
	t.Cleanup(pkgSrv.Close)
	for i := range rels {
		if rels[i].PkgURL == "usePkgSrv" {
			rels[i].PkgURL = pkgSrv.URL + "/x.pkg"
		}
	}

	opened := new(string)
	svc := update.NewService(stubFetcher{rels: rels}, time.Hour,
		update.WithDownloadDir(t.TempDir()),
		update.WithHTTPClient(pkgSrv.Client()),
		update.WithOpener(func(_ context.Context, path string) error { *opened = path; return nil }),
	)
	if len(rels) > 0 {
		if _, err := svc.CheckNow(context.Background()); err != nil {
			t.Fatalf("seed CheckNow: %v", err)
		}
	}

	// The host's /Applications must not decide the outcome of these tests; the
	// installed-version cases below set this explicitly.
	stubInstalled(t, "")

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st, Updates: svc}))
	t.Cleanup(srv.Close)
	return srv, st, opened
}

func TestUpdateStatusAvailable(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.1.0", update.Release{
		Version: "0.2.0", Tag: "v0.2.0", PkgURL: "https://example.test/x.pkg",
		PkgName: "dispatchq-0.2.0.pkg", Notes: "new stuff", HTMLURL: "https://example.test",
	})
	var s updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s)

	if !s.Supported || !s.Available {
		t.Fatalf("expected supported+available, got %+v", s)
	}
	if s.Current != "0.1.0" || s.Latest != "0.2.0" {
		t.Errorf("current/latest = %q/%q", s.Current, s.Latest)
	}
	if !s.HasInstaller {
		t.Error("has_installer should be true")
	}
	if s.AllReleasesURL == "" {
		t.Error("all_releases_url should be set so the user can browse the full history")
	}
}

// A user who skipped versions must see the notes of every release in between,
// newest first, not just the latest.
func TestUpdateStatusAggregatesSkippedNotes(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.1.1",
		update.Release{Version: "0.1.4", Tag: "v0.1.4", PkgURL: "https://example.test/x.pkg", PkgName: "p.pkg", Notes: "notes for 0.1.4"},
		update.Release{Version: "0.1.3", Tag: "v0.1.3", Notes: "notes for 0.1.3"},
		update.Release{Version: "0.1.2", Tag: "v0.1.2", Notes: "notes for 0.1.2"},
		update.Release{Version: "0.1.1", Tag: "v0.1.1", Notes: "notes for 0.1.1 (already installed)"},
	)
	var s updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s)

	if !s.Available || s.Latest != "0.1.4" {
		t.Fatalf("expected 0.1.4 available, got %+v", s)
	}
	if s.SkippedCount != 3 {
		t.Errorf("skipped_count = %d, want 3 (0.1.4, 0.1.3, 0.1.2)", s.SkippedCount)
	}
	for _, want := range []string{"notes for 0.1.4", "notes for 0.1.3", "notes for 0.1.2"} {
		if !strings.Contains(s.Notes, want) {
			t.Errorf("aggregated notes missing %q\ngot: %s", want, s.Notes)
		}
	}
	// The already-installed version's notes must not be included.
	if strings.Contains(s.Notes, "already installed") {
		t.Errorf("notes should not include the installed version: %s", s.Notes)
	}
	// Order: newest (0.1.4) appears before older (0.1.2).
	if strings.Index(s.Notes, "0.1.4") > strings.Index(s.Notes, "0.1.2") {
		t.Errorf("notes should be newest-first:\n%s", s.Notes)
	}
}

func TestUpdateStatusUpToDate(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.2.0", update.Release{
		Version: "0.2.0", Tag: "v0.2.0", PkgURL: "https://example.test/x.pkg", PkgName: "p.pkg",
	})
	var s updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s)
	if s.Available {
		t.Errorf("should not be available when on the latest version: %+v", s)
	}
	if !s.Supported {
		t.Error("a real version should be supported")
	}
}

func TestUpdateStatusDevBuildUnsupported(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "dev", update.Release{
		Version: "0.2.0", Tag: "v0.2.0", PkgURL: "https://example.test/x.pkg", PkgName: "p.pkg",
	})
	var s updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s)
	if s.Supported || s.Available {
		t.Errorf("dev build must not offer updates: %+v", s)
	}
}

func TestUpdateDismissSuppressesThenNewerReappears(t *testing.T) {
	srv, st, _ := newUpdateServer(t, "v0.1.0", update.Release{
		Version: "0.2.0", Tag: "v0.2.0", PkgURL: "https://example.test/x.pkg", PkgName: "p.pkg",
	})

	var s updateStatus
	do(t, srv, "POST", "/api/update/dismiss", map[string]string{"version": "0.2.0"}).into(t, &s)
	if s.Available {
		t.Fatalf("dismissed version should not be available: %+v", s)
	}
	// Persisted to state.
	state, _ := st.LoadState()
	if state.DismissedUpdate() != "0.2.0" {
		t.Errorf("dismissed version not persisted, got %q", state.DismissedUpdate())
	}
	// GET reflects the dismissal too.
	var s2 updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s2)
	if s2.Available {
		t.Errorf("GET should honor dismissal: %+v", s2)
	}
}

func TestUpdateDismissRequiresVersion(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.1.0", update.Release{Version: "0.2.0", PkgURL: "x", PkgName: "p.pkg"})
	if r := do(t, srv, "POST", "/api/update/dismiss", map[string]string{}); r.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 without a version, got %d", r.Status)
	}
}

func TestUpdateDownloadOpensInstaller(t *testing.T) {
	srv, _, opened := newUpdateServer(t, "v0.1.0", update.Release{
		Version: "0.2.0", Tag: "v0.2.0", PkgURL: "usePkgSrv", PkgName: "dispatchq-0.2.0.pkg",
	})
	var out struct {
		Path string `json:"path"`
	}
	r := do(t, srv, "POST", "/api/update/download", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("download status = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &out)
	if out.Path == "" {
		t.Fatal("expected a download path")
	}
	if *opened != out.Path {
		t.Errorf("installer not opened: opened=%q path=%q", *opened, out.Path)
	}
	if _, err := os.Stat(out.Path); err != nil {
		t.Errorf("downloaded file missing: %v", err)
	}
}

func TestUpdateEndpointsUnavailableWithoutService(t *testing.T) {
	// The host's /Applications must not decide the outcome of these tests; the
	// installed-version cases below set this explicitly.
	stubInstalled(t, "")

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st})) // no Updates
	t.Cleanup(srv.Close)

	// GET returns a benign status (not an error) so the UI degrades gracefully.
	var s updateStatus
	do(t, srv, "GET", "/api/update", nil).into(t, &s)
	if s.Available || s.Supported {
		t.Errorf("no service should mean nothing offered: %+v", s)
	}
	if !s.Unavailable {
		t.Errorf("no service should say so: %+v", s)
	}
	// Mutating endpoints report unavailable.
	if r := do(t, srv, "POST", "/api/update/download", nil); r.Status != http.StatusServiceUnavailable {
		t.Errorf("download without service = %d, want 503", r.Status)
	}
}

// stubInstalled pins the "newest DispatchQ.app on disk" lookup for one test, so
// the host's /Applications never decides the outcome. An empty version means
// nothing is installed.
func stubInstalled(t *testing.T, version string) string {
	t.Helper()
	app := "/Applications/DispatchQ.app"
	var inst *update.Installed
	if version != "" {
		inst = &update.Installed{Version: version, Path: app}
	}
	prev := installedApp
	installedApp = func() *update.Installed { return inst }
	t.Cleanup(func() { installedApp = prev })
	return app
}

// An update that was installed but never took over (the old daemon kept
// running) must not be offered for download again — it is already on disk.
func TestUpdateStatusRestartRequired(t *testing.T) {
	tests := []struct {
		name            string
		current         string
		installed       string
		wantRestart     bool
		wantAvailable   bool
		wantInstalledIn string
	}{
		{"stale daemon", "v0.7.0", "0.8.1", true, false, "0.8.1"},
		{"in sync", "v0.8.1", "0.8.1", false, false, "0.8.1"},
		{"nothing installed", "v0.7.0", "", false, true, ""},
		{"dev build", "dev", "0.8.1", false, false, "0.8.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := newUpdateServer(t, tc.current, update.Release{
				Version: "0.8.1", Tag: "v0.8.1", PkgURL: "https://example.test/x.pkg", PkgName: "p.pkg",
			})
			stubInstalled(t, tc.installed)

			var s updateStatus
			do(t, srv, "GET", "/api/update", nil).into(t, &s)

			if s.RestartRequired != tc.wantRestart {
				t.Errorf("restart_required = %v, want %v (%+v)", s.RestartRequired, tc.wantRestart, s)
			}
			if s.Available != tc.wantAvailable {
				t.Errorf("available = %v, want %v (%+v)", s.Available, tc.wantAvailable, s)
			}
			if s.Installed != tc.wantInstalledIn {
				t.Errorf("installed = %q, want %q", s.Installed, tc.wantInstalledIn)
			}
		})
	}
}

// The relaunch endpoint hands the LaunchAgent to the installed bundle.
func TestRelaunchUpdate(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.7.0")

	app := stubInstalled(t, "0.8.1")
	var got string
	prev := relaunchInstalled
	relaunchInstalled = func(path string) error { got = path; return nil }
	t.Cleanup(func() { relaunchInstalled = prev })

	r := do(t, srv, "POST", "/api/update/relaunch", nil)
	if r.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", r.Status, r.Body)
	}
	if got != app {
		t.Fatalf("relaunched %q, want %q", got, app)
	}
}

// With no DispatchQ.app on disk there is nothing to hand over to.
func TestRelaunchUpdateWithoutInstalledApp(t *testing.T) {
	srv, _, _ := newUpdateServer(t, "v0.7.0")
	stubInstalled(t, "")

	if r := do(t, srv, "POST", "/api/update/relaunch", nil); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", r.Status, r.Body)
	}
}

// Without an update service the system has no release installer, so there is
// nothing to hand over to, even when an installed app is found.
func TestRelaunchUpdateWithoutUpdateService(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(Deps{Store: st}))
	t.Cleanup(srv.Close)
	stubInstalled(t, "0.8.1")
	called := false
	prev := relaunchInstalled
	relaunchInstalled = func(string) error { called = true; return nil }
	t.Cleanup(func() { relaunchInstalled = prev })

	if r := do(t, srv, "POST", "/api/update/relaunch", nil); r.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", r.Status, r.Body)
	}
	if called {
		t.Fatal("relaunched an installed app without an update service")
	}
}

// pinDataFloor moves the oldest release that can run on this build's data for
// one test, so the cases do not follow the real floor.
func pinDataFloor(t *testing.T, floor string) {
	t.Helper()
	prev := dataFloor
	dataFloor = floor
	t.Cleanup(func() { dataFloor = prev })
}

func TestUpdateStatusReinstallOnDevBuild(t *testing.T) {
	cases := []struct {
		name    string
		current string
		rel     update.Release
		want    bool
	}{
		{"release at the floor", "dev-main", update.Release{Version: "2.0.0", PkgURL: "x"}, true},
		{"release above the floor", "dev-main", update.Release{Version: "2.1.0", PkgURL: "x"}, true},
		{"release below the floor", "dev-main", update.Release{Version: "1.6.2", PkgURL: "x"}, false},
		{"release without installer", "dev-main", update.Release{Version: "2.1.0"}, false},
		{"release build", "v2.0.0", update.Release{Version: "2.1.0", PkgURL: "x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinDataFloor(t, "2.0.0")
			st := buildUpdateStatus(tc.current, "", "", update.Snapshot{Releases: []update.Release{tc.rel}})
			if st.Reinstall != tc.want {
				t.Errorf("Reinstall = %v, want %v", st.Reinstall, tc.want)
			}
			if wantFloor := map[bool]string{true: "", false: "2.0.0"}[st.Supported]; st.DataFloor != wantFloor {
				t.Errorf("DataFloor = %q, want %q", st.DataFloor, wantFloor)
			}
		})
	}
}

func TestReinstallReleaseOpensInstaller(t *testing.T) {
	pinDataFloor(t, "2.0.0")
	srv, _, opened := newUpdateServer(t, "dev-main", update.Release{
		Version: "2.0.0", Tag: "v2.0.0", PkgURL: "usePkgSrv", PkgName: "dispatchq-2.0.0.pkg",
	})
	var out struct {
		Path string `json:"path"`
	}
	r := do(t, srv, "POST", "/api/update/reinstall", nil)
	if r.Status != http.StatusOK {
		t.Fatalf("reinstall status = %d (%s)", r.Status, r.Body)
	}
	r.into(t, &out)
	if out.Path == "" || *opened != out.Path {
		t.Errorf("installer not opened: opened=%q path=%q", *opened, out.Path)
	}
}

func TestReinstallReleaseRefused(t *testing.T) {
	cases := []struct {
		name    string
		current string
		rels    []update.Release
		want    string
	}{
		{"release build", "v2.0.0", []update.Release{{Version: "2.1.0", PkgURL: "usePkgSrv"}}, "only a development build"},
		{"nothing checked", "dev-main", nil, "no release is known"},
		{"no installer", "dev-main", []update.Release{{Version: "2.1.0"}}, "has no installer"},
		{"below the floor", "dev-main", []update.Release{{Version: "1.6.2", PkgURL: "usePkgSrv"}}, "2.0.0 or later"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinDataFloor(t, "2.0.0")
			srv, _, opened := newUpdateServer(t, tc.current, tc.rels...)
			r := do(t, srv, "POST", "/api/update/reinstall", nil)
			if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), tc.want) {
				t.Errorf("reinstall = %d %s, want 409 containing %q", r.Status, r.Body, tc.want)
			}
			if *opened != "" {
				t.Errorf("installer opened despite refusal: %q", *opened)
			}
		})
	}
}
