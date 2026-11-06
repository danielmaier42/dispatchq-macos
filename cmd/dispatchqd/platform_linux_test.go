package main

import (
	"reflect"
	"testing"

	"github.com/danielmaier42/claudeq/internal/api"
	"github.com/danielmaier42/claudeq/internal/system"
)

func haveOnly(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
}

func TestPickDialogTool(t *testing.T) {
	cases := []struct {
		desktop string
		have    []string
		want    api.DialogTool
		ok      bool
	}{
		{"KDE", []string{"kdialog", "zenity"}, api.Kdialog, true},
		{"ubuntu:GNOME", []string{"kdialog", "zenity"}, api.Zenity, true},
		{"KDE", []string{"zenity"}, api.Zenity, true},
		{"GNOME", []string{"kdialog"}, api.Kdialog, true},
		{"XFCE", nil, "", false},
	}
	for _, c := range cases {
		got, ok := pickDialogTool(c.desktop, haveOnly(c.have...))
		if got != c.want || ok != c.ok {
			t.Errorf("%s with %v: got (%q, %v), want (%q, %v)", c.desktop, c.have, got, ok, c.want, c.ok)
		}
	}
}

func TestPickTerminal(t *testing.T) {
	cases := []struct {
		desktop string
		have    []string
		want    api.TerminalCommand
	}{
		{"KDE", []string{"xdg-terminal-exec", "konsole"}, api.TerminalCommand{"xdg-terminal-exec"}},
		{"KDE", []string{"gnome-terminal", "konsole"}, api.TerminalCommand{"konsole", "-e"}},
		{"GNOME", []string{"gnome-terminal", "konsole"}, api.TerminalCommand{"gnome-terminal", "--"}},
		{"XFCE", []string{"x-terminal-emulator"}, api.TerminalCommand{"x-terminal-emulator", "-e"}},
		{"XFCE", nil, nil},
	}
	for _, c := range cases {
		got, ok := pickTerminal(c.desktop, haveOnly(c.have...))
		if !reflect.DeepEqual(got, c.want) || ok != (c.want != nil) {
			t.Errorf("%s with %v: got (%q, %v), want %q", c.desktop, c.have, got, ok, c.want)
		}
	}
}

func TestLinuxOSName(t *testing.T) {
	cases := map[string]string{
		"NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 26.04 LTS\"\nID=ubuntu\n": "Ubuntu 26.04 LTS",
		"PRETTY_NAME='Arch Linux'\n":                                     "Arch Linux",
		"PRETTY_NAME=\"\"\n":                                             "Linux",
		"":                                                               "Linux",
	}
	for in, want := range cases {
		if got := linuxOSName(in); got != want {
			t.Errorf("linuxOSName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Linux has autostart and sleep prevention, never wake (it needs root), no
// macOS file-access prompts, and no self-update yet.
func TestLinuxPlatformServices(t *testing.T) {
	p := newPlatform(system.Real{})
	if p.service == nil || p.sleep == nil || p.notifyStatus == nil || p.osVersion == nil {
		t.Fatalf("platform = %+v, want service, sleep, notifyStatus and osVersion", p)
	}
	if p.waker != nil || p.warmFileAccess != nil || p.uninstall != nil || p.requestNotifyAuth != nil || p.selfUpdate {
		t.Fatalf("platform = %+v, want no wake, file-access warming, uninstaller, notify auth or self-update", p)
	}
}
