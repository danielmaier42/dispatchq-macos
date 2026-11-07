package api

import (
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// screenshotCLIXML is the start of what a German Windows PowerShell 5.1 wrote
// to stderr on its first Add-Type, the output that made the folder dialog
// report "unreadable answer".
const screenshotCLIXML = "#< CLIXML\r\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\">" +
	"<Obj S=\"progress\" RefId=\"0\"><TN RefId=\"0\"><T>System.Management.Automation.PSCustomObject</T><T>System.Object</T></TN>" +
	"<MS><I64 N=\"SourceId\">1</I64><PR N=\"Record\"><AV>Module werden für erstmalige Verwendung vorbereitet.</AV><AI>0</AI><Nil />" +
	"<PI>-1</PI><PC>-1</PC><T>Completed</T><SR>-1</SR><SD> </SD></PR></MS></Obj></Objs>"

// PowerShell's progress and error records must not stop the answer from being
// read, wherever they land around it.
func TestPowerShellDialogReadsTheAnswerAroundCLIXMLNoise(t *testing.T) {
	picked := `C:\Users\Zoë O'Brien\Projekte\日本`
	answer := psAnswerOutput(picked)
	head, tail, _ := strings.Cut(screenshotCLIXML, "\r\n")
	for name, out := range map[string]string{
		"noise before":           screenshotCLIXML + "\r\n" + answer,
		"noise after":            answer + screenshotCLIXML,
		"inside the CLIXML head": head + "\r\n" + answer + tail, // how stdout and stderr interleave in practice
	} {
		t.Run(name, func(t *testing.T) {
			r := &scriptRunner{out: out}
			got, chosen, err := PowerShellFolderChooser(r)(context.Background(), "")
			if err != nil || !chosen || got != picked {
				t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", got, chosen, err, picked)
			}
		})
	}
}

func TestPowerShellDialogCancelAroundCLIXMLNoise(t *testing.T) {
	r := &scriptRunner{out: screenshotCLIXML + psCancelled}
	got, chosen, err := PowerShellSaveFileDialog(r)(context.Background(), "Export", "a.dispatchq")
	if err != nil || chosen || got != "" {
		t.Fatalf("got (%q, %v, %v), want a cancel", got, chosen, err)
	}
}

// A run that ends without an answer frame did not ask the user: reporting it
// as a cancel would make a broken dialog look like the user saying no. The
// error names the reason without dumping serialized progress records.
func TestPowerShellDialogWithoutAnswerIsAReadableError(t *testing.T) {
	for name, r := range map[string]*scriptRunner{
		"only progress": {out: screenshotCLIXML},
		"empty":         {out: "\r\n"},
		"torn frame":    {out: psAnswerOpen + "QzpcVXNl"},
		"failed": {
			out: "#< CLIXML\r\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\">" +
				"<S S=\"Error\">_x001B_[31;1mAdd-Type : Pfad &quot;ä&quot; nicht gefunden_x001B_[0m_x000D__x000A_</S></Objs>",
			err: errors.New("exit status 1"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, chosen, err := PowerShellFolderChooser(r)(context.Background(), "")
			if err == nil || chosen {
				t.Fatalf("got (chosen=%v, err=%v), want an error", chosen, err)
			}
			msg := err.Error()
			for _, noise := range []string{"CLIXML", "<Obj", "_x00", "\x1b"} {
				if strings.Contains(msg, noise) {
					t.Errorf("error %q contains serialized PowerShell output %q", msg, noise)
				}
			}
			if name == "failed" && !strings.Contains(msg, `Add-Type : Pfad "ä" nicht gefunden`) {
				t.Errorf("error %q lacks PowerShell's own error message", msg)
			}
		})
	}
}

// When the time limit ends the dialog, the error says so instead of quoting
// whatever the killed process wrote.
func TestPowerShellDialogTimeoutIsReported(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	r := &scriptRunner{out: screenshotCLIXML, err: errors.New("exit status 1")}
	_, chosen, err := PowerShellFolderChooser(r)(ctx, "")
	if chosen || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got (chosen=%v, err=%v), want a deadline error", chosen, err)
	}
}

// realPowerShell runs the script part that does not need a desktop through a
// real PowerShell: powershell.exe on Windows, pwsh elsewhere when installed.
type realPowerShell struct{ exe string }

func newRealPowerShell(t *testing.T) realPowerShell {
	t.Helper()
	exe := "powershell.exe"
	if runtime.GOOS != "windows" {
		exe = "pwsh"
	}
	path, err := exec.LookPath(exe)
	if err != nil {
		t.Skipf("%s not installed", exe)
	}
	return realPowerShell{exe: path}
}

func (p realPowerShell) Run(ctx context.Context, _ string, args ...string) ([]byte, error) {
	if runtime.GOOS != "windows" { // -STA exists only on Windows
		kept := args[:0:0]
		for _, a := range args {
			if a != "-STA" {
				kept = append(kept, a)
			}
		}
		args = kept
	}
	return exec.CommandContext(ctx, p.exe, args...).CombinedOutput()
}

// With a real PowerShell, the answer survives progress and error records that
// PowerShell serializes into the same stream, and Unicode paths arrive intact.
func TestRealPowerShellAnswerSurvivesItsOwnNoise(t *testing.T) {
	ps := newRealPowerShell(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	picked := `C:\Users\Zoë O'Brien\Projekte "x"\日本`
	script := psDialogProtocol + `
$ProgressPreference = 'Continue'
Write-Progress -Activity 'Module werden für erstmalige Verwendung vorbereitet.' -PercentComplete 50
Write-Error 'noise on stderr' -ErrorAction Continue
Emit (Decode '` + b64(picked) + `')
Write-Progress -Activity 'x' -Completed
`
	got, chosen, err := runPowerShellDialog(ctx, ps, "test", script)
	if err != nil || !chosen || got != picked {
		t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", got, chosen, err, picked)
	}

	got, chosen, err = runPowerShellDialog(ctx, ps, "test", psDialogProtocol+"Write-Progress -Activity 'x' -PercentComplete 1\nEmitCancelled\n")
	if err != nil || chosen || got != "" {
		t.Fatalf("cancel: got (%q, %v, %v), want a cancel", got, chosen, err)
	}

	_, chosen, err = runPowerShellDialog(ctx, ps, "test", psDialogProtocol+"throw 'Pfad ä kaputt'\n")
	if err == nil || chosen || !strings.Contains(err.Error(), "Pfad ä kaputt") || strings.Contains(err.Error(), "CLIXML") {
		t.Fatalf("failure: got (chosen=%v, err=%v), want an error carrying PowerShell's message", chosen, err)
	}
}

// The preamble's own setting keeps progress records (like the first-use module
// preparation) out of the output entirely.
func TestRealPowerShellProtocolSilencesProgress(t *testing.T) {
	ps := newRealPowerShell(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := ps.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-EncodedCommand",
		encodePowerShell(psDialogProtocol+"Write-Progress -Activity 'Module werden vorbereitet' -PercentComplete 50\nEmitCancelled\n"))
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if string(out) != psCancelled {
		t.Fatalf("output %q, want only the cancel frame", out)
	}
}

// powerShellAST lists the member calls in script in source order, as parsed by
// a real PowerShell, and fails on any syntax error.
func powerShellAST(t *testing.T, ps realPowerShell, script string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe := `
$src = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + base64.StdEncoding.EncodeToString([]byte(script)) + `'))
$tokens = $null; $errs = $null
$ast = [System.Management.Automation.Language.Parser]::ParseInput($src, [ref]$tokens, [ref]$errs)
foreach ($e in $errs) { [Console]::Out.WriteLine('PARSE-ERROR: ' + $e.Message + ' @' + $e.Extent.StartLineNumber) }
$calls = $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.InvokeMemberExpressionAst] }, $true)
foreach ($c in ($calls | Sort-Object { $_.Extent.StartOffset })) { [Console]::Out.WriteLine($c.Expression.Extent.Text + '.' + $c.Member.Extent.Text) }
`
	out, err := ps.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(probe))
	if err != nil {
		t.Fatalf("parse probe: %v: %s", err, out)
	}
	var calls []string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if strings.HasPrefix(line, "PARSE-ERROR") {
			t.Errorf("dialog script does not parse: %s", line)
		}
		if line != "" {
			calls = append(calls, line)
		}
	}
	return calls
}

// Both dialog scripts parse, and in source order show and activate their
// owner window before the dialog opens and close it afterwards. This checks
// the script's structure only; whether the dialog comes to the front is a
// Windows acceptance check.
func TestRealPowerShellDialogScriptsShowTheirOwnerFirst(t *testing.T) {
	ps := newRealPowerShell(t)
	for name, open := range map[string]func(*scriptRunner){
		"folder": func(r *scriptRunner) { _, _, _ = PowerShellFolderChooser(r)(context.Background(), `C:\Users\Zoë`) },
		"save": func(r *scriptRunner) {
			_, _, _ = PowerShellSaveFileDialog(r)(context.Background(), "Export", "ä.dispatchq")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := &scriptRunner{out: psCancelled}
			open(r)
			calls := powerShellAST(t, ps, decodedScript(t, r.args))
			order := map[string]int{}
			for i, c := range calls {
				if _, seen := order[c]; !seen {
					order[c] = i
				}
			}
			show, okShow := order["$owner.Show"]
			activate, okActivate := order["$owner.Activate"]
			dialog, okDialog := order["$d.ShowDialog"]
			if !okShow || !okActivate || !okDialog {
				t.Fatalf("member calls %v lack $owner.Show, $owner.Activate or $d.ShowDialog", calls)
			}
			if show >= activate || activate >= dialog {
				t.Errorf("member calls %v: the owner must be shown and activated before the dialog opens", calls)
			}
			if _, ok := order["$owner.Close"]; !ok {
				t.Errorf("member calls %v never close the owner window", calls)
			}
		})
	}
}

// A frame does not outweigh how the run ended: a run that failed or hit the
// time limit is an error even when its output holds an answer or a cancel, and
// output with more than one frame is not read at all.
func TestPowerShellDialogFrameNeedsACleanRun(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	failed := errors.New("exit status 1")
	for name, c := range map[string]struct {
		ctx context.Context
		r   *scriptRunner
	}{
		"answer from a failed run":        {context.Background(), &scriptRunner{out: psAnswerOutput(`C:\x`), err: failed}},
		"cancel from a failed run":        {context.Background(), &scriptRunner{out: "failure " + psCancelled, err: failed}},
		"answer after the time limit":     {expired, &scriptRunner{out: psAnswerOutput(`C:\x`)}},
		"cancel after the time limit":     {expired, &scriptRunner{out: psCancelled}},
		"answer and cancel":               {context.Background(), &scriptRunner{out: psAnswerOutput(`C:\x`) + psCancelled}},
		"cancel and answer":               {context.Background(), &scriptRunner{out: psCancelled + psAnswerOutput(`C:\x`)}},
		"two answers":                     {context.Background(), &scriptRunner{out: psAnswerOutput(`C:\x`) + psAnswerOutput(`C:\y`)}},
		"torn answer before a whole one":  {context.Background(), &scriptRunner{out: psAnswerOpen + "QzpcVXNl" + psAnswerOutput(`C:\y`)}},
		"answer that is not base64 UTF-8": {context.Background(), &scriptRunner{out: psAnswerOpen + "/w==" + psAnswerClose}},
	} {
		t.Run(name, func(t *testing.T) {
			for dialog, open := range map[string]func() (string, bool, error){
				"folder": func() (string, bool, error) { return PowerShellFolderChooser(c.r)(c.ctx, "") },
				"save":   func() (string, bool, error) { return PowerShellSaveFileDialog(c.r)(c.ctx, "Export", "a.dispatchq") },
			} {
				got, chosen, err := open()
				if err == nil || chosen || got != "" {
					t.Errorf("%s: got (%q, %v, %v), want an error", dialog, got, chosen, err)
				}
			}
		})
	}
}

// Error text from PowerShell keeps characters outside the Basic Multilingual
// Plane, which CLIXML escapes as two UTF-16 code units, and keeps a literal
// "_x0041_" that CLIXML protects by escaping its underscore.
func TestPowerShellDiagnosticDecodesUTF16Escapes(t *testing.T) {
	for in, want := range map[string]string{
		"_xD83D__xDE00_":         "😀",
		`C:\_xD83D__xDE00_\Zoë`:  `C:\😀\Zoë`,
		"_x005F_x0041_":          "_x0041_",
		"a_x000D__x000A_b":       "a b",
		"lone _xD83D_ surrogate": "lone \uFFFD surrogate",
		"_x0041__x005F_x0042_":   "A_x0042_",
	} {
		if got := cleanPowerShellText(in); got != want {
			t.Errorf("cleanPowerShellText(%q) = %q, want %q", in, got, want)
		}
	}
}

// A real PowerShell error naming a path outside the Basic Multilingual Plane
// arrives readable in the dialog's error.
func TestRealPowerShellErrorKeepsAstralCharacters(t *testing.T) {
	ps := newRealPowerShell(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, chosen, err := runPowerShellDialog(ctx, ps, "test", psDialogProtocol+"throw (Decode '"+b64(`Pfad C:\😀 kaputt`)+"')\n")
	if err == nil || chosen || !strings.Contains(err.Error(), `Pfad C:\😀 kaputt`) {
		t.Fatalf("got (chosen=%v, err=%v), want an error carrying the path", chosen, err)
	}
}
