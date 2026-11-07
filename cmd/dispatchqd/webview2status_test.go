package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
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
