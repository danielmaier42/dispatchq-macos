package api

import (
	"context"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/system"
)

// launchRunner stands for system.Real: it records whether a command ran in the
// background or through the runner that lets a command show its windows.
type launchRunner struct {
	ran     *[]string
	showing bool
	out     string
}

func (r launchRunner) Run(_ context.Context, name string, _ ...string) ([]byte, error) {
	mode := "background"
	if r.showing {
		mode = "showing windows"
	}
	*r.ran = append(*r.ran, name+" "+mode)
	return []byte(r.out), nil
}

func (r launchRunner) ShowingWindows() system.Runner {
	r.showing = true
	return r
}

// Every native dialog is a window the user must see and answer. Run like a
// background command, Windows starts PowerShell with SW_HIDE and the dialog
// never appears, so each dialog has to ask for the runner that shows windows.
func TestNativeDialogsRunAsCommandsThatShowWindows(t *testing.T) {
	answer := psAnswerOutput(`C:\Users\Zoë\Projekte`)
	for name, open := range map[string]func(system.Runner) error{
		"PowerShell folder": func(r system.Runner) error {
			_, _, err := PowerShellFolderChooser(r)(context.Background(), "")
			return err
		},
		"PowerShell save": func(r system.Runner) error {
			_, _, err := PowerShellSaveFileDialog(r)(context.Background(), "Export", "a.dispatchq")
			return err
		},
		"osascript folder": func(r system.Runner) error {
			_, _, err := OSAScriptFolderChooser(r)(context.Background(), "")
			return err
		},
		"osascript save": func(r system.Runner) error {
			_, _, err := OSAScriptSaveFileDialog(r)(context.Background(), "Export", "a.dispatchq")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var ran []string
			if err := open(launchRunner{ran: &ran, out: answer}); err != nil {
				t.Fatal(err)
			}
			if len(ran) != 1 || !strings.HasSuffix(ran[0], " showing windows") {
				t.Fatalf("ran %q, want one command through the runner that shows windows", ran)
			}
		})
	}
}
