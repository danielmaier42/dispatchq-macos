package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielmaier42/claudeq/internal/system"
)

// OSAScriptFolderChooser returns a FolderChooser that opens the native macOS
// folder-selection dialog via `osascript`. It works because the daemon runs in
// the user's GUI session. A user cancel is reported as chosen=false, not an error.
func OSAScriptFolderChooser(r system.Runner) FolderChooser {
	return func(ctx context.Context, start string) (string, bool, error) {
		expr := `choose folder with prompt "Select the task's working directory"`
		if loc := existingDir(start); loc != "" {
			expr += fmt.Sprintf(` default location (POSIX file %q)`, loc)
		}
		return chooseWithOSAScript(ctx, r, "choose folder", "-e", "POSIX path of ("+expr+")")
	}
}

// OSAScriptSaveFileDialog returns a SaveFileDialog that opens the native macOS
// "Save as" panel via `osascript`. The panel itself asks before replacing an
// existing file. The prompt and default name reach the AppleScript as run
// arguments (`on run argv`), so they need no AppleScript string escaping.
func OSAScriptSaveFileDialog(r system.Runner) SaveFileDialog {
	return func(ctx context.Context, prompt, defaultName string) (string, bool, error) {
		return chooseWithOSAScript(ctx, r, "choose file name",
			"-e", "on run argv",
			"-e", "return POSIX path of (choose file name with prompt (item 1 of argv) default name (item 2 of argv))",
			"-e", "end run",
			prompt, defaultName)
	}
}

// chooseWithOSAScript runs an osascript dialog that returns a POSIX path.
// A user cancel (AppleScript error -128) is reported as chosen=false, not an
// error; what is the dialog's name for error messages.
func chooseWithOSAScript(ctx context.Context, r system.Runner, what string, args ...string) (string, bool, error) {
	out, err := system.ShowingWindows(r).Run(ctx, "osascript", args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(text, "-128") || strings.Contains(strings.ToLower(text), "cancel") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("osascript %s: %w (%s)", what, err, text)
	}
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

// existingDir maps a requested start location onto a directory that actually
// exists, so the dialog can always open. `choose folder` fails outright (error
// -1700, no dialog at all) when its default location is gone — which is exactly
// the case where the user needs the picker: the task's working directory was
// deleted or renamed. Falls back to the nearest existing ancestor, then to the
// home directory, and finally to "" (let macOS pick the default location).
func existingDir(start string) string {
	if start = strings.TrimSpace(start); filepath.IsAbs(start) {
		for p := filepath.Clean(start); ; {
			fi, err := os.Stat(p)
			switch {
			case err == nil && fi.IsDir():
				return p
			case err == nil, errors.Is(err, fs.ErrNotExist):
				// A file (use its folder) or a gone path: try the parent.
			default:
				// Unreadable for the daemon (e.g. a TCC-protected folder) does
				// not mean gone — the dialog has its own access, so keep it.
				return p
			}
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			return home
		}
	}
	return ""
}
