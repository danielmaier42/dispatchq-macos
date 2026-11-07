//go:build windows

package executor

import "testing"

// utf8WriteScript writes a non-ASCII literal to out.txt in the working
// directory as BOM-less UTF-8. Without the script file's BOM, Windows
// PowerShell 5.1 decodes the literal in the ANSI code page and writes mojibake.
const utf8WriteScript = "[IO.File]::WriteAllText((Join-Path (Get-Location) 'out.txt'), 'Grüße ✓', (New-Object Text.UTF8Encoding($false)))"

func TestScriptFileStartsWithBOM(t *testing.T) {
	if got := string(scriptFileBytes("Write-Output 'é'")); got != "\xEF\xBB\xBFWrite-Output 'é'" {
		t.Fatalf("scriptFileBytes = %q, want the source behind a UTF-8 BOM", got)
	}
}
