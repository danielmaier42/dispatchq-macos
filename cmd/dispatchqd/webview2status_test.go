package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/danielmaier42/claudeq/internal/webview2"
)

// webview2-status exits 0 when the window will find the runtime, 10 when it
// will not, and 1 when the check itself cannot run, so the installer never
// mistakes a failed check for a missing runtime or the other way round.
func TestWebView2StatusExitCodes(t *testing.T) {
	report := func(ok bool, err error) platform {
		return platform{webView2Status: func(out io.Writer) (bool, error) {
			_, _ = io.WriteString(out, "status line\n")
			return ok, err
		}}
	}
	for _, tc := range []struct {
		name string
		p    platform
		args []string
		want int
	}{
		{"available", report(true, nil), nil, 0},
		{"missing", report(false, nil), nil, exitWebView2Missing},
		{"write failed", report(true, errors.New("pipe closed")), nil, 1},
		{"unsupported", platform{}, nil, 1},
		{"extra argument", report(true, nil), []string{"x"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := cmdWebView2Status(tc.p, tc.args, &out)
			got := 0
			if err != nil {
				got = exitCode(err)
			}
			if got != tc.want {
				t.Fatalf("exit code = %d (err %v), want %d", got, err, tc.want)
			}
			if tc.p.webView2Status != nil && len(tc.args) == 0 && out.String() != "status line\n" {
				t.Fatalf("output = %q", out.String())
			}
		})
	}
	if exitWebView2Missing == 1 || exitWebView2Missing == 0 {
		t.Fatal("the missing-runtime exit code must differ from success and from a failed check")
	}
}

// webview2-load-client exits with webview2.ExitClientRefused and the plain
// reason when the library cannot be used, so webview2-status can tell a
// refusal from a check that failed (exit 1) or never finished.
func TestWebView2LoadClientExitCodes(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "EmbeddedBrowserWebView.dll")
	load := func(err error) platform {
		return platform{loadWebView2Client: func(string) error { return err }}
	}
	for _, tc := range []struct {
		name string
		p    platform
		args []string
		want int
	}{
		{"loads", load(nil), []string{abs}, 0},
		{"refused", load(errors.New("the system cannot load it: Bad Image")), []string{abs}, webview2.ExitClientRefused},
		{"relative path", load(nil), []string{"x.dll"}, 1},
		{"no path", load(nil), nil, 1},
		{"unsupported", platform{}, []string{abs}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cmdWebView2LoadClient(tc.p, tc.args)
			got := 0
			if err != nil {
				got = exitCode(err)
			}
			if got != tc.want {
				t.Fatalf("exit code = %d (err %v), want %d", got, err, tc.want)
			}
			if tc.want == webview2.ExitClientRefused && err.Error() != "the system cannot load it: Bad Image" {
				t.Fatalf("reason = %q", err)
			}
		})
	}
	if webview2.ExitClientRefused == 1 || webview2.ExitClientRefused == exitWebView2Missing {
		t.Fatal("the refusal exit code must differ from a failed command")
	}
}
