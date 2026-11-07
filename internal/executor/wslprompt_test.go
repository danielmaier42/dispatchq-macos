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
	for _, custom := range []string{"", "Be brief."} {
		if got := systemPromptFor(provider.NativeHost{}, custom); got != systemPrompt(custom) {
			t.Errorf("native prompt changed for custom %q", custom)
		}
		got := systemPromptFor(wslStubHost{}, custom)
		if !strings.Contains(got, wslSystemPrompt) || !strings.HasPrefix(got, builtinSystemPrompt) || !strings.HasSuffix(got, custom) {
			t.Errorf("WSL prompt for custom %q is wrong", custom)
		}
	}
}
