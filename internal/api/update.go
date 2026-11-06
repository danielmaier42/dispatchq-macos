package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/update"
	"github.com/danielmaier42/claudeq/internal/version"
)

// updateStatus is the update state the dashboard renders (the yellow banner and
// the Settings badge). It combines the running build's version, the cached
// GitHub check, and the user's dismissed version.
type updateStatus struct {
	// Current is the running build's normalized version, e.g. "0.1.4".
	Current string `json:"current"`
	// Supported is false for a development build (version "dev"), where updates
	// cannot be compared and nothing is offered.
	Supported bool `json:"supported"`
	// Unavailable is true when this system has no update service: it cannot
	// install a release, so the dashboard only links to the releases page.
	Unavailable bool `json:"unavailable,omitempty"`
	// Available is true when a newer, non-dismissed release with an installer is
	// ready to download.
	Available bool `json:"available"`
	// Latest is the latest release's version, "" if unknown / not yet checked.
	Latest string `json:"latest"`
	// Tag is the latest release's git tag, e.g. "v0.1.5".
	Tag string `json:"tag,omitempty"`
	// Notes is the combined notes of every release newer than the current build
	// (each prefixed with its version), so a user who skipped versions sees all
	// the changes in between — not just the newest release.
	Notes string `json:"notes,omitempty"`
	// SkippedCount is how many releases are newer than the current build (i.e.
	// how many versions' notes Notes aggregates).
	SkippedCount int `json:"skipped_count,omitempty"`
	// HTMLURL is the latest release's page on GitHub.
	HTMLURL string `json:"html_url,omitempty"`
	// AllReleasesURL is the GitHub page listing every release, so the user can
	// always browse the full history.
	AllReleasesURL string `json:"all_releases_url,omitempty"`
	// HasInstaller reports whether the latest release ships a `.pkg` asset.
	HasInstaller bool `json:"has_installer"`
	// PublishedAt is when the latest release was published.
	PublishedAt *time.Time `json:"published_at,omitempty"`
	// Dismissed is the version the user dismissed, "" if none.
	Dismissed string `json:"dismissed,omitempty"`
	// Installed is the version of the newest DispatchQ.app on disk. It differs
	// from Current only when an installed update never took over from the
	// running daemon.
	Installed string `json:"installed,omitempty"`
	// RestartRequired is true when a newer build is installed on disk than the
	// one this daemon is running: the update is already there, the background
	// service just never switched to it. Downloading it again would not help,
	// so no update is offered while this is set.
	RestartRequired bool `json:"restart_required,omitempty"`
	// Reinstall is true on a development build when the latest release ships
	// an installer and can run on this build's data (see update.DataFloor).
	Reinstall bool `json:"reinstall,omitempty"`
	// DataFloor is the oldest release that can run on this build's data. It is
	// set only on a development build, to explain a reinstall that is not offered.
	DataFloor string `json:"data_floor,omitempty"`
	// Checking / Downloading reflect in-flight background work.
	Checking    bool `json:"checking"`
	Downloading bool `json:"downloading"`
	// LastChecked is when the cache was last refreshed.
	LastChecked *time.Time `json:"last_checked,omitempty"`
	// Error is the last check error, "" if healthy.
	Error string `json:"error,omitempty"`
}

// buildUpdateStatus assembles the response from the running build's version,
// the newest version installed on disk, the dismissed version, and the cached
// check snapshot.
func buildUpdateStatus(current, installed, dismissed string, snap update.Snapshot) updateStatus {
	cur := update.Normalize(current)
	inst := update.Normalize(installed)
	// A development build is never "behind" an installed bundle — it is simply
	// not the installed one.
	restart := update.IsReleaseVersion(current) &&
		update.IsReleaseVersion(inst) && update.IsNewer(inst, cur)
	st := updateStatus{
		Current:         cur,
		Installed:       inst,
		RestartRequired: restart,
		Supported:       update.IsReleaseVersion(current),
		Dismissed:       dismissed,
		Checking:        snap.Checking,
		Downloading:     snap.Downloading,
		Error:           snap.Err,
		AllReleasesURL:  update.ReleasesPageURL(""),
	}
	if !st.Supported {
		st.DataFloor = dataFloor
	}
	if !snap.CheckedAt.IsZero() {
		t := snap.CheckedAt
		st.LastChecked = &t
	}
	rel := snap.Latest()
	if rel == nil {
		return st
	}
	st.Latest = rel.Version
	st.Tag = rel.Tag
	st.HTMLURL = rel.HTMLURL
	st.HasInstaller = rel.PkgURL != ""
	if !rel.PublishedAt.IsZero() {
		p := rel.PublishedAt
		st.PublishedAt = &p
	}

	// Show the notes of every release the user skipped, newest first. For a
	// dev/unknown current version NewerThan returns nothing, so fall back to the
	// latest release's own notes for display.
	skipped := update.NewerThan(snap.Releases, cur)
	st.SkippedCount = len(skipped)
	st.Notes = aggregateNotes(skipped, rel)

	// An update is offered only when we can compare versions, the release is
	// strictly newer, it ships an installer, and the user hasn't dismissed
	// exactly this version.
	if st.Supported && !restart && rel.PkgURL != "" &&
		update.IsNewer(rel.Version, cur) &&
		rel.Version != update.Normalize(dismissed) {
		st.Available = true
	}
	// A development build is never offered an update, but it can go back to the
	// latest release, provided that release still reads the data it finds.
	if !st.Supported && rel.PkgURL != "" && update.Compare(rel.Version, dataFloor) >= 0 {
		st.Reinstall = true
	}
	return st
}

// aggregateNotes joins the notes of the skipped releases (newest first), each
// under a "DispatchQ <version>" heading. With none skipped (e.g. a dev build) it
// falls back to the latest release's plain notes.
func aggregateNotes(skipped []update.Release, latest *update.Release) string {
	if len(skipped) == 0 {
		return latest.Notes
	}
	var b strings.Builder
	for i, r := range skipped {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("DispatchQ ")
		b.WriteString(r.Version)
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(r.Notes))
	}
	return b.String()
}

// dataFloor is update.DataFloor, a variable so tests can move it.
var dataFloor = update.DataFloor

// installedApp is the newest DispatchQ.app on disk, nil when none is found. Its
// version is what the running daemon's version is compared against to notice
// an update that was installed but never took over (see updateStatus.
// RestartRequired), and what the relaunch endpoint hands the LaunchAgent to.
// It is a variable so tests are not at the mercy of whatever is installed on
// the machine running them.
var installedApp = detectInstalledApp

func detectInstalledApp() *update.Installed { return update.InstalledApp(selfPath()) }

func installedVersion() string {
	if inst := installedApp(); inst != nil {
		return inst.Version
	}
	return ""
}

// selfPath is this executable's path, "" if it cannot be determined.
func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// currentStatus reads the dismissed version from the store and combines it with
// the update service snapshot.
func (s *server) currentStatus() updateStatus {
	snap := s.d.Updates.Snapshot()
	dismissed := ""
	if st, err := s.d.Store.LoadState(); err == nil {
		dismissed = st.DismissedUpdate()
	}
	return buildUpdateStatus(version.String(), installedVersion(), dismissed, snap)
}

func (s *server) getUpdate(w http.ResponseWriter, _ *http.Request) {
	if s.d.Updates == nil {
		st := buildUpdateStatus(version.String(), installedVersion(), "", update.Snapshot{})
		st.Unavailable = true
		writeJSON(w, http.StatusOK, st)
		return
	}
	writeJSON(w, http.StatusOK, s.currentStatus())
}

// checkUpdate forces an immediate GitHub check, then returns the fresh status.
func (s *server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	if s.d.Updates == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("update checks are not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// Ignore the error here: a failed check records itself in the snapshot, so
	// currentStatus() surfaces it (Error field) rather than returning a bare
	// HTTP error the UI can't explain.
	_, _ = s.d.Updates.CheckNow(ctx)
	writeJSON(w, http.StatusOK, s.currentStatus())
}

// dismissUpdate records the dismissed version so this release stops prompting;
// a newer release later supersedes it.
func (s *server) dismissUpdate(w http.ResponseWriter, r *http.Request) {
	if s.d.Updates == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("update checks are not available"))
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Version == "" {
		writeErr(w, http.StatusBadRequest, errors.New("version is required"))
		return
	}
	err := s.d.Store.UpdateState(func(st *store.State) error {
		st.DismissUpdate(update.Normalize(in.Version))
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.currentStatus())
}

// downloadUpdate downloads the latest installer and opens it. It blocks until
// the download finishes (a package is ~16 MB), so the client shows a
// "Downloading…" state meanwhile.
func (s *server) downloadUpdate(w http.ResponseWriter, r *http.Request) {
	if s.d.Updates == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("update downloads are not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	path, err := s.d.Updates.Download(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// reinstallRelease replaces a development build with the latest release: it
// downloads that release's installer and opens it, like downloadUpdate. It
// refuses when the status does not offer it, so a release that cannot read
// this build's data is never installed over it.
func (s *server) reinstallRelease(w http.ResponseWriter, r *http.Request) {
	if s.d.Updates == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("update downloads are not available"))
		return
	}
	st := s.currentStatus()
	switch {
	case st.Supported:
		writeErr(w, http.StatusConflict, errors.New("only a development build reinstalls the latest release"))
		return
	case st.Latest == "":
		writeErr(w, http.StatusConflict, errors.New("no release is known yet; check for updates first"))
		return
	case !st.Reinstall && !st.HasInstaller:
		writeErr(w, http.StatusConflict, fmt.Errorf("DispatchQ %s has no installer", st.Latest))
		return
	case !st.Reinstall:
		writeErr(w, http.StatusConflict, fmt.Errorf("DispatchQ %s cannot read this build's data; %s or later can", st.Latest, st.DataFloor))
		return
	}
	s.downloadUpdate(w, r)
}

// relaunchInstalled hands the LaunchAgent over to the DispatchQ.app at path by
// running that bundle's own `dispatchqd install`. Injectable for tests.
var relaunchInstalled = spawnInstalledDaemon

// spawnInstalledDaemon starts `<app>/Contents/MacOS/dispatchqd install` detached.
// That command stops the running daemon (this process) and re-bootstraps the
// LaunchAgent at the installed binary — the same hand-over the installer's
// postinstall performs, minus the parts that need root.
func spawnInstalledDaemon(app string) error {
	bin := filepath.Join(app, "Contents", "MacOS", "dispatchqd")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("installed daemon not found at %s: %w", bin, err)
	}
	cmd := exec.Command(bin, "install")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s install: %w", bin, err)
	}
	// Reap it if this process happens to outlive the hand-over.
	go func() { _ = cmd.Wait() }()
	return nil
}

// relaunchUpdate switches the background service over to the newest DispatchQ.app
// on disk. It answers the case where an installer ran fine but the old daemon
// kept running: the update is already installed, it just never took effect.
func (s *server) relaunchUpdate(w http.ResponseWriter, _ *http.Request) {
	if s.d.Updates == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("updates are not available"))
		return
	}
	inst := installedApp()
	if inst == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no installed DispatchQ.app was found"))
		return
	}
	if err := relaunchInstalled(inst.Path); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The daemon is about to be replaced, so answer before that happens.
	writeJSON(w, http.StatusAccepted, map[string]string{"version": inst.Version, "app": inst.Path})
}
