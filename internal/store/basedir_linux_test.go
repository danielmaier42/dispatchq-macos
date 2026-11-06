package store

import (
	"path/filepath"
	"testing"
)

func TestDataBaseDirFollowsXDG(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	cases := []struct{ xdg, want string }{
		{"/data", "/data"},
		{"", filepath.Join("/home/me", ".local", "share")},
		// The XDG spec says to ignore a relative path.
		{"relative/dir", filepath.Join("/home/me", ".local", "share")},
	}
	for _, c := range cases {
		t.Setenv("XDG_DATA_HOME", c.xdg)
		if got, err := dataBaseDir(); err != nil || got != c.want {
			t.Errorf("XDG_DATA_HOME=%q: got (%q, %v), want %q", c.xdg, got, err, c.want)
		}
	}
}
