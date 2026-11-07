// Package webview2 finds the Microsoft Edge WebView2 Runtime the way the
// DispatchQ window finds it.
//
// The window uses the webview library's built-in WebView2 loader (webview_go,
// libs/webview/include/webview.h, class mswebview2::loader). It does not read
// the "pv" version Microsoft documents for installers. It reads the runtime's
// EBWebView folder from EdgeUpdate's ClientState key in the 32-bit registry
// view, first per machine and then per user, takes the first entry whose
// version is new enough, and loads EBWebView\<arch>\EmbeddedBrowserWebView.dll
// from that folder with LoadLibraryW, then needs its export
// CreateWebViewEnvironmentWithOptionsInternal. Probe repeats exactly that
// choice and hands the chosen library to a check (CheckClient, which loads it
// the same way in a separate, time-limited process on Windows), and reports
// the documented "pv" entries next to it so a wrong answer can be told apart
// from a missing runtime.
//
// The library would use Microsoft's WebView2Loader.dll instead if
// LoadLibraryW("WebView2Loader.dll") found one from the window's folder or the
// Windows DLL search path. DispatchQ does not ship it and Probe does not look
// for it; such a DLL is the one case in which the window's answer may differ
// from Probe's, in either direction.
package webview2

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// Hive is the registry root a registration lives under.
type Hive int

const (
	// Machine is HKEY_LOCAL_MACHINE: a per-machine runtime.
	Machine Hive = iota
	// User is HKEY_CURRENT_USER: a per-user runtime.
	User
)

func (h Hive) String() string {
	if h == Machine {
		return "HKLM"
	}
	return "HKCU"
}

// View is the registry view a key is read in.
type View int

const (
	// View32 is the 32-bit view (KEY_WOW64_32KEY). EdgeUpdate is a 32-bit
	// program and registers the runtime there; on 64-bit Windows its machine
	// keys are stored under SOFTWARE\WOW6432Node.
	View32 View = iota
	// View64 is the 64-bit view (KEY_WOW64_64KEY).
	View64
)

// Registry reads one string value. ok is false when the key cannot be
// opened or the value does not exist.
type Registry interface {
	String(hive Hive, view View, path, name string) (value string, ok bool)
}

// StableClientID is the EdgeUpdate client id of the Evergreen WebView2 Runtime.
const StableClientID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

const (
	// clientStatePath holds the EBWebView folder the window's loader reads.
	clientStatePath = `SOFTWARE\Microsoft\EdgeUpdate\ClientState\` + StableClientID
	// clientsPath holds the "pv" version Microsoft documents for installers.
	clientsPath = `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + StableClientID
)

// MinBuild is the oldest runtime build (third version component) the
// window's loader accepts (mswebview2::loader::api_version).
const MinBuild = 1150

// Arch is the folder name of the runtime client for a Go architecture, the
// one the window's loader picks for its own build. "" for an architecture
// WebView2 has no client for.
func Arch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	}
	return ""
}

// Entry is one registry value Probe read.
type Entry struct {
	Where string // e.g. "HKLM 32-bit view"
	Value string // "" when Found is false
	Found bool
}

// Status is what Probe found.
type Status struct {
	// Available is true when the window's loader picks a runtime and the
	// check accepted that runtime's client library.
	Available bool
	// Version and Scope ("per-machine", "per-user") of the runtime the
	// loader picks; empty when it picks none.
	Version string
	Scope   string
	// Client is the client library the loader would load.
	Client string
	// Reason says why the window will not find a runtime.
	Reason string
	// Loader lists the EBWebView entries the loader reads, in its order.
	Loader []Entry
	// Registered lists the documented "pv" entries.
	Registered []Entry
}

// Probe answers whether the window, built for arch (see Arch), will find a
// usable WebView2 Runtime. check answers whether the window can use a client
// library (nil) or why not; it is called at most once.
func Probe(reg Registry, check func(client string) error, arch string) Status {
	var st Status
	for _, e := range []struct {
		hive Hive
		view View
	}{{Machine, View32}, {Machine, View64}, {User, View32}} {
		v, ok := reg.String(e.hive, e.view, clientsPath, "pv")
		st.Registered = append(st.Registered, Entry{Where: e.hive.String() + " " + viewName(e.view), Value: v, Found: ok})
	}

	var skipped []string
	for _, hive := range []Hive{Machine, User} {
		folder, ok := reg.String(hive, View32, clientStatePath, "EBWebView")
		st.Loader = append(st.Loader, Entry{Where: hive.String(), Value: folder, Found: ok})
		if !ok {
			continue
		}
		version := lastPathComponent(folder)
		if build := parseVersion(version)[2]; build < MinBuild {
			skipped = append(skipped, fmt.Sprintf("%s entry %q is not a runtime of build %d or later", hive, version, MinBuild))
			continue
		}
		// The loader stops at the first acceptable entry, even when its
		// client library turns out to be missing or broken.
		st.Version = version
		st.Scope = scope(hive)
		if arch == "" {
			st.Reason = "this processor architecture has no WebView2 client"
			return st
		}
		st.Client = clientLibrary(folder, arch)
		if err := check(st.Client); err != nil {
			st.Reason = fmt.Sprintf("the %s runtime %s has no usable %s client library: %v", st.Scope, version, arch, err)
			return st
		}
		st.Available = true
		return st
	}
	st.Reason = "no runtime registered for the window's loader"
	if len(skipped) > 0 {
		st.Reason += " (" + strings.Join(skipped, "; ") + ")"
	}
	return st
}

// Write prints st on one line for the installer's details log and a
// terminal. sanitize shortens paths that name the user (it may be nil).
func (st Status) Write(w io.Writer, sanitize func(string) string) error {
	if sanitize == nil {
		sanitize = func(s string) string { return s }
	}
	var b strings.Builder
	if st.Available {
		fmt.Fprintf(&b, "WebView2 Runtime available: %s %s", st.Scope, st.Version)
	} else {
		fmt.Fprintf(&b, "WebView2 Runtime not usable: %s", st.Reason)
	}
	b.WriteString(". Window loader (ClientState EBWebView):")
	for _, e := range st.Loader {
		fmt.Fprintf(&b, " %s=%s", e.Where, entryText(e, sanitize))
	}
	if st.Client != "" {
		fmt.Fprintf(&b, ", client %s", sanitize(st.Client))
	}
	b.WriteString(". Registered (Clients pv):")
	for i, e := range st.Registered {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, " %s=%s", e.Where, entryText(e, sanitize))
	}
	b.WriteString(".")
	_, err := fmt.Fprintln(w, b.String())
	return err
}

// Sanitizer replaces the given folders (such as the user profile) at the start
// of a path with their names, so a log does not carry the user's name.
// Longer folders win; empty ones are ignored.
func Sanitizer(folders map[string]string) func(string) string {
	return func(s string) string {
		best, name := "", ""
		for placeholder, dir := range folders {
			dir = strings.TrimRight(dir, `\/`)
			if dir == "" || len(dir) <= len(best) {
				continue
			}
			if len(s) >= len(dir) && strings.EqualFold(s[:len(dir)], dir) &&
				(len(s) == len(dir) || s[len(dir)] == '\\' || s[len(dir)] == '/') {
				best, name = dir, placeholder
			}
		}
		if best == "" {
			return s
		}
		return name + s[len(best):]
	}
}

func entryText(e Entry, sanitize func(string) string) string {
	switch {
	case !e.Found:
		return "none"
	case e.Value == "":
		return `""`
	}
	return sanitize(e.Value)
}

func viewName(v View) string {
	if v == View64 {
		return "64-bit view"
	}
	return "32-bit view"
}

func scope(h Hive) string {
	if h == Machine {
		return "per-machine"
	}
	return "per-user"
}

// lastPathComponent is the text after the last backslash, "" without one
// (webview.h get_last_native_path_component).
func lastPathComponent(path string) string {
	i := strings.LastIndexByte(path, '\\')
	if i < 0 {
		return ""
	}
	return path[i+1:]
}

// clientLibrary is the client DLL path in a runtime folder (webview.h
// make_client_dll_path).
func clientLibrary(folder, arch string) string {
	if folder != "" && !strings.HasSuffix(folder, `\`) && !strings.HasSuffix(folder, "/") {
		folder += `\`
	}
	return folder + `EBWebView\` + arch + `\EmbeddedBrowserWebView.dll`
}

// parseVersion reads up to four dot-separated numbers like webview.h
// parse_version: a component that does not start with a number, or is
// negative, counts as 0, and trailing text after the digits is ignored.
func parseVersion(v string) [4]uint64 {
	var out [4]uint64
	for i, part := range strings.SplitN(v, ".", 5) {
		if i == len(out) {
			break
		}
		out[i] = leadingNumber(part)
	}
	return out
}

func leadingNumber(s string) uint64 {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	s = strings.TrimPrefix(s, "+")
	var n uint64
	digits := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
		if n > math.MaxInt32 {
			return 0 // std::stol on Windows (32-bit long) throws: counts as 0
		}
		digits++
	}
	if digits == 0 {
		return 0
	}
	return n
}
