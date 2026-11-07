//go:build windows

package webview2

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/windows/registry"
)

// SystemRegistry reads this PC's registry. It only reads.
type SystemRegistry struct{}

// String reads a REG_SZ or REG_EXPAND_SZ value (unexpanded) in the given view.
func (SystemRegistry) String(hive Hive, view View, path, name string) (string, bool) {
	root := registry.LOCAL_MACHINE
	if hive == User {
		root = registry.CURRENT_USER
	}
	access := uint32(registry.QUERY_VALUE | registry.WOW64_32KEY)
	if view == View64 {
		access = registry.QUERY_VALUE | registry.WOW64_64KEY
	}
	k, err := registry.OpenKey(root, path, access)
	if err != nil {
		return "", false
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return "", errors.Is(err, registry.ErrUnexpectedType)
	}
	return v, true
}

// ProbeSystem probes this PC for a window built for the running architecture
// (the installer ships the daemon and the window for the same one).
func ProbeSystem() Status {
	return Probe(SystemRegistry{}, func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && !info.IsDir()
	}, Arch(runtime.GOARCH))
}

// SystemSanitizer shortens paths under the user's profile folders.
func SystemSanitizer() func(string) string {
	return Sanitizer(map[string]string{
		"%LOCALAPPDATA%": os.Getenv("LOCALAPPDATA"),
		"%APPDATA%":      os.Getenv("APPDATA"),
		"%USERPROFILE%":  os.Getenv("USERPROFILE"),
	})
}
