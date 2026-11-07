//go:build unix

package wslhelper

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// policyFixture lays out a Linux bin folder and a Windows drive mount, each
// with programs that would run if the helper chose them.
type policyFixture struct {
	mnt, winDir, linDir string
}

func newPolicyFixture(t *testing.T) policyFixture {
	t.Helper()
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	f := policyFixture{
		mnt:    filepath.Join(dir, "mnt") + "/",
		linDir: filepath.Join(dir, "home", "ünï code", "bin"),
	}
	f.winDir = filepath.Join(f.mnt, "c", "Users", "ünï x", "AppData", "npm")
	for _, d := range []string{f.winDir, f.linDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func writeExec(t *testing.T, p, content string) string {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	return p
}

func symlink(t *testing.T, target, link string) string {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link
}

// An explicitly configured binary gets the same Linux-program policy as a
// name found on PATH: a mounted-drive program, a Windows extension, a link
// to either and a Windows image under a Linux name are all refused, while
// Linux scripts and links to them still resolve.
func TestLookPathAppliesTheLinuxPolicyToExplicitPaths(t *testing.T) {
	f := newPolicyFixture(t)
	const script = "#!/bin/sh\necho linux\n"
	winExe := writeExec(t, filepath.Join(f.winDir, "codex.exe"), "MZ\x90\x00windows")
	winBare := writeExec(t, filepath.Join(f.winDir, "codex"), script)
	linCmd := writeExec(t, filepath.Join(f.linDir, "codex.cmd"), script)
	linBat := writeExec(t, filepath.Join(f.linDir, "codex.BAT"), script)
	renamedPE := writeExec(t, filepath.Join(f.linDir, "codex-renamed"), "MZ\x90\x00windows")
	linkToWin := symlink(t, winExe, filepath.Join(f.linDir, "codex-link"))
	linkToMount := symlink(t, winBare, filepath.Join(f.linDir, "codex-mount-link"))
	linux := writeExec(t, filepath.Join(f.linDir, "codex"), script)
	linuxWrapper := symlink(t, linux, filepath.Join(f.linDir, "codex-wrapper"))

	h := &Helper{}
	for _, p := range []string{winExe, winBare, linCmd, linBat, renamedPE, linkToWin, linkToMount} {
		got, err := h.lookPath(p, nil, f.mnt)
		if err == nil {
			t.Errorf("lookPath(%q) = %q, want a Windows-program refusal", p, got)
			continue
		}
		if !strings.Contains(err.Error(), "Windows program") || !strings.Contains(err.Error(), "Linux") {
			t.Errorf("lookPath(%q) error %q does not explain the Linux-provider policy", p, err)
		}
		if strings.Contains(err.Error(), "not found") {
			t.Errorf("lookPath(%q) error %q claims the file is missing", p, err)
		}
	}
	for _, p := range []string{linux, linuxWrapper} {
		if got, err := h.lookPath(p, nil, f.mnt); err != nil || got != p {
			t.Errorf("lookPath(%q) = %q, %v; want the Linux program itself", p, got, err)
		}
	}
	if _, err := h.lookPath(filepath.Join(f.linDir, "missing"), nil, f.mnt); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing explicit path: err = %v, want not-exist", err)
	}
}

// PATH discovery resolves links too: a Linux PATH entry that is only a link
// to a Windows program is skipped in favour of a later Linux candidate, and
// the not-found message names what was skipped.
func TestLookPathSkipsLinksAndImagesOnPATH(t *testing.T) {
	f := newPolicyFixture(t)
	early := filepath.Join(filepath.Dir(f.linDir), "early")
	if err := os.MkdirAll(early, 0o755); err != nil {
		t.Fatal(err)
	}
	winExe := writeExec(t, filepath.Join(f.winDir, "codex.exe"), "MZ\x90\x00windows")
	symlink(t, winExe, filepath.Join(early, "codex"))
	writeExec(t, filepath.Join(early, "claude"), "MZ\x90\x00renamed windows image")
	linux := writeExec(t, filepath.Join(f.linDir, "codex"), "#!/bin/sh\n")
	linuxClaude := writeExec(t, filepath.Join(f.linDir, "claude"), "\x7fELF fake linux image")

	h := &Helper{}
	env := []string{"PATH=" + early + ":" + f.linDir}
	if got, err := h.lookPath("codex", env, f.mnt); err != nil || got != linux {
		t.Errorf("lookPath(codex) = %q, %v; want %q", got, err, linux)
	}
	if got, err := h.lookPath("claude", env, f.mnt); err != nil || got != linuxClaude {
		t.Errorf("lookPath(claude) = %q, %v; want %q", got, err, linuxClaude)
	}
	_, err := h.lookPath("codex", []string{"PATH=" + early}, f.mnt)
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), filepath.Join(early, "codex")) {
		t.Errorf("only a Windows link on PATH: err = %v, want not-found naming the skipped link", err)
	}
}

// The real run operation refuses an explicit Windows provider before it
// starts anything, while an explicit Linux program in the same folder runs.
func TestRunRefusesAnExplicitWindowsProvider(t *testing.T) {
	f := newPolicyFixture(t)
	marker := filepath.Join(f.linDir, "ran")
	body := "#!/bin/sh\necho started > '" + marker + "'\n"
	winExe := writeExec(t, filepath.Join(f.linDir, "codex.exe"), body)
	link := symlink(t, winExe, filepath.Join(f.linDir, "codex"))
	for _, p := range []string{winExe, link} {
		s := startHelper(t, wslproto.OpRun, wslproto.Header{Argv: []string{p, "it's", "ünï"}})
		s.frame(wslproto.FrameEnd, nil)
		if code := s.wait(10 * time.Second); code != ExitHelperFailed {
			t.Fatalf("run %s: exit = %d, want %d; stderr %s", p, code, ExitHelperFailed, s.errOut)
		}
		if !strings.Contains(s.errOut.String(), "Windows program") {
			t.Errorf("run %s: stderr %q lacks the policy reason", p, s.errOut)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("run %s started the Windows provider", p)
		}
	}
	linux := writeExec(t, filepath.Join(f.linDir, "codex-linux"), body)
	s := startHelper(t, wslproto.OpRun, wslproto.Header{Argv: []string{linux}})
	s.frame(wslproto.FrameEnd, nil)
	if code := s.wait(10 * time.Second); code != 0 {
		t.Fatalf("Linux run: exit = %d; stderr %s", code, s.errOut)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the Linux provider did not run: %v", err)
	}
}
