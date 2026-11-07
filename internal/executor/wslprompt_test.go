package executor

import (
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/provider"
)

type wslStubHost struct{ provider.NativeHost }

func (wslStubHost) Execution() provider.Execution {
	return provider.Execution{Type: provider.ExecWSL, Distro: "Ubuntu"}
}

func TestSystemPromptNamesWSLOnlyForWSLRuns(t *testing.T) {
	withHostGOOS(t, "darwin")
	for _, custom := range []string{"", "Be brief."} {
		if got := systemPromptFor(provider.NativeHost{}, custom); got != systemPrompt(custom) {
			t.Errorf("native prompt changed for custom %q", custom)
		}
		got := systemPromptFor(wslStubHost{}, custom)
		if !strings.Contains(got, wslSystemPrompt) || !strings.HasPrefix(got, wslBuiltinSystemPrompt) || !strings.HasSuffix(got, custom) {
			t.Errorf("WSL prompt for custom %q is wrong", custom)
		}
	}
}

// withHostGOOS runs the test as if the daemon ran on goos.
func withHostGOOS(t *testing.T, goos string) {
	t.Helper()
	old := hostGOOS
	hostGOOS = goos
	t.Cleanup(func() { hostGOOS = old })
}

func TestPowerShellRewritesMatchThePrompt(t *testing.T) {
	for i := 0; i < len(powerShellRewrites); i += 2 {
		if !strings.Contains(builtinSystemPrompt, powerShellRewrites[i]) {
			t.Errorf("built-in prompt no longer contains %q; Windows runs would keep the macOS text", powerShellRewrites[i])
		}
	}
}

func TestWSLRewritesMatchThePrompt(t *testing.T) {
	for i := 0; i < len(wslRewrites); i += 2 {
		if !strings.Contains(builtinSystemPrompt, wslRewrites[i]) {
			t.Errorf("built-in prompt no longer contains WSL rewrite %q", wslRewrites[i])
		}
	}
}

// A native Windows run used to get the macOS prompt: Bash parameter expansion
// for the CLI, macOS data and notification paths, mdfind advice.
func TestNativeWindowsPromptIsPowerShell(t *testing.T) {
	withHostGOOS(t, "windows")
	got := systemPromptFor(provider.NativeHost{}, "Be brief.")
	for _, posix := range []string{"${DISPATCHQ_BIN", "~/Library", "macOS Notification Center", "mdfind", "command -v", "inside a Linux system"} {
		if strings.Contains(got, posix) {
			t.Errorf("Windows prompt still contains %q", posix)
		}
	}
	const call = `& $(if ($env:DISPATCHQ_BIN) { $env:DISPATCHQ_BIN } else { 'dispatchq' })`
	if want := strings.Count(builtinSystemPrompt, `"${DISPATCHQ_BIN:-dispatchq}"`); want == 0 || strings.Count(got, call) != want {
		t.Errorf("Windows prompt has %d PowerShell CLI calls, want %d", strings.Count(got, call), want)
	}
	for _, want := range []string{`$env:APPDATA\dispatchq`, "Windows notifications", "PowerShell, never through Bash", "a PowerShell script on this system"} {
		if !strings.Contains(got, want) {
			t.Errorf("Windows prompt lacks %q", want)
		}
	}
	if !strings.HasSuffix(got, customSystemPromptIntro+"Be brief.") {
		t.Error("custom prompt must stay last")
	}
}

// A WSL run on Windows is a Linux shell: it keeps the POSIX prompt and gets
// the WSL notes, never the PowerShell one.
func TestWSLRunOnWindowsKeepsThePOSIXPrompt(t *testing.T) {
	withHostGOOS(t, "windows")
	if got := systemPromptFor(wslStubHost{}, ""); got != wslBuiltinSystemPrompt {
		t.Error("WSL prompt on a Windows host is not the POSIX prompt plus the WSL notes")
	}
}

func TestMacPromptIsUnchanged(t *testing.T) {
	withHostGOOS(t, "darwin")
	if got := systemPromptFor(provider.NativeHost{}, ""); got != builtinSystemPrompt {
		t.Error("native macOS prompt changed")
	}
}
