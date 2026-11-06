package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielmaier42/claudeq/internal/system"
)

// DialogTool names the program that shows file dialogs on a Linux desktop.
type DialogTool string

// The dialog programs dispatchq knows: kdialog on KDE Plasma, zenity on GNOME
// and most other desktops.
const (
	Kdialog DialogTool = "kdialog"
	Zenity  DialogTool = "zenity"
)

// DesktopFolderChooser returns a FolderChooser that opens the desktop's folder
// dialog through tool. A user cancel is reported as chosen=false, not an error.
func DesktopFolderChooser(r system.Runner, tool DialogTool) FolderChooser {
	const title = "Select the task's working directory"
	return func(ctx context.Context, start string) (string, bool, error) {
		loc := existingDir(start)
		var args []string
		switch tool {
		case Kdialog:
			args = []string{"--title", title, "--getexistingdirectory"}
			if loc != "" {
				args = append(args, loc)
			}
		default:
			args = []string{"--file-selection", "--directory", "--title=" + title}
			if loc != "" {
				// The trailing slash opens the folder itself, not its parent.
				args = append(args, "--filename="+strings.TrimSuffix(loc, "/")+"/")
			}
		}
		return chooseWithDialog(ctx, r, tool, args)
	}
}

// DesktopSaveFileDialog returns a SaveFileDialog that opens the desktop's
// "Save as" dialog through tool, which asks before it replaces a file.
func DesktopSaveFileDialog(r system.Runner, tool DialogTool) SaveFileDialog {
	return func(ctx context.Context, prompt, defaultName string) (string, bool, error) {
		start := defaultName
		if home, err := os.UserHomeDir(); err == nil {
			start = filepath.Join(home, filepath.Base(defaultName))
		}
		var args []string
		switch tool {
		case Kdialog:
			args = []string{"--title", prompt, "--getsavefilename", start}
		default:
			args = []string{"--file-selection", "--save", "--confirm-overwrite",
				"--title=" + prompt, "--filename=" + start}
		}
		return chooseWithDialog(ctx, r, tool, args)
	}
}

// chooseWithDialog runs a dialog program that prints the chosen path. Both
// programs exit with status 1 and print nothing when the user cancels. GTK and
// Qt can print warnings into the same output, so the path is the last line
// that is an absolute path.
func chooseWithDialog(ctx context.Context, r system.Runner, tool DialogTool, args []string) (string, bool, error) {
	out, err := r.Run(ctx, string(tool), args...)
	path := lastAbsPath(string(out))
	if err != nil {
		var exit interface{ ExitCode() int }
		if path == "" && errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("%s: %w (%s)", tool, err, strings.TrimSpace(string(out)))
	}
	if path == "" {
		return "", false, nil
	}
	return path, true, nil
}

func lastAbsPath(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimRight(lines[i], "\r"); filepath.IsAbs(l) {
			return l
		}
	}
	return ""
}

// TerminalCommand is the command that opens a terminal window and runs the
// command after it, e.g. ["konsole", "-e"] or ["gnome-terminal", "--"].
type TerminalCommand []string

// DesktopTerminalOpener returns a TerminalOpener that opens a terminal window
// through term. The window runs argv in dir and then keeps an interactive
// shell open, as Terminal.app does on macOS. The terminal starts as its own
// transient systemd user unit, so a restart of the daemon's service does not
// close it. With --service-type=exec, a terminal program that cannot start is
// reported as an error.
func DesktopTerminalOpener(r system.Runner, term TerminalCommand) TerminalOpener {
	// $1 is dir. The rest is argv. A failing cd stops before argv runs.
	const script = `cd -- "$1" || exit; shift; "$@"; exec "${SHELL:-/bin/sh}"`
	return func(ctx context.Context, dir string, argv []string) error {
		args := []string{"--user", "--collect", "--quiet", "--service-type=exec", "--"}
		args = append(args, term...)
		args = append(args, "/bin/sh", "-c", script, "sh", dir)
		args = append(args, argv...)
		out, err := r.Run(ctx, "systemd-run", args...)
		if err != nil {
			return fmt.Errorf("open terminal: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}
