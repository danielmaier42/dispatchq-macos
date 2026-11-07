package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/danielmaier42/claudeq/internal/system"
)

// The Windows dialogs are Windows Forms dialogs shown by PowerShell, the way the
// macOS ones are AppleScript dialogs shown by osascript: the daemon runs in the
// signed-in user's session, so a window it opens appears on their desktop, and
// no GUI code has to live in the daemon itself.
//
// Every value reaches the script as base64 of UTF-8 and the answer comes back
// the same way, so neither a quote in a path nor a console code page can change
// what is asked or what is returned.

// psDialogPreamble builds the owner window that keeps a dialog in front of the
// browser window that asked for it.
const psDialogPreamble = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
function Decode([string]$b) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)) }
function Emit([string]$s) { [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($s))) }
$owner = New-Object System.Windows.Forms.Form -Property @{ TopMost = $true; ShowInTaskbar = $false; Opacity = 0 }
`

// PowerShellFolderChooser returns a FolderChooser that opens the native Windows
// folder dialog. A user cancel is reported as chosen=false, not an error.
func PowerShellFolderChooser(r system.Runner) FolderChooser {
	return func(ctx context.Context, start string) (string, bool, error) {
		script := psDialogPreamble + fmt.Sprintf(`
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = Decode '%s'
$d.ShowNewFolderButton = $true
$start = Decode '%s'
if ($start) { $d.SelectedPath = $start }
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { Emit $d.SelectedPath }
`, b64("Select the task's working directory"), b64(existingDir(start)))
		return runPowerShellDialog(ctx, r, "choose folder", script)
	}
}

// PowerShellSaveFileDialog returns a SaveFileDialog that opens the native
// Windows "Save as" dialog, which itself asks before replacing a file.
func PowerShellSaveFileDialog(r system.Runner) SaveFileDialog {
	return func(ctx context.Context, prompt, defaultName string) (string, bool, error) {
		script := psDialogPreamble + fmt.Sprintf(`
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.Title = Decode '%s'
$d.FileName = Decode '%s'
$d.OverwritePrompt = $true
$d.Filter = 'All files (*.*)|*.*'
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) { Emit $d.FileName }
`, b64(prompt), b64(defaultName))
		return runPowerShellDialog(ctx, r, "choose file name", script)
	}
}

// runPowerShellDialog runs script in an STA PowerShell, which Windows Forms
// dialogs require, and decodes the base64 answer. No output means the user
// cancelled. PowerShell runs as a command that shows a window: started hidden
// like a background command, its dialog would never appear.
func runPowerShellDialog(ctx context.Context, r system.Runner, what, script string) (string, bool, error) {
	out, err := system.ShowingWindows(r).Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA",
		"-EncodedCommand", encodePowerShell(script))
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", false, fmt.Errorf("powershell %s: %w (%s)", what, err, text)
	}
	if text == "" {
		return "", false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return "", false, fmt.Errorf("powershell %s: unreadable answer %q", what, text)
	}
	return string(raw), true, nil
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// encodePowerShell is the form -EncodedCommand takes: base64 of UTF-16LE.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
