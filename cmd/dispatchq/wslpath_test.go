package main

import "testing"

func TestFromCallerTranslatesWSLPathsOnWindowsOnly(t *testing.T) {
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	t.Setenv("DISPATCHQ_WSL_MNT", "/mnt/")
	defer func(g string) { callerGOOS = g }(callerGOOS)

	callerGOOS = "windows"
	for in, want := range map[string]string{
		"/home/dm/repo/out.md": `\\wsl.localhost\Ubuntu\home\dm\repo\out.md`,
		"/mnt/c/repo":          `C:\repo`,
		`C:\repo`:              `C:\repo`,
		"out.md":               "out.md",
	} {
		if got := fromCaller(in); got != want {
			t.Errorf("fromCaller(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("WSL_DISTRO_NAME", "")
	if got := fromCaller("/home/dm"); got != "/home/dm" {
		t.Errorf("outside a WSL run: %q", got)
	}
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	callerGOOS = "darwin"
	if got := fromCaller("/Users/dm/repo"); got != "/Users/dm/repo" {
		t.Errorf("macOS path changed: %q", got)
	}
}
