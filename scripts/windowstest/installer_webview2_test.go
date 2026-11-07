package windowstest

import (
	"strings"
	"testing"
)

// The installer's WebView2 handling, run through the same interpreter as the
// stop-and-replace flow, with the bootstrapper bundled as in CI builds. The
// registry is modelled by where an x86 process on 64-bit Windows reads it (see
// regLocation). dispatchqd webview2-status repeats the window's own lookup and
// answers 0 (usable), 10 (not usable) or fails to run.

const (
	wv2Clients = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`
	// Physical locations of the documented "pv" entries.
	wv2Machine32 = `hklm\software\wow6432node\microsoft\edgeupdate\clients\{f3017226-fe2a-4295-8bdf-00c3a9a7e4c5}|pv`
	wv2Machine64 = `hklm\software\microsoft\edgeupdate\clients\{f3017226-fe2a-4295-8bdf-00c3a9a7e4c5}|pv`
	wv2User      = `hkcu\software\microsoft\edgeupdate\clients\{f3017226-fe2a-4295-8bdf-00c3a9a7e4c5}|pv`

	// The result the Windows beta showed on a laptop where an earlier build's
	// window opened (0x80040828); its meaning is not documented.
	wv2BootstrapperResult = "-2147219416"
)

// wv2Scenario is the PC: its registry, the answers of the window's check
// before and after the bootstrapper, and the bootstrapper's exit code
// ("error": it could not be started).
type wv2Scenario struct {
	registry     map[string]string
	checkBefore  string
	checkAfter   string
	bootstrapper string
	// afterBootstrap is merged into the registry when the bootstrapper ran.
	afterBootstrap map[string]string
}

type wv2Result struct {
	run          *installerRun
	bootstrapped bool
}

func runWebView2(t *testing.T, sc wv2Scenario, defines ...string) wv2Result {
	t.Helper()
	prog := preprocess(t, installerScript(), defines...)
	var res wv2Result
	r := newRun(t, prog, map[string]string{}, nil)
	r.exits = func(program, sub string) (string, string) {
		switch {
		case strings.Contains(program, "webview2-bootstrapper"):
			res.bootstrapped = true
			for k, v := range sc.afterBootstrap {
				r.registry[k] = v
			}
			return sc.bootstrapper, ""
		case sub == "webview2-status":
			code := sc.checkBefore
			if res.bootstrapped {
				code = sc.checkAfter
			}
			switch code {
			case "0":
				return code, "WebView2 Runtime available: per-machine 131.0.2903.70. Window loader (ClientState EBWebView): HKLM=C:\\Program Files (x86)\\Microsoft\\EdgeWebView\\Application\\131.0.2903.70"
			case "10":
				return code, "WebView2 Runtime not usable: no runtime registered for the window's loader. Window loader (ClientState EBWebView): HKLM=none HKCU=none. Registered (Clients pv): HKLM 32-bit view=none, HKLM 64-bit view=none, HKCU 32-bit view=none."
			}
			return code, ""
		}
		return "0", "ok"
	}
	for k, v := range sc.registry {
		r.registry[k] = v
	}
	res.run = r
	r.call("Section DispatchQ")
	if r.aborted || !r.finishShown() || len(r.violations) > 0 {
		t.Fatalf("the installation did not complete: aborted %v, violations %v\n%s", r.aborted, r.violations, r.dump())
	}
	return res
}

func (res wv2Result) warning() string {
	for _, m := range res.run.messages {
		if strings.Contains(m, "WebView2") {
			return m
		}
	}
	return ""
}

// A runtime the window will use is never reinstalled and never reported
// missing, whatever the documented entries say.
func TestWebView2UsableRuntimeIsNotReinstalled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		registry map[string]string
	}{
		{"documented per-machine entry", map[string]string{wv2Machine32: "131.0.2903.70"}},
		// What the window's loader reads (ClientState) is not what Microsoft
		// documents for installers (Clients pv): the two can disagree.
		{"no documented entry", nil},
		{"stale per-machine entry, per-user runtime", map[string]string{wv2Machine32: "0.0.0.0", wv2User: "130.0.2849.80"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runWebView2(t, wv2Scenario{registry: tc.registry, checkBefore: "0", checkAfter: "0", bootstrapper: wv2BootstrapperResult}, "-DBOOTSTRAPPER")
			if res.bootstrapped {
				t.Errorf("the bootstrapper ran although the window's runtime is usable\n%s", res.run.dump())
			}
			if w := res.warning(); w != "" {
				t.Errorf("setup warned about a usable runtime: %s", w)
			}
		})
	}
}

// When the window's check cannot run, the documented registration decides,
// read in both registry views and with a stale per-machine entry falling
// back to the next location.
func TestWebView2RegistryFallbackWhenCheckCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		registry  map[string]string
		installed bool
	}{
		{"per-machine in the 32-bit view", map[string]string{wv2Machine32: "131.0.2903.70"}, true},
		{"stale per-machine 0.0.0.0, per-user runtime", map[string]string{wv2Machine32: "0.0.0.0", wv2User: "130.0.2849.80"}, true},
		{"empty per-machine entry, per-user runtime", map[string]string{wv2Machine32: "", wv2User: "130.0.2849.80"}, true},
		{"per-machine only in the 64-bit view", map[string]string{wv2Machine64: "131.0.2903.70"}, true},
		{"stale everywhere", map[string]string{wv2Machine32: "0.0.0.0", wv2User: "0.0.0.0"}, false},
		{"nothing registered", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runWebView2(t, wv2Scenario{registry: tc.registry, checkBefore: "error", checkAfter: "error", bootstrapper: "0"}, "-DBOOTSTRAPPER")
			if res.bootstrapped == tc.installed {
				t.Errorf("bootstrapper ran = %v for a registered runtime = %v\n%s", res.bootstrapped, tc.installed, res.run.dump())
			}
			if w := res.warning(); (w != "") == tc.installed {
				t.Errorf("warning %q for a registered runtime = %v", w, tc.installed)
			}
		})
	}
}

// The registration is read at exactly the documented key, by its logical path
// through the registry view, never by the physical WOW6432Node path Windows
// reserves. (The first beta prefixed SOFTWARE\WOW6432Node to a key that
// already began with Software\, so it read a key that never exists and missed
// every per-machine runtime.)
func TestWebView2RegistryReadsUseTheDocumentedKey(t *testing.T) {
	res := runWebView2(t, wv2Scenario{checkBefore: "error", checkAfter: "error", bootstrapper: "0"}, "-DBOOTSTRAPPER")
	reads := map[string]bool{}
	for _, line := range res.run.trace {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "read:" {
			continue
		}
		root, path, view := f[1], f[2], f[4]
		if !strings.EqualFold(path, wv2Clients) {
			t.Errorf("setup reads %s %s, not the documented key %s", root, path, wv2Clients)
		}
		reads[root+" "+view] = true
	}
	for _, want := range []string{"HKLM 32", "HKLM 64", "HKCU 32"} {
		if !reads[want] {
			t.Errorf("no read of %s view (reads %v)\n%s", want, reads, res.run.dump())
		}
	}
}

// A runtime that is still not usable after the bootstrapper is reported with
// the exit code in decimal and hex and what the check found; the rest of the
// installation completes.
func TestWebView2FailedBootstrapperWarnsWithDetails(t *testing.T) {
	res := runWebView2(t, wv2Scenario{checkBefore: "10", checkAfter: "10", bootstrapper: wv2BootstrapperResult}, "-DBOOTSTRAPPER")
	if !res.bootstrapped {
		t.Fatalf("the bootstrapper did not run for a missing runtime\n%s", res.run.dump())
	}
	w := res.warning()
	for _, want := range []string{
		"exit code -2147219416, 0x80040828",
		"no runtime registered for the window's loader",
		"https://developer.microsoft.com/microsoft-edge/webview2/",
		"dispatchqd webview2-status",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warning lacks %q:\n%s", want, w)
		}
	}
	if indexOf(res.run.trace, `run: "$INSTDIR\dispatchqd.exe" install`) < 0 {
		t.Errorf("the service was not registered after the WebView2 warning\n%s", res.run.dump())
	}
}

// An error exit code alone is no failure: when the runtime is usable after
// the bootstrapper, nothing is reported.
func TestWebView2BootstrapperErrorWithUsableRuntimeIsSilent(t *testing.T) {
	res := runWebView2(t, wv2Scenario{checkBefore: "10", checkAfter: "0", bootstrapper: wv2BootstrapperResult}, "-DBOOTSTRAPPER")
	if !res.bootstrapped {
		t.Fatalf("the bootstrapper did not run\n%s", res.run.dump())
	}
	if w := res.warning(); w != "" {
		t.Errorf("setup warned although the runtime is usable afterwards: %s", w)
	}
}

// A bootstrapper that cannot be started is named as such.
func TestWebView2BootstrapperNotStarted(t *testing.T) {
	res := runWebView2(t, wv2Scenario{checkBefore: "10", checkAfter: "10", bootstrapper: "error"}, "-DBOOTSTRAPPER")
	if w := res.warning(); !strings.Contains(w, "installer result: could not be started") {
		t.Errorf("warning does not say the bootstrapper could not be started:\n%s", w)
	}
}

// Without a bundled bootstrapper, only a runtime the window will not find is
// reported, with what the check found.
func TestWebView2WithoutBootstrapper(t *testing.T) {
	res := runWebView2(t, wv2Scenario{checkBefore: "0"})
	if w := res.warning(); w != "" {
		t.Errorf("warning for a usable runtime: %s", w)
	}
	res = runWebView2(t, wv2Scenario{checkBefore: "10"})
	if w := res.warning(); !strings.Contains(w, "no runtime registered for the window's loader") {
		t.Errorf("warning lacks the check's finding:\n%s", w)
	}
}
