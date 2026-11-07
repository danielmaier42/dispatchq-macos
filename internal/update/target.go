package update

import (
	"runtime"
	"strings"
)

// Platform and architecture labels as they appear in installer file names.
const (
	OSMacOS   = "macos"
	OSWindows = "windows"
	ArchARM64 = "arm64"
	ArchX64   = "x64"
)

// Target is the system an installer is built for: the operating system and
// processor architecture labels of the release asset names
// dispatchq-<version>-<os>-<arch>[-beta].<ending>.
type Target struct {
	OS   string
	Arch string
}

// CurrentTarget is the system this program was built for. A system without a
// published installer yields a Target that is not [Target.Supported].
func CurrentTarget() Target { return targetFor(runtime.GOOS, runtime.GOARCH) }

// targetFor maps Go's GOOS/GOARCH to the installer labels.
func targetFor(goos, goarch string) Target {
	var t Target
	switch goos {
	case "darwin":
		t.OS = OSMacOS
	case "windows":
		t.OS = OSWindows
	}
	switch goarch {
	case "arm64":
		t.Arch = ArchARM64
	case "amd64":
		t.Arch = ArchX64
	}
	return t
}

// Supported reports whether installers are published for t at all.
func (t Target) Supported() bool { return t.Ext() != "" && (t.Arch == ArchARM64 || t.Arch == ArchX64) }

// Ext is the file ending of t's installer, with the dot: ".pkg" on macOS and
// ".exe" on Windows, "" for any other system.
func (t Target) Ext() string {
	switch t.OS {
	case OSMacOS:
		return ".pkg"
	case OSWindows:
		return ".exe"
	}
	return ""
}

// String is the "<os>-<arch>" part of an installer name, e.g. "windows-x64".
func (t Target) String() string { return t.OS + "-" + t.Arch }

// Asset is a file attached to a release.
type Asset struct {
	Name string
	URL  string
}

// installerName is what an asset's file name says about the installer in it.
type installerName struct {
	version string
	target  Target
	beta    bool
}

// parseInstallerName reads a canonical installer name,
// dispatchq-<version>-<os>-<arch>[-beta].<ending>, all lowercase. The version is
// plain dotted numbers, and the ending must belong to the operating system. Any
// other name, including the legacy dispatchq-<version>.pkg, is rejected.
func parseInstallerName(name string) (installerName, bool) {
	const prefix = "dispatchq-"
	if !strings.HasPrefix(name, prefix) {
		return installerName{}, false
	}
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return installerName{}, false
	}
	ext := name[dot:]
	fields := strings.Split(name[len(prefix):dot], "-")
	var in installerName
	switch {
	case len(fields) == 4 && fields[3] == "beta":
		in.beta = true
	case len(fields) == 3:
	default:
		return installerName{}, false
	}
	in.version, in.target = fields[0], Target{OS: fields[1], Arch: fields[2]}
	if !isPlainVersion(in.version) || !in.target.Supported() || in.target.Ext() != ext {
		return installerName{}, false
	}
	return in, true
}

// isPlainVersion reports whether v is dotted numbers only, e.g. "2.1.0".
func isPlainVersion(v string) bool {
	if v == "" {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// legacyMacTarget is the only system the legacy installer name
// dispatchq-<version>.pkg was ever built for: every macOS release up to and
// including 2.0.13 shipped Apple silicon programs only under that name.
var legacyMacTarget = Target{OS: OSMacOS, Arch: ArchARM64}

// SelectInstaller picks the installer for target from a release's assets.
// version is the release's normalized version, and an asset whose name carries
// another version is never chosen, nor is one for another operating system or
// architecture. Of the canonical names, the one without "-beta" wins over the
// beta for the same version. A release that has no canonical installer for an
// Apple silicon Mac falls back to the legacy name dispatchq-<version>.pkg, so
// the historic macOS releases stay installable.
func SelectInstaller(target Target, version string, assets []Asset) (Asset, bool) {
	if !target.Supported() || !isPlainVersion(version) {
		return Asset{}, false
	}
	var beta, legacy *Asset
	for i := range assets {
		a := &assets[i]
		if a.URL == "" {
			continue
		}
		if in, ok := parseInstallerName(a.Name); ok {
			if in.version != version || in.target != target {
				continue
			}
			if !in.beta {
				return *a, true
			}
			if beta == nil {
				beta = a
			}
			continue
		}
		if legacy == nil && target == legacyMacTarget && a.Name == "dispatchq-"+version+".pkg" {
			legacy = a
		}
	}
	switch {
	case beta != nil:
		return *beta, true
	case legacy != nil:
		return *legacy, true
	}
	return Asset{}, false
}
