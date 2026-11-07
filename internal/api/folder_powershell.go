package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

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
//
// The runner returns stdout and stderr in one stream, and PowerShell writes
// more than the answer into it: started with -EncodedCommand and redirected
// output, it serializes progress records ("Preparing modules for first use")
// and errors to stderr as CLIXML, interleaved with stdout. So the answer is
// framed (psAnswerOpen ... psAnswerClose, or psCancelled) and only the frame is
// read; everything else is diagnostics. A frame counts only from a run that
// ended on its own and without error, and only when it is the one frame.
const (
	psAnswerOpen  = "<<DQ-ANSWER:"
	psAnswerClose = ">>"
	psCancelled   = "<<DQ-CANCELLED>>"
)

// psDialogProtocol silences progress output, which the dialog does not need,
// and defines how values travel in (Decode) and the answer travels out (Emit,
// EmitCancelled).
const psDialogProtocol = `
$ProgressPreference = 'SilentlyContinue'
$ErrorActionPreference = 'Stop'
function Decode([string]$b) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)) }
function Emit([string]$s) {
	[Console]::Out.Write('` + psAnswerOpen + `' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($s)) + '` + psAnswerClose + `')
	[Console]::Out.Flush()
}
function EmitCancelled {
	[Console]::Out.Write('` + psCancelled + `')
	[Console]::Out.Flush()
}
`

// psDialogOwner gives the dialog an owner window meant to bring it in front of
// the browser window that asked for it. Before, the owner was never shown, so
// its TopMost could not take effect as a visible window, and users saw the
// dialog open behind DispatchQ. Now the owner is shown as a topmost, invisible
// 1x1 window on the screen under the mouse (where the user just clicked
// "Choose") and activated before the dialog opens, so the dialog it owns should
// open above it. Whether Windows' foreground rules let it come to the front and
// take keyboard focus is decided at run time and still needs acceptance on
// Windows. ShowOwned shows a dialog owned by it and closes the owner afterwards.
const psDialogOwner = `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()
$owner = New-Object System.Windows.Forms.Form -Property @{
	TopMost = $true; ShowInTaskbar = $false; Opacity = 0
	FormBorderStyle = [System.Windows.Forms.FormBorderStyle]::None
	StartPosition = [System.Windows.Forms.FormStartPosition]::Manual
	Size = New-Object System.Drawing.Size 1, 1
}
$area = [System.Windows.Forms.Screen]::FromPoint([System.Windows.Forms.Cursor]::Position).WorkingArea
$owner.Location = New-Object System.Drawing.Point ([int]($area.X + $area.Width / 2)), ([int]($area.Y + $area.Height / 3))
$owner.Show()
$owner.Activate()
[System.Windows.Forms.Application]::DoEvents()
function ShowOwned($d) {
	try { $d.ShowDialog($owner) } finally { $owner.Close(); $owner.Dispose() }
}
`

// psDialogPreamble is what every dialog script starts with.
const psDialogPreamble = psDialogProtocol + psDialogOwner

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
if ((ShowOwned $d) -eq [System.Windows.Forms.DialogResult]::OK) { Emit $d.SelectedPath } else { EmitCancelled }
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
if ((ShowOwned $d) -eq [System.Windows.Forms.DialogResult]::OK) { Emit $d.FileName } else { EmitCancelled }
`, b64(prompt), b64(defaultName))
		return runPowerShellDialog(ctx, r, "choose file name", script)
	}
}

// runPowerShellDialog runs script in an STA PowerShell, which Windows Forms
// dialogs require, and reads the framed answer out of whatever else PowerShell
// wrote. A run that hit the time limit or failed is an error even when a frame
// is in its output, and only the cancel frame is a cancel: a script that
// failed or never asked is not mistaken for the user choosing or saying no.
// PowerShell runs as a command that shows a window: started hidden like a
// background command, its dialog would never appear. ctx bounds how long the
// dialog may stay open; when it ends, PowerShell and its dialog are killed.
func runPowerShellDialog(ctx context.Context, r system.Runner, what, script string) (string, bool, error) {
	out, runErr := system.ShowingWindows(r).Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA",
		"-EncodedCommand", encodePowerShell(script))
	if ctx.Err() != nil {
		return "", false, fmt.Errorf("powershell %s: no answer in time: %w", what, ctx.Err())
	}
	if runErr != nil {
		return "", false, fmt.Errorf("powershell %s: %w%s", what, runErr, powerShellDiagnostic(string(out)))
	}
	answer, cancelled, err := readPowerShellAnswer(string(out))
	switch {
	case err != nil:
		return "", false, fmt.Errorf("powershell %s: %w%s", what, err, powerShellDiagnostic(string(out)))
	case cancelled:
		return "", false, nil
	default:
		return answer, true, nil
	}
}

var (
	errNoAnswer         = errors.New("no answer")
	errAmbiguousAnswer  = errors.New("more than one answer")
	errUnreadableAnswer = errors.New("unreadable answer")
)

// readPowerShellAnswer finds the one answer or cancel frame in PowerShell's
// combined output. No frame, more than one, or a frame that is torn or not
// base64 of UTF-8 is an error.
func readPowerShellAnswer(out string) (answer string, cancelled bool, err error) {
	cancels, answers := strings.Count(out, psCancelled), strings.Count(out, psAnswerOpen)
	switch {
	case cancels+answers == 0:
		return "", false, errNoAnswer
	case cancels+answers > 1:
		return "", false, errAmbiguousAnswer
	case cancels == 1:
		return "", true, nil
	}
	_, rest, _ := strings.Cut(out, psAnswerOpen)
	frame, _, closed := strings.Cut(rest, psAnswerClose)
	if !closed {
		return "", false, errUnreadableAnswer
	}
	raw, decErr := base64.StdEncoding.DecodeString(frame)
	if decErr != nil || !utf8.Valid(raw) {
		return "", false, errUnreadableAnswer
	}
	return string(raw), false, nil
}

var (
	clixmlHeader = regexp.MustCompile(`#< CLIXML\r?\n?`)
	clixmlObjs   = regexp.MustCompile(`(?s)<Objs\b.*?</Objs>`)
	clixmlError  = regexp.MustCompile(`(?s)<S S="Error">(.*?)</S>`)
	clixmlEscape = regexp.MustCompile(`(?:_x[0-9A-Fa-f]{4}_)+`)
	ansiEscape   = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
)

// powerShellDiagnostic turns PowerShell's output into a short, readable reason
// for an error message: the text of its error records and any plain output,
// without serialized progress records.
func powerShellDiagnostic(out string) string {
	var parts []string
	for _, m := range clixmlError.FindAllStringSubmatch(out, -1) {
		if s := cleanPowerShellText(html.UnescapeString(m[1])); s != "" {
			parts = append(parts, s)
		}
	}
	plain := clixmlObjs.ReplaceAllString(clixmlHeader.ReplaceAllString(out, ""), "")
	if s := cleanPowerShellText(plain); s != "" {
		parts = append(parts, s)
	}
	text := strings.Join(parts, "; ")
	if text == "" {
		return ""
	}
	const limit = 400
	if utf8.RuneCountInString(text) > limit {
		text = string([]rune(text)[:limit]) + "…"
	}
	return " (" + text + ")"
}

// cleanPowerShellText undoes CLIXML's _xHHHH_ escapes, drops terminal colour
// codes and folds whitespace. Each escape is one UTF-16 code unit, so a run of
// them is decoded together and a surrogate pair becomes one character. A
// literal "_x" in the text is serialized as _x005F_ followed by "x...", which
// ends the run, so it stays literal.
func cleanPowerShellText(s string) string {
	s = clixmlEscape.ReplaceAllStringFunc(s, func(m string) string {
		const width = len("_xHHHH_")
		units := make([]uint16, 0, len(m)/width)
		for i := 0; i+width <= len(m); i += width {
			n, err := strconv.ParseUint(m[i+2:i+6], 16, 16)
			if err != nil {
				return m
			}
			units = append(units, uint16(n))
		}
		return string(utf16.Decode(units))
	})
	s = ansiEscape.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(s), " ")
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
