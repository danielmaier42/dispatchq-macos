// Package update checks GitHub for newer dispatchq releases and downloads the
// installer, so the app can offer an in-app "Check for updates" flow plus a
// background hourly check (see the app's Settings pane). It talks only to
// GitHub's public releases API and downloads the release's own installer asset
// for this system (a `.pkg` on macOS, an `.exe` on Windows); nothing else
// leaves the machine (NFA-04). Opening the downloaded installer is the
// platform's job, handed in with [WithOpener].
package update

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DataFloor is the oldest release that runs on the data this build leaves
// behind. A development build offers to reinstall the latest release only when
// that release is at least this version, because an older one finds its data
// moved or rewritten in a way it cannot read. Raise it to the coming release
// whenever a change migrates data one way: 2.0.0 moved the ClaudeQ data
// directory and LaunchAgent, which 1.x would run beside instead of replacing.
const DataFloor = "2.0.0"

// Release describes a published dispatchq release and its installer asset.
type Release struct {
	// Version is the normalized dotted version, e.g. "0.1.4" (no leading "v").
	Version string `json:"version"`
	// Tag is the original git tag, e.g. "v0.1.4".
	Tag string `json:"tag"`
	// PkgURL is the browser download URL of the release's installer for this
	// system (see [SelectInstaller]), empty if the release has none. The name
	// predates the Windows installers and is kept for compatibility.
	PkgURL string `json:"pkg_url"`
	// PkgName is the installer's file name, e.g. "dispatchq-2.1.0-macos-arm64.pkg"
	// or "dispatchq-2.1.0-windows-x64-beta.exe".
	PkgName string `json:"pkg_name"`
	// Notes is the release body (GitHub-flavoured markdown).
	Notes string `json:"notes"`
	// HTMLURL is the release page on GitHub.
	HTMLURL string `json:"html_url"`
	// PublishedAt is when the release was published.
	PublishedAt time.Time `json:"published_at"`
}

// Fetcher retrieves recent published releases, newest first (drafts and
// pre-releases excluded). [GitHubFetcher] is the real implementation; tests
// substitute a fake.
type Fetcher interface {
	Releases(ctx context.Context) ([]Release, error)
}

// Latest returns the highest-versioned release that has an installer for this
// system, or, when none has, the highest-versioned release at all; nil if the
// list is empty. A newer release published without this system's installer
// thus never hides an older one that can be installed. GitHub returns releases
// newest-first, but we pick by version so an out-of-order publish can't hide a
// newer version.
func Latest(releases []Release) *Release {
	var latest, installable *Release
	for i := range releases {
		r := &releases[i]
		if latest == nil || IsNewer(r.Version, latest.Version) {
			latest = r
		}
		if r.PkgURL != "" && (installable == nil || IsNewer(r.Version, installable.Version)) {
			installable = r
		}
	}
	if installable != nil {
		return installable
	}
	return latest
}

// NewerThan returns the releases strictly newer than current, sorted
// newest-first — i.e. every version the user skipped. When current is not a
// real release version the result is empty (a dev build is never "behind").
func NewerThan(releases []Release, current string) []Release {
	if !IsReleaseVersion(current) {
		return nil
	}
	out := make([]Release, 0, len(releases))
	for _, r := range releases {
		if IsNewer(r.Version, current) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return IsNewer(out[i].Version, out[j].Version) })
	return out
}

// ReleasesPageURL is the GitHub page listing every release for a repo, so the
// user can always browse the full history. Empty repo uses [DefaultRepo].
func ReleasesPageURL(repo string) string {
	if repo == "" {
		repo = DefaultRepo
	}
	return "https://github.com/" + repo + "/releases"
}

// Normalize strips a leading "v" and surrounding whitespace from a version or
// tag string ("v0.1.4" -> "0.1.4").
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// IsReleaseVersion reports whether v looks like a real dotted release version
// rather than a development build ("dev") or an empty string. Only real
// versions can be meaningfully compared for updates, so a dev build never
// surfaces an "update available" prompt.
func IsReleaseVersion(v string) bool {
	v = Normalize(v)
	if v == "" {
		return false
	}
	stripped := v
	if i := strings.IndexAny(stripped, "-+"); i >= 0 {
		stripped = stripped[:i]
	}
	for _, part := range strings.Split(stripped, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

// Compare returns -1, 0, or 1 as dotted numeric version a is older than, equal
// to, or newer than b. A leading "v" is ignored, missing trailing components
// count as 0 (so "0.2" == "0.2.0"), and any pre-release/build suffix ("-rc1",
// "+meta") is dropped before comparison.
func Compare(a, b string) int {
	pa, pb := parts(a), parts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// IsNewer reports whether candidate is a strictly newer release than current.
func IsNewer(candidate, current string) bool {
	return Compare(candidate, current) > 0
}

// parts splits a version into its numeric components. Non-numeric fields count
// as 0, which keeps comparison total and panic-free on odd input.
func parts(v string) []int {
	v = Normalize(v)
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	fields := strings.Split(v, ".")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, _ := strconv.Atoi(f)
		out = append(out, n)
	}
	return out
}
