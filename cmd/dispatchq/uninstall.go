//go:build !windows

package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/uninstall"
)

// uninstallEnv is what cmdUninstall needs from the outside world.
type uninstallEnv struct {
	exe string
	// user is who runs the command; consoleUser who is logged in at the
	// screen, the user the uninstaller acts for.
	user, consoleUser string
	// dataHome is DISPATCHQ_HOME, "" when unset.
	dataHome string
	in       io.Reader
	out      io.Writer
	// locate and stage find the uninstaller and copy it out of the bundle.
	locate func(exe string) (string, error)
	stage  func(pkg string) (string, error)
	// run runs a command attached to the terminal (sudo asks for the password).
	run func(name string, args ...string) error
}

func cmdUninstall(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return runUninstall(args, uninstallEnv{
		exe: exe, in: os.Stdin, out: os.Stdout,
		user: currentUser(), consoleUser: consoleUser(),
		dataHome: store.HomeOverride(),
		locate:   uninstall.Locate, stage: uninstall.Stage,
		run: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
	})
}

// runUninstall runs the uninstaller package with the command-line installer,
// the same package the app's Uninstall button opens in the Installer window.
func runUninstall(args []string, env uninstallEnv) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	purge := fs.Bool("purge", false, "also delete tasks, run history, artifacts and settings")
	yes := fs.Bool("yes", false, "do not ask before removing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	// The package acts for whoever is logged in at the screen. Run by anyone
	// else (over SSH, or with sudo), it would leave their agent behind.
	if env.user != env.consoleUser {
		return fmt.Errorf("run dispatchq uninstall as %q, the user logged in at the screen, without sudo (it runs as %q)", env.consoleUser, env.user)
	}
	pkg, err := env.locate(env.exe)
	if err != nil {
		return err
	}

	fmt.Fprintln(env.out, "This removes DispatchQ from this Mac: the app, its background service (a run in")
	fmt.Fprintln(env.out, "progress is stopped), the wake permission and the installer receipt.")
	if *purge {
		fmt.Fprintln(env.out, "Your tasks, run history and settings are deleted too.")
		if env.dataHome != "" {
			fmt.Fprintf(env.out, "DISPATCHQ_HOME is set: the uninstaller cannot see it, so delete %s yourself.\n", env.dataHome)
		}
	} else {
		fmt.Fprintln(env.out, "Your tasks, run history and settings are kept (add --purge to delete them).")
	}
	if !*yes && !confirm(env.in, env.out) {
		fmt.Fprintln(env.out, "Nothing was removed.")
		return nil
	}

	staged, err := env.stage(pkg)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(filepath.Dir(staged)) }()
	installerArgs := []string{"/usr/sbin/installer", "-pkg", staged, "-target", "/"}
	if *purge {
		choices := filepath.Join(filepath.Dir(staged), "choices.xml")
		if err := os.WriteFile(choices, uninstall.ChoiceChanges(), 0o600); err != nil {
			return fmt.Errorf("write installer choices: %w", err)
		}
		installerArgs = append(installerArgs, "-applyChoiceChangesXML", choices)
	}
	if err := env.run("sudo", installerArgs...); err != nil {
		return fmt.Errorf("run the uninstaller: %w", err)
	}
	return nil
}

// currentUser is the login name this command runs as.
func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// consoleUser is who is logged in at the screen: the owner of /dev/console.
func consoleUser() string {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10))
	if err != nil {
		return ""
	}
	return u.Username
}

// confirm asks a yes/no question on in; only an explicit yes counts.
func confirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Remove DispatchQ? [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
