//go:build !windows

package executor

import "testing"

// utf8WriteScript writes a non-ASCII literal to out.txt in the working directory.
const utf8WriteScript = "printf '%s\\n' 'Grüße ✓' > out.txt"

func TestScriptFileKeepsTheShebangFirst(t *testing.T) {
	if got := string(scriptFileBytes("#!/bin/sh\necho é")); got != "#!/bin/sh\necho é" {
		t.Fatalf("scriptFileBytes = %q, want the source unchanged", got)
	}
}
