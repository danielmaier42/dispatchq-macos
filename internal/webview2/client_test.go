package webview2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture copies a testdata file (see testdata/README.md) or the first n
// bytes of it (n >= 0) into a temporary folder.
func fixture(t *testing.T, name string, n int) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if n >= 0 {
		b = b[:n]
	}
	p := filepath.Join(t.TempDir(), "EmbeddedBrowserWebView.dll")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// refuse is a native load that fails like LoadLibraryW does for a library of
// the wrong architecture or GetProcAddress for a missing export.
func refuse(msg string) func(string) error {
	return func(string) error { return errors.New(msg) }
}

func TestCheckClientWithRealLibraries(t *testing.T) {
	accept := func(string) error { return nil }
	for _, tc := range []struct {
		name    string
		file    string
		n       int // -1: whole file
		arch    string
		load    func(string) error
		want    []string // nil: usable
		noLoad  bool     // refused from the headers, never loaded
		missing bool
	}{
		{name: "clean x64 client", file: "x64-good.dll", n: -1, arch: "x64", load: accept},
		{name: "clean ARM64 client", file: "arm64-good.dll", n: -1, arch: "arm64", load: accept},
		{name: "empty file", file: "x64-good.dll", n: 0, arch: "x64", load: accept,
			want: []string{"the file is empty"}, noLoad: true},
		{name: "truncated library", file: "x64-good.dll", n: 200, arch: "x64", load: accept,
			want: []string{"not a valid Windows library"}, noLoad: true},
		{name: "headers only, sections cut off", file: "x64-good.dll", n: 1100, arch: "x64", load: accept,
			want: []string{"export table is damaged"}, noLoad: true},
		{name: "program instead of library", file: "x64-exe.exe", n: -1, arch: "x64", load: accept,
			want: []string{"not a library (DLL)"}, noLoad: true},
		{name: "missing file", missing: true, arch: "x64", load: accept,
			want: []string{"does not exist"}, noLoad: true},
		{name: "wrong architecture", file: "arm64-good.dll", n: -1, arch: "x64",
			load: refuse("the system cannot load it: %1 is not a valid Win32 application."),
			want: []string{"not a valid Win32 application", "built for ARM64, the window is x64"}},
		{name: "missing export", file: "x64-noexport.dll", n: -1, arch: "x64",
			load: refuse("it does not provide CreateWebViewEnvironmentWithOptionsInternal: The specified procedure could not be found."),
			want: []string{"does not provide", "it does not export CreateWebViewEnvironmentWithOptionsInternal"}},
		{name: "load check did not finish", file: "x64-good.dll", n: -1, arch: "x64",
			load: refuse("loading it did not finish within 20s"),
			want: []string{"did not finish within 20s"}},
		// Windows decides: a hybrid image may load although its main header
		// names another machine.
		{name: "header mismatch but Windows loads it", file: "arm64-good.dll", n: -1, arch: "x64", load: accept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "absent.dll")
			if !tc.missing {
				path = fixture(t, tc.file, tc.n)
			}
			loaded := false
			err := CheckClient(path, tc.arch, func(p string) error { loaded = true; return tc.load(p) })
			if tc.want == nil {
				if err != nil {
					t.Fatalf("CheckClient = %v, want usable", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckClient accepted it, want %q", tc.want)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			if tc.noLoad && loaded {
				t.Error("a file that cannot be a library was loaded")
			}
		})
	}
}

func TestInspectClientReadsHeaders(t *testing.T) {
	in, err := InspectClient(fixture(t, "x64-noexport.dll", -1), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if in.Machine != 0x8664 || in.HasExport {
		t.Fatalf("inspection = %+v", in)
	}
	want := "it is built for x64, the window is ARM64; it does not export " + RequiredExport
	if got := in.Mismatch(); got != want {
		t.Fatalf("Mismatch = %q, want %q", got, want)
	}
	if _, err := InspectClient(t.TempDir(), "x64"); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Fatalf("folder: %v", err)
	}
}

// A per-machine runtime whose client is broken is reported as such: the
// loader does not go on to a per-user runtime, and the check runs once.
func TestProbeBrokenMachineClientDoesNotFallBack(t *testing.T) {
	r := reg(pair(loaderEntry(Machine, machineDir)), pair(loaderEntry(User, userDir)))
	calls := 0
	st := Probe(r, func(p string) error {
		calls++
		if strings.HasPrefix(p, machineDir) {
			return errors.New("the file is empty")
		}
		return nil
	}, "x64")
	if st.Available || st.Scope != "per-machine" || calls != 1 {
		t.Fatalf("available %v, scope %q, checks %d", st.Available, st.Scope, calls)
	}
	if !strings.Contains(st.Reason, "per-machine runtime 131.0.2903.70 has no usable x64 client library: the file is empty") {
		t.Fatalf("reason %q", st.Reason)
	}
}

// The load-check process is this test binary in helper mode.
func TestLoadCheckHelper(t *testing.T) {
	mode := os.Getenv("WEBVIEW2_LOADCHECK_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	switch mode {
	case "ok":
		os.Exit(0)
	case "refused":
		fmt.Fprintln(os.Stderr, "the system cannot load it: Bad Image")
		os.Exit(ExitClientRefused)
	case "refused-silent":
		os.Exit(ExitClientRefused)
	case "crash":
		fmt.Println("panic: boom")
		os.Exit(2)
	case "flood":
		fmt.Print(strings.Repeat("x", 100000))
		os.Exit(ExitClientRefused)
	case "hang":
		time.Sleep(time.Minute)
	}
	os.Exit(99)
}

func TestRunLoadCheck(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string, timeout time.Duration) error {
		t.Setenv("WEBVIEW2_LOADCHECK_HELPER", mode)
		return RunLoadCheck(context.Background(), timeout, self, "-test.run=^TestLoadCheckHelper$")
	}
	if err := run("ok", 30*time.Second); err != nil {
		t.Fatalf("ok: %v", err)
	}
	if err := run("refused", 30*time.Second); err == nil || err.Error() != "the system cannot load it: Bad Image" {
		t.Fatalf("refused: %v", err)
	}
	if err := run("refused-silent", 30*time.Second); err == nil || !strings.Contains(err.Error(), "refused to load") {
		t.Fatalf("refused without output: %v", err)
	}
	if err := run("crash", 30*time.Second); err == nil || !strings.Contains(err.Error(), "the load check failed") || !strings.Contains(err.Error(), "panic: boom") {
		t.Fatalf("crash: %v", err)
	}
	if err := run("flood", 30*time.Second); err == nil || len(err.Error()) > 2048 {
		t.Fatalf("flood: %d bytes", len(fmt.Sprint(err)))
	}
	start := time.Now()
	err = run("hang", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish within 300ms") {
		t.Fatalf("hang: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("a hanging load check held the caller for %s", d)
	}
	if err := RunLoadCheck(context.Background(), time.Second, filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "the load check failed") {
		t.Fatalf("missing program: %v", err)
	}
}
