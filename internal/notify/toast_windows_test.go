//go:build windows

package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shortcutProps is what a shortcut carries that registration may touch.
type shortcutProps struct {
	appID, target, icon string
	activator           windows.GUID
}

// readShortcut reads a shortcut's identity, target and icon.
func readShortcut(t *testing.T, lnk string) shortcutProps {
	t.Helper()
	var sp shortcutProps
	err := withShellLink(func(s shellLink) error {
		p, err := windows.UTF16PtrFromString(lnk)
		if err != nil {
			return err
		}
		if err := s.file.call(slotPersistLoad, uintptr(unsafe.Pointer(p)), 0); err != nil { // STGM_READ
			return err
		}
		if sp.appID, err = readAppID(s.store); err != nil {
			return err
		}
		if sp.activator, err = readActivator(s.store); err != nil {
			return err
		}
		buf := make([]uint16, windows.MAX_PATH)
		if err := s.link.call(3, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); err != nil { // IShellLinkW::GetPath
			return err
		}
		sp.target = windows.UTF16ToString(buf)
		var idx int32
		if err := s.link.call(16, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&idx))); err != nil { // IShellLinkW::GetIconLocation
			return err
		}
		sp.icon = windows.UTF16ToString(buf)
		return nil
	})
	if err != nil {
		t.Fatalf("read %s: %v", lnk, err)
	}
	return sp
}

func wantActivator(t *testing.T) windows.GUID {
	t.Helper()
	g, err := windows.GUIDFromString(WindowsToastActivatorCLSID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A missing shortcut is created for the window app, with the ID and stub
// activator; a second run leaves it untouched; a shortcut without them (what
// NSIS writes) gets both and keeps its target and icon. Paths with spaces and
// non-ASCII characters work.
func TestRegisterWindowsAppShortcut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Prögrams with spaces")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lnk := filepath.Join(dir, "DispatchQ", "DispatchQ.lnk")
	if err := RegisterWindowsApp(lnk, WindowsAppID, WindowsToastActivatorCLSID, exe, filepath.Join(dir, "missing.ico")); err != nil {
		t.Fatalf("create: %v", err)
	}
	act := wantActivator(t)
	if got := readShortcut(t, lnk); got.appID != WindowsAppID || got.activator != act || !strings.EqualFold(got.target, exe) {
		t.Fatalf("created shortcut = %+v, want ID %q, activator %v, target %q", got, WindowsAppID, act, exe)
	}
	before, err := os.Stat(lnk)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterWindowsApp(lnk, WindowsAppID, WindowsToastActivatorCLSID, exe, ""); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if after, _ := os.Stat(lnk); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a shortcut that already carries the ID was rewritten")
	}

	// Rewrite it as a plain shortcut without the ID, as the installer does,
	// with an icon that must survive the repair.
	icon := filepath.Join(dir, "dispatchq.ico")
	if err := os.WriteFile(icon, []byte{0, 0, 1, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lnk); err != nil {
		t.Fatal(err)
	}
	if err := withShellLink(func(s shellLink) error {
		if err := s.setTarget(exe, icon); err != nil {
			return err
		}
		p, _ := windows.UTF16PtrFromString(lnk)
		return s.file.call(slotPersistSave, uintptr(unsafe.Pointer(p)), 1)
	}); err != nil {
		t.Fatal(err)
	}
	if got := readShortcut(t, lnk); got.appID != "" || got.activator != (windows.GUID{}) {
		t.Fatalf("plain shortcut carries %+v", got)
	}
	if err := RegisterWindowsApp(lnk, WindowsAppID, WindowsToastActivatorCLSID, filepath.Join(dir, "other.exe"), ""); err != nil {
		t.Fatalf("repair: %v", err)
	}
	got := readShortcut(t, lnk)
	if got.appID != WindowsAppID || got.activator != act {
		t.Fatalf("repaired shortcut = %+v, want ID %q, activator %v", got, WindowsAppID, act)
	}
	if !strings.EqualFold(got.target, exe) || !strings.EqualFold(got.icon, icon) {
		t.Fatalf("repair changed target/icon: %+v, want %q / %q", got, exe, icon)
	}
}

// Without the shortcut and without the window app to point a new one at, the
// error says what to do instead of creating a broken shortcut.
func TestRegisterWindowsAppMissingApp(t *testing.T) {
	dir := t.TempDir()
	lnk := filepath.Join(dir, "DispatchQ", "DispatchQ.lnk")
	err := RegisterWindowsApp(lnk, WindowsAppID, WindowsToastActivatorCLSID, filepath.Join(dir, "dispatchqapp.exe"), "")
	if err == nil || !strings.Contains(err.Error(), "reinstall DispatchQ") {
		t.Fatalf("err = %v, want a reinstall hint", err)
	}
	if _, err := os.Stat(lnk); !os.IsNotExist(err) {
		t.Fatalf("a shortcut was created: %v", err)
	}
}

// The shortcut lives where the per-user installer puts it.
func TestWindowsShortcutPath(t *testing.T) {
	p, err := WindowsShortcutPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, `\Start Menu\Programs\DispatchQ\DispatchQ.lnk`) {
		t.Fatalf("shortcut path = %q", p)
	}
}

// The stub activator is a valid, fixed CLSID.
func TestWindowsToastActivatorCLSID(t *testing.T) {
	if wantActivator(t) == (windows.GUID{}) {
		t.Fatal("zero CLSID")
	}
}

// PROPVARIANT is 24 bytes with its union at offset 8 on amd64 and arm64.
func TestPropVariantLayout(t *testing.T) {
	var v propVariant
	if unsafe.Sizeof(v) != 24 || unsafe.Offsetof(v.val) != 8 {
		t.Fatalf("PROPVARIANT size %d, value offset %d", unsafe.Sizeof(v), unsafe.Offsetof(v.val))
	}
	if unsafe.Sizeof(propertyKey{}) != 20 {
		t.Fatalf("PROPERTYKEY size %d", unsafe.Sizeof(propertyKey{}))
	}
}

func TestSetProcessAppID(t *testing.T) {
	if err := SetProcessAppID(WindowsAppID); err != nil {
		t.Fatal(err)
	}
}
