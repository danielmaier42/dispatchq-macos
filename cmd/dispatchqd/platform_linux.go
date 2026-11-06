package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/system"
	"github.com/danielmaier42/claudeq/internal/systemd"
)

// newPlatform wires the Linux services: a systemd user service, a logind
// sleep lock through systemd-inhibit, freedesktop notifications, and the
// desktop's dialog and terminal programs. Wake from sleep needs root on Linux
// and is not offered. A program that is not installed switches its feature off.
func newPlatform(r system.Runner) platform {
	p := platform{
		service: systemdService{runner: r},
		// The same hold as the CLI's run-now: a logind lock via systemd-inhibit.
		sleep: system.DefaultSleepInhibitor(),
		// The notify package answers "unsupported" off macOS: Linux has no
		// per-app notification permission to ask about.
		notifyStatus: notify.MacAuthorization,
		osVersion:    func() string { return linuxOSName(readOSRelease()) },
	}
	if installed("notify-send") {
		p.desktop = notify.FreeDesktop{Runner: r}
	}
	if !installed("systemd-run") {
		return p
	}
	// The dialogs and the terminal are chosen when they open, for the desktop
	// the session runs now: the daemon can start at login before the desktop
	// has told the user manager which one it is. The dialogs also run through
	// the manager, so they get its current DISPLAY and WAYLAND_DISPLAY.
	session := systemd.SessionRunner{Runner: r}
	if _, ok := pickDialogTool("", installed); ok {
		p.chooseFolder = func(ctx context.Context, start string) (string, bool, error) {
			tool, _ := pickDialogTool(sessionDesktop(ctx, r), installed)
			return api.DesktopFolderChooser(session, tool)(ctx, start)
		}
		p.saveFile = func(ctx context.Context, prompt, defaultName string) (string, bool, error) {
			tool, _ := pickDialogTool(sessionDesktop(ctx, r), installed)
			return api.DesktopSaveFileDialog(session, tool)(ctx, prompt, defaultName)
		}
	}
	if _, ok := pickTerminal("", installed); ok {
		p.openTerminal = func(ctx context.Context, dir string, argv []string) error {
			term, _ := pickTerminal(sessionDesktop(ctx, r), installed)
			return api.DesktopTerminalOpener(r, term)(ctx, dir, argv)
		}
	}
	return p
}

// sessionDesktop is XDG_CURRENT_DESKTOP as the systemd user manager has it
// now, or the daemon's own value when the manager cannot be asked.
func sessionDesktop(ctx context.Context, r system.Runner) string {
	if env, err := systemd.ManagerEnv(ctx, r); err == nil {
		if d := env["XDG_CURRENT_DESKTOP"]; d != "" {
			return d
		}
	}
	return os.Getenv("XDG_CURRENT_DESKTOP")
}

func installed(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// pickDialogTool chooses the dialog program: kdialog on KDE Plasma, zenity on
// any other desktop, and whichever of the two exists when the preferred one
// does not.
func pickDialogTool(desktop string, have func(string) bool) (api.DialogTool, bool) {
	order := []api.DialogTool{api.Zenity, api.Kdialog}
	if onKDE(desktop) {
		order = []api.DialogTool{api.Kdialog, api.Zenity}
	}
	for _, t := range order {
		if have(string(t)) {
			return t, true
		}
	}
	return "", false
}

// pickTerminal chooses the terminal: xdg-terminal-exec, which opens the
// terminal the user has set as default, then the desktop's own terminal, then
// Debian's x-terminal-emulator.
func pickTerminal(desktop string, have func(string) bool) (api.TerminalCommand, bool) {
	candidates := []api.TerminalCommand{{"xdg-terminal-exec"}}
	native := []api.TerminalCommand{{"gnome-terminal", "--"}, {"konsole", "-e"}}
	if onKDE(desktop) {
		native[0], native[1] = native[1], native[0]
	}
	candidates = append(candidates, native...)
	candidates = append(candidates, api.TerminalCommand{"x-terminal-emulator", "-e"})
	for _, c := range candidates {
		if have(c[0]) {
			return c, true
		}
	}
	return nil, false
}

// onKDE reports whether XDG_CURRENT_DESKTOP (a colon-separated list) names KDE.
func onKDE(desktop string) bool {
	for _, d := range strings.Split(desktop, ":") {
		if strings.EqualFold(d, "KDE") {
			return true
		}
	}
	return false
}

func readOSRelease() string {
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		if b, err := os.ReadFile(p); err == nil {
			return string(b)
		}
	}
	return ""
}

// linuxOSName returns PRETTY_NAME from an os-release file, e.g.
// "Ubuntu 26.04 LTS", or "Linux" when the file does not name the system.
func linuxOSName(osRelease string) string {
	for _, line := range strings.Split(osRelease, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "PRETTY_NAME="); ok {
			if v = strings.Trim(v, `"'`); v != "" {
				return v
			}
		}
	}
	return "Linux"
}

// systemdService installs the daemon as a systemd user service.
type systemdService struct {
	runner system.Runner
}

func (s systemdService) agent(ctx context.Context) (systemd.Agent, error) {
	dir, err := systemd.UserUnitDir(ctx, s.runner)
	if err != nil {
		return systemd.Agent{}, err
	}
	return systemd.Agent{Runner: s.runner, Dir: dir}, nil
}

// RetireLegacy does nothing: no earlier release ran on Linux.
func (systemdService) RetireLegacy(context.Context) error { return nil }

func (s systemdService) Install(ctx context.Context, spec serviceSpec, out io.Writer) error {
	unit, err := systemd.Unit(systemd.Config{
		BinPath: spec.Bin, Args: spec.Args, StdoutPath: spec.StdoutPath, StderrPath: spec.StderrPath,
		// The manager does not read the shell profile, so a data directory the
		// shell resolves differently (DISPATCHQ_HOME, XDG_DATA_HOME) would
		// split the daemon and the CLI. The unit pins the one install found.
		Env: map[string]string{"DISPATCHQ_HOME": spec.Home},
	})
	if err != nil {
		return err
	}
	agent, err := s.agent(ctx)
	if err != nil {
		return err
	}
	if err := agent.Install(ctx, unit); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed systemd user service %s (%s)\n", systemd.DefaultUnit, agent.Path())
	fmt.Fprintln(out, "dispatchqd will now start at login and restart on exit.")
	fmt.Fprintln(out, "It runs while you are logged in. Wake from sleep is not available on Linux.")
	return nil
}

func (s systemdService) Uninstall(ctx context.Context, out io.Writer) error {
	agent, err := s.agent(ctx)
	if err != nil {
		return err
	}
	if err := agent.Uninstall(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed systemd user service %s\n", systemd.DefaultUnit)
	return nil
}
