package app

import (
	"errors"
	"testing"

	"github.com/danielmaier42/claudeq/internal/store"
	"github.com/danielmaier42/claudeq/internal/task"
)

func folderConfig() store.Config {
	wsl := func(d string) *store.ProviderExecution { return &store.ProviderExecution{Type: "wsl", Distro: d} }
	return store.Config{
		Settings: store.Settings{DefaultProvider: "win"},
		Providers: []store.Provider{
			{ID: "win", Kind: "claude-code", Enabled: true},
			{ID: "ubuntu", Kind: "claude-code", Enabled: true, Execution: wsl("Ubuntu")},
			{ID: "debian", Kind: "claude-code", Enabled: true, Execution: wsl("Debian")},
		},
		Pools: []store.Pool{
			{ID: "mixed", Members: []store.PoolMember{{Provider: "win"}, {Provider: "ubuntu"}}},
			{ID: "two-linux", Members: []store.PoolMember{{Provider: "ubuntu"}, {Provider: "debian"}}},
		},
	}
}

func TestPlaceFolderOnWindows(t *testing.T) {
	defer func(g string) { hostGOOS = g }(hostGOOS)
	hostGOOS = "windows"
	cfg := folderConfig()
	cases := []struct {
		tk   task.Task
		want string // "" = refused
	}{
		{task.Task{Provider: "ubuntu", WorkingDir: "/home/dm/repo"}, `\\wsl.localhost\Ubuntu\home\dm\repo`},
		{task.Task{Pool: "mixed", WorkingDir: "/home/dm/repo"}, `\\wsl.localhost\Ubuntu\home\dm\repo`},
		{task.Task{Pool: "two-linux", WorkingDir: "/home/dm/repo"}, ""},
		{task.Task{WorkingDir: "/home/dm/repo"}, ""}, // the default is native
		{task.Task{Provider: "ubuntu", WorkingDir: `C:\repo`}, `C:\repo`},
		{task.Task{Provider: "ubuntu", WorkingDir: `\\wsl.localhost\Debian\x`}, ""},
		{task.Task{Pool: "two-linux", WorkingDir: `\\wsl.localhost\Debian\x`}, `\\wsl.localhost\Debian\x`},
		{task.Task{Provider: "win", WorkingDir: `\\wsl.localhost\Debian\x`}, `\\wsl.localhost\Debian\x`},
		{task.Task{Kind: task.KindScript, WorkingDir: "/home/dm"}, ""},
	}
	for _, c := range cases {
		tk := c.tk
		err := placeFolder(cfg, &tk)
		switch {
		case c.want == "" && !errors.Is(err, ErrFolderUnreachable):
			t.Errorf("%+v: err = %v, want refused", c.tk, err)
		case c.want != "" && (err != nil || tk.WorkingDir != c.want):
			t.Errorf("%+v: got %q, %v; want %q", c.tk, tk.WorkingDir, err, c.want)
		}
	}
}

// On macOS every folder passes unchanged, as before environments existed.
func TestPlaceFolderOnMacOSChangesNothing(t *testing.T) {
	defer func(g string) { hostGOOS = g }(hostGOOS)
	hostGOOS = "darwin"
	tk := task.Task{WorkingDir: "/Users/dm/repo"}
	if err := placeFolder(folderConfig(), &tk); err != nil || tk.WorkingDir != "/Users/dm/repo" {
		t.Fatalf("got %q, %v", tk.WorkingDir, err)
	}
}
