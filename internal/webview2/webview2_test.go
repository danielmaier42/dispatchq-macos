package webview2

import (
	"bytes"
	"strings"
	"testing"
)

type fakeReg map[string]string

func key(h Hive, v View, path, name string) string {
	return h.String() + "|" + viewName(v) + "|" + strings.ToLower(path) + "|" + name
}

func (f fakeReg) String(h Hive, v View, path, name string) (string, bool) {
	s, ok := f[key(h, v, path, name)]
	return s, ok
}

const (
	machineDir = `C:\Program Files (x86)\Microsoft\EdgeWebView\Application\131.0.2903.70`
	userDir    = `C:\Users\Jane Doe\AppData\Local\Microsoft\EdgeWebView\Application\130.0.2849.80`
)

func loaderEntry(h Hive, dir string) (string, string) {
	return key(h, View32, clientStatePath, "EBWebView"), dir
}

func reg(pairs ...[2]string) fakeReg {
	f := fakeReg{}
	for _, p := range pairs {
		f[p[0]] = p[1]
	}
	return f
}

func pair(k, v string) [2]string { return [2]string{k, v} }

func files(paths ...string) func(string) bool {
	return func(p string) bool {
		for _, q := range paths {
			if strings.EqualFold(p, q) {
				return true
			}
		}
		return false
	}
}

func TestProbeFollowsTheWindowLoader(t *testing.T) {
	machineX64 := machineDir + `\EBWebView\x64\EmbeddedBrowserWebView.dll`
	machineARM := machineDir + `\EBWebView\arm64\EmbeddedBrowserWebView.dll`
	userX64 := userDir + `\EBWebView\x64\EmbeddedBrowserWebView.dll`
	for _, tc := range []struct {
		name      string
		reg       fakeReg
		files     func(string) bool
		arch      string
		available bool
		version   string
		scope     string
		reason    string
	}{
		{
			name:      "per-machine runtime, no documented pv entry at all",
			reg:       reg(pair(loaderEntry(Machine, machineDir))),
			files:     files(machineX64),
			arch:      "x64",
			available: true, version: "131.0.2903.70", scope: "per-machine",
		},
		{
			name: "per-machine entry too old, per-user runtime used",
			reg: reg(pair(loaderEntry(Machine, `C:\x\86.0.616.0`)),
				pair(loaderEntry(User, userDir))),
			files:     files(userX64),
			arch:      "x64",
			available: true, version: "130.0.2849.80", scope: "per-user",
		},
		{
			name: "stale per-machine entry without version, per-user runtime used",
			reg: reg(pair(loaderEntry(Machine, "")),
				pair(loaderEntry(User, userDir))),
			files:     files(userX64),
			arch:      "x64",
			available: true, version: "130.0.2849.80", scope: "per-user",
		},
		{
			name: "per-machine entry chosen but its client is gone: the loader does not fall back",
			reg: reg(pair(loaderEntry(Machine, machineDir)),
				pair(loaderEntry(User, userDir))),
			files:   files(userX64),
			arch:    "x64",
			version: "131.0.2903.70", scope: "per-machine",
			reason: "has no x64 client library",
		},
		{
			name:      "ARM64 window uses the arm64 client",
			reg:       reg(pair(loaderEntry(Machine, machineDir))),
			files:     files(machineARM),
			arch:      "arm64",
			available: true, version: "131.0.2903.70", scope: "per-machine",
		},
		{
			name:    "ARM64 window with only an x64 client",
			reg:     reg(pair(loaderEntry(Machine, machineDir))),
			files:   files(machineX64),
			arch:    "arm64",
			version: "131.0.2903.70", scope: "per-machine",
			reason: "has no arm64 client library",
		},
		{
			name: "only documented pv entries, nothing for the loader",
			reg: reg(pair(key(Machine, View32, clientsPath, "pv"), "131.0.2903.70"),
				pair(key(User, View32, clientsPath, "pv"), "131.0.2903.70")),
			files:  files(),
			arch:   "x64",
			reason: "no runtime registered for the window's loader",
		},
		{
			name:   "nothing at all",
			reg:    reg(),
			files:  files(),
			arch:   "x64",
			reason: "no runtime registered for the window's loader",
		},
		{
			name:    "unknown architecture",
			reg:     reg(pair(loaderEntry(Machine, machineDir))),
			files:   files(machineX64),
			arch:    "",
			version: "131.0.2903.70", scope: "per-machine",
			reason: "no WebView2 client",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := Probe(tc.reg, tc.files, tc.arch)
			if st.Available != tc.available || st.Version != tc.version || st.Scope != tc.scope {
				t.Fatalf("Probe = available %v, %q %q (reason %q); want %v, %q %q",
					st.Available, st.Version, st.Scope, st.Reason, tc.available, tc.version, tc.scope)
			}
			if !strings.Contains(st.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", st.Reason, tc.reason)
			}
			if st.Available && st.Reason != "" {
				t.Fatalf("available with a reason: %q", st.Reason)
			}
		})
	}
}

// The line tells a false "missing" apart from a real one: the loader's entries
// and the documented pv entries in every view, with the user's folder hidden.
func TestStatusLineIsDiagnosticAndSanitized(t *testing.T) {
	r := reg(pair(loaderEntry(User, userDir)),
		pair(key(Machine, View32, clientsPath, "pv"), "0.0.0.0"),
		pair(key(User, View32, clientsPath, "pv"), "130.0.2849.80"))
	st := Probe(r, files(), "x64")
	var b bytes.Buffer
	if err := st.Write(&b, Sanitizer(map[string]string{
		"%LOCALAPPDATA%": `C:\Users\Jane Doe\AppData\Local`,
		"%USERPROFILE%":  `C:\Users\Jane Doe`,
		"%EMPTY%":        "",
	})); err != nil {
		t.Fatal(err)
	}
	line := b.String()
	for _, want := range []string{
		"WebView2 Runtime not usable: the per-user runtime 130.0.2849.80 has no x64 client library",
		`HKLM=none HKCU=%LOCALAPPDATA%\Microsoft\EdgeWebView\Application\130.0.2849.80`,
		`client %LOCALAPPDATA%\Microsoft\EdgeWebView\Application\130.0.2849.80\EBWebView\x64\EmbeddedBrowserWebView.dll`,
		"HKLM 32-bit view=0.0.0.0, HKLM 64-bit view=none, HKCU 32-bit view=130.0.2849.80.",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("status line lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "Jane") || strings.Count(line, "\n") != 1 {
		t.Errorf("status line is not one sanitized line:\n%s", line)
	}
}

func TestSanitizerMatchesWholeFolders(t *testing.T) {
	s := Sanitizer(map[string]string{"%USERPROFILE%": `C:\Users\Jane\`, "%LOCALAPPDATA%": `c:\users\jane\AppData\Local`})
	for in, want := range map[string]string{
		`C:\Users\Jane\AppData\Local\x`: `%LOCALAPPDATA%\x`,
		`C:\Users\Jane\Desktop`:         `%USERPROFILE%\Desktop`,
		`C:\Users\Janet\x`:              `C:\Users\Janet\x`,
		`C:\Users\Jane`:                 `%USERPROFILE%`,
		`D:\elsewhere`:                  `D:\elsewhere`,
	} {
		if got := s(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseVersionLikeWebview(t *testing.T) {
	for in, want := range map[string][4]uint64{
		"131.0.2903.70":   {131, 0, 2903, 70},
		"1.2":             {1, 2, 0, 0},
		"":                {0, 0, 0, 0},
		"x.1.y2.3":        {0, 1, 0, 3},
		"1.2.3.4.5":       {1, 2, 3, 4},
		"1.2.1150abc.0":   {1, 2, 1150, 0},
		"1.2.99999999999": {1, 2, 0, 0},
		"-1.2.-3":         {0, 2, 0, 0},
	} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %v, want %v", in, got, want)
		}
	}
	if lastPathComponent(`C:\a\b`) != "b" || lastPathComponent("noslash") != "" {
		t.Error("lastPathComponent differs from webview.h")
	}
	if Arch("amd64") != "x64" || Arch("arm64") != "arm64" || Arch("riscv64") != "" {
		t.Error("Arch mapping")
	}
}
