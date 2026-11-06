//go:build !windows

package main

import "os/exec"

// defaultEditor is what runs when neither $VISUAL nor $EDITOR is set.
const defaultEditor = "vi"

// editorCommand opens path in editor. The command goes through the shell, the
// way git does it, so $EDITOR may carry arguments ("code -w") as well as a path
// with spaces; the file is passed as an argument rather than interpolated, so
// its name is never re-parsed.
func editorCommand(editor, path string) *exec.Cmd {
	return exec.Command("sh", "-c", editor+` "$1"`, "sh", path) // #nosec G204 — the user's own $EDITOR
}
