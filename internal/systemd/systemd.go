// Package systemd installs dispatchqd as a systemd user service, so it starts
// at login and is restarted if it exits. It is the Linux counterpart of package
// launchd.
package systemd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/danielmaier42/claudeq/internal/system"
)

// DefaultUnit is the name of the user service.
const DefaultUnit = "dispatchq.service"

// Config parameterizes the generated unit file.
type Config struct {
	BinPath    string   // absolute path to dispatchqd
	Args       []string // arguments, e.g. ["run"]
	StdoutPath string
	StderrPath string
	// Env is set for the daemon, e.g. DISPATCHQ_HOME. The systemd user
	// manager does not read the user's shell profile, so a value the shell has
	// must be written into the unit.
	Env map[string]string
}

var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=DispatchQ background service
Documentation=https://www.dispatchq.de/
# Never give up restarting (see Restart below).
StartLimitIntervalSec=0

[Service]
ExecStart={{.ExecStart}}
{{- range .Env}}
Environment={{.}}
{{- end}}
# Restart a daemon that exits. A daemon that exits at once (another one owns
# the data directory) is retried with a growing delay of up to a minute, and
# systemd never gives up on it.
Restart=always
RestartSec=5
RestartSteps=5
RestartMaxDelaySec=60
# On stop, signal only the daemon. It lets in-flight runs finish for up to
# 30 seconds and then stops their process groups itself. Whatever is left
# after the timeout is killed.
KillMode=mixed
TimeoutStopSec=60
StandardOutput=append:{{.StdoutPath}}
StandardError=append:{{.StderrPath}}

[Install]
WantedBy=default.target
`))

// Unit renders the unit file for the config.
func Unit(c Config) (string, error) {
	if !filepath.IsAbs(c.BinPath) {
		return "", fmt.Errorf("render unit: %q is not an absolute path", c.BinPath)
	}
	words := make([]string, 0, 1+len(c.Args))
	for _, w := range append([]string{c.BinPath}, c.Args...) {
		words = append(words, quote(w))
	}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, quote(k+"="+c.Env[k]))
	}
	data := struct {
		ExecStart, StdoutPath, StderrPath string
		Env                               []string
	}{
		ExecStart:  strings.Join(words, " "),
		StdoutPath: escapeSpecifiers(c.StdoutPath),
		StderrPath: escapeSpecifiers(c.StderrPath),
		Env:        env,
	}
	var b strings.Builder
	if err := unitTemplate.Execute(&b, data); err != nil {
		return "", fmt.Errorf("render unit: %w", err)
	}
	return b.String(), nil
}

// quote renders one ExecStart word or Environment assignment. systemd splits the line at spaces, reads
// C-style escapes inside double quotes, expands "%" specifiers and "$"
// variables, so each word is quoted and those characters are escaped.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$")
	return `"` + escapeSpecifiers(r.Replace(s)) + `"`
}

// escapeSpecifiers keeps systemd from reading "%" as a specifier.
func escapeSpecifiers(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

// Agent installs and removes the user service. Dir is the user unit directory
// (e.g. ~/.config/systemd/user).
type Agent struct {
	Runner system.Runner
	Dir    string
	Unit   string
}

func (a Agent) unit() string {
	if a.Unit != "" {
		return a.Unit
	}
	return DefaultUnit
}

// Path is where the unit file goes.
func (a Agent) Path() string {
	return filepath.Join(a.Dir, a.unit())
}

// Install writes the unit file, enables it for login, and (re)starts it, so a
// new binary takes over at once.
func (a Agent) Install(ctx context.Context, unit string) error {
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return fmt.Errorf("create systemd user unit dir: %w", err)
	}
	if err := os.WriteFile(a.Path(), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", a.unit()},
		{"restart", a.unit()},
	} {
		if out, err := a.systemctl(ctx, args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %w (%s)", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Uninstall stops and disables the service and removes its unit file.
func (a Agent) Uninstall(ctx context.Context) error {
	// Best-effort: the service may not be loaded at all.
	_, _ = a.systemctl(ctx, "disable", "--now", a.unit())
	if err := os.Remove(a.Path()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit: %w", err)
	}
	if out, err := a.systemctl(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a Agent) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return a.Runner.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// UserUnitDir is the directory where the systemd user manager looks for the
// user's own unit files: $XDG_CONFIG_HOME/systemd/user, by default
// ~/.config/systemd/user. The variable is read from the manager, not from the
// calling shell, which can set it differently.
func UserUnitDir(ctx context.Context, r system.Runner) (string, error) {
	env, err := ManagerEnv(ctx, r)
	if err != nil {
		return "", err
	}
	if d := env["XDG_CONFIG_HOME"]; filepath.IsAbs(d) {
		return filepath.Join(d, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// ManagerEnv returns the environment of the systemd user manager: what every
// user service and every `systemd-run --user` unit starts with. The desktop
// session adds DISPLAY, WAYLAND_DISPLAY and XDG_CURRENT_DESKTOP to it when it
// starts, which can be after dispatchq's own service started.
func ManagerEnv(ctx context.Context, r system.Runner) (map[string]string, error) {
	out, err := r.Run(ctx, "systemctl", "--user", "show-environment")
	if err != nil {
		return nil, fmt.Errorf("systemctl --user show-environment: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env, nil
}

// SessionRunner runs each command as a transient unit of the systemd user
// manager and waits for it. The command gets the manager's current
// environment (the desktop session's display), not the environment the
// daemon started with. Output and exit status come back as from the command
// itself.
type SessionRunner struct {
	Runner system.Runner
}

// Run runs name with args through `systemd-run --user --wait --pipe`.
func (s SessionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	full := append([]string{"--user", "--wait", "--pipe", "--quiet", "--collect", "--", name}, args...)
	return s.Runner.Run(ctx, "systemd-run", full...)
}
