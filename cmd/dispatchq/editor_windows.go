//go:build windows

package main

import (
	"os/exec"
	"strings"
)

// defaultEditor is what runs when neither $VISUAL nor $EDITOR is set.
const defaultEditor = "notepad"

// editorCommand opens path in editor. Windows has no sh to hand the line to, so
// $EDITOR is split on whitespace into a program and its arguments ("code -w");
// an editor path with spaces needs a program on PATH instead.
func editorCommand(editor, path string) *exec.Cmd {
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		fields = []string{defaultEditor}
	}
	return exec.Command(fields[0], append(fields[1:], path)...) // #nosec G204 — the user's own $EDITOR
}
