//go:build unix

package wslhelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnreadableProgramCannotBypassLinuxEligibility(t *testing.T) {
	f := newPolicyFixture(t)
	p := writeExec(t, filepath.Join(f.linDir, "renamed-provider"), "MZ Windows image")
	if err := os.Chmod(p, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Error(err)
		}
	})
	if file, err := os.Open(p); err == nil {
		_ = file.Close()
		t.Skip("this user can read execute-only files; permission fixture is not reachable")
	}
	if err := executable(p); err != nil {
		t.Fatalf("fixture must remain executable: %v", err)
	}
	h := &Helper{}
	for _, name := range []string{p, "renamed-provider"} {
		t.Run(name, func(t *testing.T) {
			resolved, err := h.lookPath(name, []string{"PATH=" + f.linDir}, f.mnt)
			if err == nil {
				t.Fatalf("uninspectable executable was accepted as %q", resolved)
			}
			if !strings.Contains(err.Error(), "cannot be inspected") {
				t.Fatalf("inspection error was lost: %v", err)
			}
		})
	}
}
