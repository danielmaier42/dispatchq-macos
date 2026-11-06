package api

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

// scriptRunner records the PowerShell invocation and answers with canned output.
type scriptRunner struct {
	name string
	args []string
	out  string
	err  error
}

func (r *scriptRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.name, r.args = name, args
	return []byte(r.out), r.err
}

// decodedScript recovers the script from the -EncodedCommand argument.
func decodedScript(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a == "-EncodedCommand" && i+1 < len(args) {
			raw, err := base64.StdEncoding.DecodeString(args[i+1])
			if err != nil {
				t.Fatalf("encoded command is not base64: %v", err)
			}
			units := make([]uint16, len(raw)/2)
			for j := range units {
				units[j] = uint16(raw[2*j]) | uint16(raw[2*j+1])<<8
			}
			return string(utf16.Decode(units))
		}
	}
	t.Fatalf("no -EncodedCommand in %v", args)
	return ""
}

func TestPowerShellFolderChooserDecodesTheAnswerAndRunsSTA(t *testing.T) {
	picked := `C:\Users\Zoë O'Brien\Projekte "x"`
	r := &scriptRunner{out: base64.StdEncoding.EncodeToString([]byte(picked)) + "\r\n"}
	got, chosen, err := PowerShellFolderChooser(r)(context.Background(), "")
	if err != nil || !chosen || got != picked {
		t.Fatalf("got (%q, %v, %v), want (%q, true, nil)", got, chosen, err, picked)
	}
	if r.name != "powershell.exe" {
		t.Errorf("ran %q, want powershell.exe", r.name)
	}
	if !strings.Contains(strings.Join(r.args, " "), "-STA") {
		t.Errorf("args %v lack -STA, which Windows Forms dialogs require", r.args)
	}
}

func TestPowerShellDialogEmptyAnswerIsACancel(t *testing.T) {
	r := &scriptRunner{out: "\r\n"}
	got, chosen, err := PowerShellSaveFileDialog(r)(context.Background(), "Export", "task.dispatchq")
	if err != nil || chosen || got != "" {
		t.Fatalf("got (%q, %v, %v), want a cancel", got, chosen, err)
	}
}

func TestPowerShellDialogFailureIsAnError(t *testing.T) {
	r := &scriptRunner{out: "boom", err: errors.New("exit status 1")}
	if _, _, err := PowerShellFolderChooser(r)(context.Background(), ""); err == nil {
		t.Fatal("a failed PowerShell run must be an error, not a cancel")
	}
}

func TestPowerShellDialogCarriesValuesWithoutQuoting(t *testing.T) {
	r := &scriptRunner{}
	prompt, name := `Export "it's"`, `a'b"c.dispatchq`
	if _, _, err := PowerShellSaveFileDialog(r)(context.Background(), prompt, name); err != nil {
		t.Fatal(err)
	}
	script := decodedScript(t, r.args)
	for _, want := range []string{b64(prompt), b64(name)} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks base64 of an argument: %s", want)
		}
	}
	if strings.Contains(script, name) {
		t.Error("the file name must not appear raw in the script, where a quote could break out of it")
	}
}
