package execenv

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/executor"
	"github.com/danielmaier42/claudeq/internal/provider"
)

var (
	native = provider.Execution{}
	wslU   = provider.Execution{Type: provider.ExecWSL, Distro: "Ubuntu"}
)

func TestCanReachAndToLinux(t *testing.T) {
	cases := []struct {
		stored        string
		native, wsl   bool
		linux         string
		nativeOnMacOK bool
	}{
		{`C:\repo`, true, true, "/mnt/c/repo", true},
		{`c:/Repo/x`, true, true, "/mnt/c/Repo/x", true},
		{`D:\`, true, true, "/mnt/d", true},
		{`\\wsl.localhost\Ubuntu\home\dm\repo`, true, true, "/home/dm/repo", true},
		{`\\wsl$\ubuntu\home\dm`, true, true, "/home/dm", true},
		{`\\wsl.localhost\Debian\home\dm`, true, false, "", true},
		{`\\server\share\x`, true, false, "", true},
		{`/home/dm/repo`, false, true, "/home/dm/repo", true},
		{`relative\dir`, true, false, "", true},
		{``, true, true, "", true},
	}
	for _, c := range cases {
		if err := CanReach(native, c.stored, "windows"); (err == nil) != c.native {
			t.Errorf("native CanReach(%q) = %v", c.stored, err)
		}
		if err := CanReach(native, c.stored, "darwin"); (err == nil) != c.nativeOnMacOK {
			t.Errorf("macOS CanReach(%q) = %v", c.stored, err)
		}
		err := CanReach(wslU, c.stored, "windows")
		if (err == nil) != c.wsl {
			t.Errorf("wsl CanReach(%q) = %v", c.stored, err)
		}
		if err != nil && !errors.Is(err, ErrUnreachable) {
			t.Errorf("wsl CanReach(%q) = %v, not ErrUnreachable", c.stored, err)
		}
		if got, err := ToLinux(wslU, c.stored, "/mnt/"); c.wsl && (err != nil || got != c.linux) {
			t.Errorf("ToLinux(%q) = %q, %v; want %q", c.stored, got, err, c.linux)
		}
	}
	if _, err := ToLinux(wslU, `C:\repo`, ""); err == nil || !strings.Contains(err.Error(), "automount") {
		t.Errorf("drive path without a mount = %v", err)
	}
	if got, _ := ToLinux(wslU, `C:\repo`, "/windir/"); got != "/windir/c/repo" {
		t.Errorf("custom mount root: %q", got)
	}
	if err := CanReach(provider.Execution{Type: "docker"}, `C:\x`, "windows"); err == nil {
		t.Error("an unknown environment reached a folder")
	}
}

func TestFromLinux(t *testing.T) {
	for in, want := range map[string]string{
		"/mnt/c/Users/dm/out.md": `C:\Users\dm\out.md`,
		"/mnt/d":                 `D:\`,
		"/home/dm/repo/a.md":     `\\wsl.localhost\Ubuntu\home\dm\repo\a.md`,
		"/mnt/wsl/x":             `\\wsl.localhost\Ubuntu\mnt\wsl\x`,
	} {
		if got := FromLinux(in, "Ubuntu", "/mnt/"); got != want {
			t.Errorf("FromLinux(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FromLinux("/mnt/c/x", "Ubuntu", ""); got != `\\wsl.localhost\Ubuntu\mnt\c\x` {
		t.Errorf("no mount root: %q", got)
	}
}

func TestDecodeWSL(t *testing.T) {
	utf16 := []byte{'U', 0, 'b', 0, '\r', 0, '\n', 0, 'D', 0}
	for name, in := range map[string][]byte{
		"utf-8":       []byte("Ub\r\nD"),
		"utf-16":      utf16,
		"utf-16 bom":  append([]byte{0xff, 0xfe}, utf16...),
		"utf-8 bom":   append([]byte{0xef, 0xbb, 0xbf}, "Ub\r\nD"...),
		"nul-trailed": append([]byte("Ub\r\nD"), 0),
	} {
		if got := strings.Join(lines(decodeWSL(in)), ","); got != "Ub,D" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func elfHeader(machine uint16) []byte {
	h := make([]byte, 64)
	copy(h, "\x7fELF")
	h[4], h[5] = 2, 1
	binary.LittleEndian.PutUint16(h[16:], 2)
	binary.LittleEndian.PutUint16(h[18:], machine)
	return h
}

func TestCheckELF(t *testing.T) {
	dir := t.TempDir()
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	amd, arm := put("amd", elfHeader(0x3e)), put("arm", elfHeader(0xb7))
	pe := put("pe", append([]byte("MZ"), make([]byte, 62)...))
	for _, c := range []struct {
		path, arch string
		ok         bool
	}{
		{amd, "amd64", true}, {arm, "arm64", true}, {amd, "arm64", false}, {arm, "amd64", false},
		{pe, "amd64", false}, {amd, "riscv64", false}, {filepath.Join(dir, "missing"), "amd64", false},
	} {
		if err := CheckELF(c.path, c.arch); (err == nil) != c.ok {
			t.Errorf("CheckELF(%s, %s) = %v", filepath.Base(c.path), c.arch, err)
		}
	}
	for m, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64"} {
		if got, ok := linuxArch(m); !ok || got != want {
			t.Errorf("linuxArch(%s) = %s", m, got)
		}
	}
	if _, ok := linuxArch("armv7l"); ok {
		t.Error("32-bit arm has no helper")
	}
}

func TestRunVarsShareOnlyDispatchQNames(t *testing.T) {
	env, share := runVars([]string{
		executor.EnvQueueBin + `=C:\Program Files\DispatchQ\dispatchq.exe`, `DISPATCHQ_HOME=C:\Data`,
	}, "/mnt/", wslU)
	got := strings.Join(env, "\n")
	for _, want := range []string{
		"DISPATCHQ_BIN=/mnt/c/Program Files/DispatchQ/dispatchq.exe", "CLAUDEQ_BIN=/mnt/c/Program Files/DispatchQ/dispatchq.exe",
		`DISPATCHQ_HOME=C:\Data`, `CLAUDEQ_HOME=C:\Data`, "DISPATCHQ_WSL_MNT=/mnt/",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %q:\n%s", want, got)
		}
	}
	if strings.Join(share, ",") != "DISPATCHQ_BIN,DISPATCHQ_HOME,CLAUDEQ_BIN,CLAUDEQ_HOME,DISPATCHQ_WSL_MNT,WSL_DISTRO_NAME" {
		t.Errorf("share = %v", share)
	}
}

func TestBinaryNamesMatchTheAdapters(t *testing.T) {
	if queueBinVar != executor.EnvQueueBin {
		t.Errorf("queueBinVar %q, executor %q", queueBinVar, executor.EnvQueueBin)
	}
	for _, k := range []provider.Kind{provider.KindClaudeCode, provider.KindCodex, provider.KindOpencode} {
		if binaryNames[k] == "" {
			t.Errorf("no binary name for %s", k)
		}
	}
}
