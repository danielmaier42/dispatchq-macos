//go:build unix

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// Readiness, detection and the actual start all go through the helper's
// Linux-program policy: an explicitly configured Windows CLI on the drive
// mount, or a Linux link to it, is not ready and never starts, while a
// Linux CLI given by path is ready and runs.
func TestWindowsProviderBinaryIsRefusedEndToEnd(t *testing.T) {
	f := newFixture(t)
	h := f.svc.Host(ubuntu)
	ctx := context.Background()
	marker := filepath.Join(f.tmp, "windows-ran")
	winDir := filepath.Join(f.mount, "c", "Users", "ünï x", "AppData", "Roaming", "npm")
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\necho started > '" + marker + "'\n"
	winExe := filepath.Join(winDir, "codex.exe")
	write(t, winExe, body, 0o755)
	winCmd := filepath.Join(winDir, "codex.cmd")
	write(t, winCmd, body, 0o755)
	link := filepath.Join(f.tmp, "bin", "codex")
	if err := os.Symlink(winExe, link); err != nil {
		t.Fatal(err)
	}
	linux := filepath.Join(f.tmp, "bin", "claude")

	for _, p := range []string{winExe, winCmd, link} {
		err := h.Executable(ctx, p)
		if err == nil || !strings.Contains(err.Error(), "Windows program") {
			t.Errorf("Executable(%q) = %v, want a Windows-program refusal", p, err)
		}
		out, err := h.Probe(ctx, provider.Command{Path: p, Args: []string{"--version"}})
		if err == nil {
			t.Errorf("Probe(%q) succeeded: %q", p, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("Probe(%q) started the Windows program", p)
		}
	}
	if err := h.Executable(ctx, linux); err != nil {
		t.Errorf("Executable(Linux path) = %v", err)
	}
	if out, err := h.Probe(ctx, provider.Command{Path: linux, Args: []string{"--version"}}); err != nil || !strings.Contains(string(out), "claude 2.1.0") {
		t.Errorf("Probe(Linux path) = %q, %v", out, err)
	}
	if err := h.Executable(ctx, filepath.Join(f.tmp, "bin", "nope")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Executable(missing) = %v", err)
	}
	if err := h.Executable(ctx, filepath.Join(f.tmp, "bin")); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("Executable(dir) = %v", err)
	}
	d, err := f.svc.Detect(ctx, ubuntu, provider.KindCodex)
	if err != nil {
		t.Fatal(err)
	}
	if d.Binary != "" || len(d.Warnings) == 0 {
		t.Errorf("Detect(codex linked to Windows) = %+v, want no binary and a warning", d)
	}
}
