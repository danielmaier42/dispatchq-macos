package executor

import (
	"strings"
	"testing"
)

func TestWSLPromptUsesLinuxPathsAndWindowsOwnedData(t *testing.T) {
	for _, daemonOS := range []string{"darwin", "windows"} {
		t.Run(daemonOS, func(t *testing.T) {
			withHostGOOS(t, daemonOS)
			got := systemPromptFor(wslStubHost{}, "")
			for _, wrong := range []string{"macOS", "~/Library", "mdfind", "$env:", "Get-Command"} {
				if strings.Contains(got, wrong) {
					t.Errorf("WSL guidance retains foreign host instruction %q", wrong)
				}
			}
			for _, want := range []string{"command -v", "wslpath", "Windows daemon owns", "DISPATCHQ_HOME", `"${DISPATCHQ_BIN:-dispatchq}"`, "Windows notifications", "Windows interop"} {
				if !strings.Contains(got, want) {
					t.Errorf("WSL guidance lacks %q", want)
				}
			}
		})
	}
}
