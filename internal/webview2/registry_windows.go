//go:build windows

package webview2

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
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
// (the installer ships the daemon and the window for the same one). load is
// the bounded native check of the chosen client library (see RunLoadCheck).
func ProbeSystem(load func(path string) error) Status {
	arch := Arch(runtime.GOARCH)
	return Probe(SystemRegistry{}, func(p string) error { return CheckClient(p, arch, load) }, arch)
}

// LoadClient loads the client library the way the window's loader does
// (LoadLibraryW with the full path, then GetProcAddress of RequiredExport).
// It runs the library's initialisation code, so it belongs in the separate,
// time-limited check process only. Windows error dialogs are suppressed.
func LoadClient(path string) error {
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS | windows.SEM_NOOPENFILEERRORBOX)
	h, err := windows.LoadLibrary(path)
	if err != nil {
		return fmt.Errorf("the system cannot load it: %w", err)
	}
	defer func() { _ = windows.FreeLibrary(h) }()
	if _, err := windows.GetProcAddress(h, RequiredExport); err != nil {
		return fmt.Errorf("it does not provide %s: %w", RequiredExport, err)
	}
	return nil
}

// SystemSanitizer shortens paths under the user's profile folders.
func SystemSanitizer() func(string) string {
	return Sanitizer(map[string]string{
		"%LOCALAPPDATA%": os.Getenv("LOCALAPPDATA"),
		"%APPDATA%":      os.Getenv("APPDATA"),
		"%USERPROFILE%":  os.Getenv("USERPROFILE"),
	})
}
