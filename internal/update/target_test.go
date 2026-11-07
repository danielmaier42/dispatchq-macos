package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

var (
	macARM = Target{OS: OSMacOS, Arch: ArchARM64}
	macX64 = Target{OS: OSMacOS, Arch: ArchX64}
	winX64 = Target{OS: OSWindows, Arch: ArchX64}
	winARM = Target{OS: OSWindows, Arch: ArchARM64}
)

func TestTargetFor(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         Target
		supported    bool
		ext          string
	}{
		{"darwin", "arm64", macARM, true, ".pkg"},
		{"darwin", "amd64", macX64, true, ".pkg"},
		{"windows", "amd64", winX64, true, ".exe"},
		{"windows", "arm64", winARM, true, ".exe"},
		{"windows", "386", Target{OS: OSWindows}, false, ".exe"},
		{"linux", "amd64", Target{Arch: ArchX64}, false, ""},
	} {
		got := targetFor(tc.goos, tc.goarch)
		if got != tc.want || got.Supported() != tc.supported || got.Ext() != tc.ext {
			t.Errorf("targetFor(%s, %s) = %+v supported=%v ext=%q, want %+v %v %q",
				tc.goos, tc.goarch, got, got.Supported(), got.Ext(), tc.want, tc.supported, tc.ext)
		}
	}
}

// assets builds release assets whose URL is their name.
func assets(names ...string) []Asset {
	out := make([]Asset, 0, len(names))
	for _, n := range names {
		out = append(out, Asset{Name: n, URL: "https://example.test/" + n})
	}
	return out
}

func TestSelectInstaller(t *testing.T) {
	// One release carrying every platform's installer plus noise that must
	// never be chosen: wrong ending, other version, upper case, checksums.
	mixed := assets(
		"SHA256SUMS",
		"dispatchq-2.1.0.pkg", // legacy name listed before the canonical one
		"dispatchq-2.1.0-windows-x64-beta.exe",
		"dispatchq-2.1.0-macos-arm64.pkg",
		"dispatchq-2.1.0-windows-x64.exe",
		"dispatchq-2.1.0-windows-arm64-beta.exe",
		"dispatchq-2.0.9-windows-arm64.exe",     // filename version mismatch
		"DispatchQ-2.1.0-windows-arm64.exe",     // not lowercase
		"dispatchq-2.1.0-windows-arm64.pkg",     // ending of another system
		"dispatchq-2.1.0-macos-arm64-beta.pkg",  // stable exists, beta loses
		"dispatchq-2.1.0-windows-x86.exe",       // unknown architecture
		"dispatchq-2.1.0-linux-x64.exe",         // unknown system
		"dispatchq-2.1.0-windows-arm64-rc1.exe", // unknown suffix
	)
	for _, tc := range []struct {
		name    string
		target  Target
		version string
		assets  []Asset
		want    string // "" = no installer
	}{
		{"mac arm64 prefers canonical over legacy", macARM, "2.1.0", mixed, "dispatchq-2.1.0-macos-arm64.pkg"},
		{"windows x64 prefers stable over beta of the same version", winX64, "2.1.0", mixed, "dispatchq-2.1.0-windows-x64.exe"},
		{"windows arm64 takes the beta, rejecting mismatches", winARM, "2.1.0", mixed, "dispatchq-2.1.0-windows-arm64-beta.exe"},
		{"intel mac gets no arm64 or legacy package", macX64, "2.1.0", mixed, ""},
		{"unsupported system gets nothing", Target{OS: "linux", Arch: ArchX64}, "2.1.0", mixed, ""},
		{"historic mac release keeps its legacy package", macARM, "2.0.13", assets("dispatchq-2.0.13.pkg"), "dispatchq-2.0.13.pkg"},
		{"legacy package is never a windows installer", winX64, "2.0.13", assets("dispatchq-2.0.13.pkg"), ""},
		{"legacy package of another version is rejected", macARM, "2.0.13", assets("dispatchq-2.0.12.pkg"), ""},
		{"any other pkg is not a legacy installer", macARM, "2.0.13", assets("claudeq-2.0.13.pkg", "dispatchq-uninstall.pkg"), ""},
		{"filename version must match exactly", winX64, "2.1.0", assets("dispatchq-2.1-windows-x64.exe"), ""},
		{"stable beats beta listed first", macARM, "2.2.0", assets("dispatchq-2.2.0-macos-arm64-beta.pkg", "dispatchq-2.2.0-macos-arm64.pkg"), "dispatchq-2.2.0-macos-arm64.pkg"},
		{"doubled beta suffix is malformed", winX64, "2.1.0", assets("dispatchq-2.1.0-windows-x64-beta-beta.exe"), ""},
		{"non-numeric release version", winX64, "2.1.0-rc1", assets("dispatchq-2.1.0-rc1-windows-x64.exe"), ""},
		{"asset without a download URL", winX64, "2.1.0", []Asset{{Name: "dispatchq-2.1.0-windows-x64.exe"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SelectInstaller(tc.target, tc.version, tc.assets)
			if ok != (tc.want != "") || got.Name != tc.want {
				t.Fatalf("SelectInstaller = %q, %v; want %q", got.Name, ok, tc.want)
			}
			if ok && got.URL != "https://example.test/"+tc.want {
				t.Fatalf("URL = %q does not belong to %q", got.URL, tc.want)
			}
		})
	}
}

// mixedReleasesJSON is a release feed in which each release ships installers
// for several systems, as the release workflow publishes them.
const mixedReleasesJSON = `[
  {"tag_name": "v2.3.0", "body": "mac only", "assets": [
    {"name": "dispatchq-2.3.0-macos-arm64.pkg", "browser_download_url": "https://example.test/dispatchq-2.3.0-macos-arm64.pkg"}]},
  {"tag_name": "v2.2.0", "body": "windows beta", "assets": [
    {"name": "dispatchq-2.2.0-macos-arm64.pkg", "browser_download_url": "https://example.test/dispatchq-2.2.0-macos-arm64.pkg"},
    {"name": "dispatchq-2.2.0-windows-x64-beta.exe", "browser_download_url": "https://example.test/dispatchq-2.2.0-windows-x64-beta.exe"},
    {"name": "dispatchq-2.2.0-windows-arm64-beta.exe", "browser_download_url": "https://example.test/dispatchq-2.2.0-windows-arm64-beta.exe"}]},
  {"tag_name": "v2.1.0", "body": "windows stable", "assets": [
    {"name": "dispatchq-2.1.0-windows-arm64.exe", "browser_download_url": "https://example.test/dispatchq-2.1.0-windows-arm64.exe"},
    {"name": "dispatchq-2.1.0-windows-x64.exe", "browser_download_url": "https://example.test/dispatchq-2.1.0-windows-x64.exe"},
    {"name": "dispatchq-2.1.0-macos-arm64.pkg", "browser_download_url": "https://example.test/dispatchq-2.1.0-macos-arm64.pkg"}]},
  {"tag_name": "v2.0.13", "body": "historic", "assets": [
    {"name": "dispatchq-2.0.13.pkg", "browser_download_url": "https://example.test/dispatchq-2.0.13.pkg"}]}
]`

// The fetcher attaches each release's installer for its target, and Latest
// picks the newest installable one: a higher beta beats a lower stable release,
// and a newer release without this system's installer never hides it.
func TestGitHubFetcherTargetInstallers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mixedReleasesJSON))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		target     Target
		perVersion map[string]string
		latest     string
	}{
		{macARM, map[string]string{
			"2.3.0": "dispatchq-2.3.0-macos-arm64.pkg", "2.2.0": "dispatchq-2.2.0-macos-arm64.pkg",
			"2.1.0": "dispatchq-2.1.0-macos-arm64.pkg", "2.0.13": "dispatchq-2.0.13.pkg",
		}, "2.3.0"},
		{winX64, map[string]string{
			"2.2.0": "dispatchq-2.2.0-windows-x64-beta.exe", "2.1.0": "dispatchq-2.1.0-windows-x64.exe",
		}, "2.2.0"},
		{winARM, map[string]string{
			"2.2.0": "dispatchq-2.2.0-windows-arm64-beta.exe", "2.1.0": "dispatchq-2.1.0-windows-arm64.exe",
		}, "2.2.0"},
	} {
		t.Run(tc.target.String(), func(t *testing.T) {
			rels, err := GitHubFetcher{BaseURL: srv.URL, Target: tc.target}.Releases(context.Background())
			if err != nil {
				t.Fatalf("Releases: %v", err)
			}
			if len(rels) != 4 {
				t.Fatalf("got %d releases, want 4", len(rels))
			}
			for _, r := range rels {
				want := tc.perVersion[r.Version]
				if r.PkgName != want || (want == "" && r.PkgURL != "") ||
					(want != "" && r.PkgURL != "https://example.test/"+want) {
					t.Errorf("%s: installer %q (%q), want %q", r.Version, r.PkgName, r.PkgURL, want)
				}
			}
			if got := Latest(rels); got == nil || got.Version != tc.latest || got.PkgName != tc.perVersion[tc.latest] {
				t.Errorf("Latest = %+v, want %s", got, tc.latest)
			}
		})
	}
}

// A current Windows beta is offered the next normal release, whose file name
// no longer carries "-beta".
func TestWindowsBetaUpdatesToStableRelease(t *testing.T) {
	rels := []Release{
		{Version: "2.0.14", PkgName: "dispatchq-2.0.14-windows-x64-beta.exe", PkgURL: "u14"},
		{Version: "2.1.0", PkgName: "dispatchq-2.1.0-windows-x64.exe", PkgURL: "u21"},
	}
	newer := NewerThan(rels, "2.0.14")
	if len(newer) != 1 || newer[0].Version != "2.1.0" {
		t.Fatalf("NewerThan = %+v", newer)
	}
	if got := Latest(rels); got.PkgName != "dispatchq-2.1.0-windows-x64.exe" {
		t.Fatalf("Latest = %+v", got)
	}
}
