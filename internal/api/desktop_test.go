package api

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// exitErr is a runner error with an exit status, like *exec.ExitError.
type exitErr int

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func TestDesktopFolderChooserArguments(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		tool DialogTool
		want []string
	}{
		{Kdialog, []string{"--title", "Select the task's working directory", "--getexistingdirectory", dir}},
		{Zenity, []string{"--file-selection", "--directory", "--title=Select the task's working directory", "--filename=" + dir + "/"}},
	}
	for _, c := range cases {
		fr := &fakeRunner{out: []byte(dir + "\n")}
		path, chosen, err := DesktopFolderChooser(fr, c.tool)(context.Background(), filepath.Join(dir, "gone"))
		if err != nil || !chosen || path != dir {
			t.Fatalf("%s: got (%q, %v, %v), want (%q, true, nil)", c.tool, path, chosen, err, dir)
		}
		if fr.name != string(c.tool) || !reflect.DeepEqual(fr.args, c.want) {
			t.Fatalf("%s: ran %s %q\nwant %q", c.tool, fr.name, fr.args, c.want)
		}
	}
}

func TestDesktopDialogResults(t *testing.T) {
	cases := []struct {
		name       string
		out        string
		err        error
		wantPath   string
		wantChosen bool
		wantErr    bool
	}{
		{"chosen", "/home/me/proj\n", nil, "/home/me/proj", true, false},
		{"toolkit warnings before the path", "Gtk-Message: 12:00: no parent\n/home/me/my proj\n", nil, "/home/me/my proj", true, false},
		{"cancel", "", exitErr(1), "", false, false},
		{"cancel with a warning", "Gtk-WARNING: whatever\n", exitErr(1), "", false, false},
		{"no output", "", nil, "", false, false},
		{"broken dialog", "cannot open display", exitErr(255), "", false, true},
		{"missing program", "", errors.New(`exec: "zenity": executable file not found`), "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := &fakeRunner{out: []byte(c.out), err: c.err}
			path, chosen, err := DesktopFolderChooser(fr, Zenity)(context.Background(), "")
			if path != c.wantPath || chosen != c.wantChosen || (err != nil) != c.wantErr {
				t.Fatalf("got (%q, %v, %v), want (%q, %v, err=%v)", path, chosen, err, c.wantPath, c.wantChosen, c.wantErr)
			}
		})
	}
}

func TestDesktopSaveFileDialogStartsInHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	start := filepath.Join(home, "task.dispatchq")
	cases := []struct {
		tool DialogTool
		want []string
	}{
		{Kdialog, []string{"--title", "Export task", "--getsavefilename", start}},
		{Zenity, []string{"--file-selection", "--save", "--confirm-overwrite", "--title=Export task", "--filename=" + start}},
	}
	for _, c := range cases {
		fr := &fakeRunner{out: []byte(start + "\n")}
		path, chosen, err := DesktopSaveFileDialog(fr, c.tool)(context.Background(), "Export task", "../task.dispatchq")
		if err != nil || !chosen || path != start {
			t.Fatalf("%s: got (%q, %v, %v)", c.tool, path, chosen, err)
		}
		if !reflect.DeepEqual(fr.args, c.want) {
			t.Fatalf("%s: args %q\nwant %q", c.tool, fr.args, c.want)
		}
	}
}

func TestDesktopTerminalOpenerStartsATransientUnit(t *testing.T) {
	fr := &fakeRunner{}
	open := DesktopTerminalOpener(fr, TerminalCommand{"konsole", "-e"})
	if err := open(context.Background(), "/home/me/my proj", []string{"/opt/claude", "--resume", "s-1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if fr.name != "systemd-run" {
		t.Fatalf("ran %q, want systemd-run", fr.name)
	}
	head := []string{"--user", "--collect", "--quiet", "--service-type=exec", "--", "konsole", "-e", "/bin/sh", "-c"}
	if !reflect.DeepEqual(fr.args[:len(head)], head) {
		t.Fatalf("args %q, want them to start with %q", fr.args, head)
	}
	tail := []string{"sh", "/home/me/my proj", "/opt/claude", "--resume", "s-1"}
	if got := fr.args[len(head)+1:]; !reflect.DeepEqual(got, tail) {
		t.Fatalf("script arguments %q, want %q", got, tail)
	}
}

// The script that the terminal runs changes into dir, runs argv with every
// argument intact, and then starts the shell.
func TestDesktopTerminalScriptRunsArgvInDir(t *testing.T) {
	fr := &fakeRunner{}
	dir := filepath.Join(t.TempDir(), `my "odd" proj`)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	argv := []string{"/bin/sh", "-c", `pwd > "$1"; printf '%s\n' "$2" >> "$1"`, "x", out, "it's $HOME"}
	if err := DesktopTerminalOpener(fr, TerminalCommand{"term"})(context.Background(), dir, argv); err != nil {
		t.Fatal(err)
	}
	// Run what the terminal would run. SHELL=true ends the script at once.
	script := fr.args[6:] // after "--user --collect --quiet --service-type=exec -- term"
	cmd := exec.Command(script[0], script[1:]...)
	cmd.Env = append(os.Environ(), "SHELL=/usr/bin/true")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v (%s)", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	if want := resolved + "\nit's $HOME\n"; strings.Replace(string(got), dir, resolved, 1) != want {
		t.Fatalf("script output %q, want %q", got, want)
	}
}
