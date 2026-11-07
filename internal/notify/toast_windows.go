//go:build windows

package notify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A desktop app can raise a toast only when a shortcut in the Start menu's
// Programs folder carries its AppUserModelID in the System.AppUserModel.ID
// property ("How to enable desktop toast notifications through an
// AppUserModelID", Microsoft Learn). For the notification center to keep the
// toasts, the shortcut also needs System.AppUserModel.ToastActivatorCLSID; a
// stub CLSID without a COM server is Microsoft's documented no-COM option and
// limits clicks to protocol activation (see WindowsToastActivatorCLSID). The
// installer creates the per-user shortcut; NSIS cannot set these properties,
// so RegisterWindowsApp adds them, and recreates the shortcut when the
// operator deleted it.

// WindowsShortcutPath is the per-user Start menu shortcut the installer
// creates ($SMPROGRAMS\DispatchQ\DispatchQ.lnk for a per-user install).
func WindowsShortcutPath() (string, error) {
	programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	if err != nil {
		return "", fmt.Errorf("locate the Start menu: %w", err)
	}
	return filepath.Join(programs, "DispatchQ", "DispatchQ.lnk"), nil
}

// RegisterWindowsApp makes sure the Start menu shortcut at lnk carries appID
// and the toast activator CLSID activator ("{...}" form). An existing shortcut
// keeps its target and icon and is only written when a property differs. A
// missing one is created for appExe with icon, when appExe exists. It needs no
// administrator rights and writes no registry: the shortcut is the user's own.
func RegisterWindowsApp(lnk, appID, activator, appExe, icon string) error {
	clsid, err := windows.GUIDFromString(activator)
	if err != nil {
		return fmt.Errorf("toast activator CLSID %q: %w", activator, err)
	}
	_, statErr := os.Stat(lnk)
	exists := statErr == nil
	if !exists {
		if _, err := os.Stat(appExe); err != nil {
			return fmt.Errorf("the Start menu shortcut %s is missing and %s was not found to recreate it; reinstall DispatchQ", lnk, appExe)
		}
		if err := os.MkdirAll(filepath.Dir(lnk), 0o755); err != nil {
			return fmt.Errorf("create the Start menu folder: %w", err)
		}
	}
	return withShellLink(func(s shellLink) error {
		lnkPtr, err := windows.UTF16PtrFromString(lnk)
		if err != nil {
			return err
		}
		if exists {
			if err := s.file.call(slotPersistLoad, uintptr(unsafe.Pointer(lnkPtr)), stgmReadWrite); err != nil {
				return fmt.Errorf("read %s: %w", lnk, err)
			}
			id, idErr := readAppID(s.store)
			cur, clsidErr := readActivator(s.store)
			if idErr == nil && clsidErr == nil && id == appID && cur == clsid {
				return nil
			}
		} else if err := s.setTarget(appExe, icon); err != nil {
			return err
		}
		if err := writeIdentity(s.store, appID, clsid); err != nil {
			return err
		}
		if err := s.file.call(slotPersistSave, uintptr(unsafe.Pointer(lnkPtr)), 1); err != nil {
			return fmt.Errorf("write %s: %w", lnk, err)
		}
		return nil
	})
}

// shellLink is one ShellLink object with the two other interfaces used here.
type shellLink struct {
	link, file, store comObject
}

// withShellLink runs fn with a fresh ShellLink object on a COM-initialized,
// locked thread and releases everything afterwards.
func withShellLink(fn func(shellLink) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); {
	case err == nil, errors.Is(err, syscall.Errno(1)): // S_FALSE: already initialized on this thread
		defer windows.CoUninitialize()
	case errors.Is(err, syscall.Errno(windows.RPC_E_CHANGED_MODE)):
		// This thread runs a multithreaded apartment, where the shell link
		// object works too; that initialization is not ours to undo.
	default:
		return fmt.Errorf("initialize COM: %w", err)
	}
	var p unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 {
		return fmt.Errorf("create shell link: %w", syscall.Errno(hr))
	}
	s := shellLink{link: comObject{p}}
	defer s.link.release()
	var err error
	if s.file, err = s.link.query(&iidIPersistFile); err != nil {
		return fmt.Errorf("shell link IPersistFile: %w", err)
	}
	defer s.file.release()
	if s.store, err = s.link.query(&iidIPropertyStore); err != nil {
		return fmt.Errorf("shell link IPropertyStore: %w", err)
	}
	defer s.store.release()
	return fn(s)
}

// setTarget points a new shortcut at exe, with icon when that file exists.
func (s shellLink) setTarget(exe, icon string) error {
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	if err := s.link.call(slotShellLinkSetPath, uintptr(unsafe.Pointer(exePtr))); err != nil {
		return fmt.Errorf("shortcut target: %w", err)
	}
	if _, err := os.Stat(icon); err != nil {
		return nil
	}
	iconPtr, err := windows.UTF16PtrFromString(icon)
	if err != nil {
		return err
	}
	if err := s.link.call(slotShellLinkSetIconLocation, uintptr(unsafe.Pointer(iconPtr)), 0); err != nil {
		return fmt.Errorf("shortcut icon: %w", err)
	}
	return nil
}

var (
	ole32                     = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance      = ole32.NewProc("CoCreateInstance")
	procPropVariantClear      = ole32.NewProc("PropVariantClear")
	clsidShellLink            = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW            = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile           = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPropertyStore         = windows.GUID{Data1: 0x886D8EEB, Data2: 0x8CF2, Data3: 0x4446, Data4: [8]byte{0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99}}
	appUserModelFMTID         = windows.GUID{Data1: 0x9F4C2855, Data2: 0x9F79, Data3: 0x4B39, Data4: [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
	pkeyAppUserModelID        = propertyKey{fmtid: appUserModelFMTID, pid: 5}  // System.AppUserModel.ID, VT_LPWSTR
	pkeyToastActivatorCLSID   = propertyKey{fmtid: appUserModelFMTID, pid: 26} // System.AppUserModel.ToastActivatorCLSID, VT_CLSID
	errUnexpectedPropertyType = errors.New("unexpected property type on the shortcut")
)

// Vtable slots (0-2 are IUnknown's QueryInterface, AddRef and Release).
const (
	slotQueryInterface           = 0
	slotRelease                  = 2
	slotShellLinkSetIconLocation = 17 // IShellLinkW::SetIconLocation
	slotShellLinkSetPath         = 20 // IShellLinkW::SetPath
	slotPersistLoad              = 5  // IPersistFile::Load
	slotPersistSave              = 6  // IPersistFile::Save
	slotPropertyGetValue         = 5  // IPropertyStore::GetValue
	slotPropertySetValue         = 6  // IPropertyStore::SetValue
	slotPropertyCommit           = 7  // IPropertyStore::Commit
)

const (
	stgmReadWrite      = 0x2
	clsctxInprocServer = 0x1
	vtEmpty            = 0
	vtLPWSTR           = 31
	vtCLSID            = 72
)

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT: a 2-byte type, 6 reserved bytes, then a
// 16-byte union (24 bytes, union at offset 8, on amd64 and arm64). Only the
// pointer members are used here: pwszVal (VT_LPWSTR) and puuid (VT_CLSID).
type propVariant struct {
	vt       uint16
	reserved [3]uint16
	val      unsafe.Pointer
	pad      uintptr
}

// comObject is a raw COM interface pointer.
type comObject struct{ p unsafe.Pointer }

func (o comObject) method(slot int) uintptr {
	vtbl := *(*unsafe.Pointer)(o.p)
	return *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
}

func (o comObject) call(slot int, args ...uintptr) error {
	hr, _, _ := syscall.SyscallN(o.method(slot), append([]uintptr{uintptr(o.p)}, args...)...)
	if int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	return nil
}

func (o comObject) query(iid *windows.GUID) (comObject, error) {
	var p unsafe.Pointer
	if err := o.call(slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p))); err != nil {
		return comObject{}, err
	}
	return comObject{p}, nil
}

func (o comObject) release() {
	if o.p != nil {
		_, _, _ = syscall.SyscallN(o.method(slotRelease), uintptr(o.p))
	}
}

// readAppID is IPropertyStore::GetValue(PKEY_AppUserModel_ID).
func readAppID(store comObject) (string, error) {
	var v propVariant
	if err := store.call(slotPropertyGetValue, uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&v))); err != nil {
		return "", fmt.Errorf("read the shortcut's AppUserModelID: %w", err)
	}
	defer func() { _, _, _ = procPropVariantClear.Call(uintptr(unsafe.Pointer(&v))) }()
	switch v.vt {
	case vtEmpty:
		return "", nil
	case vtLPWSTR:
		return windows.UTF16PtrToString((*uint16)(v.val)), nil
	default:
		return "", errUnexpectedPropertyType
	}
}

// readActivator is IPropertyStore::GetValue(PKEY_AppUserModel_ToastActivatorCLSID);
// the zero GUID when the shortcut has none.
func readActivator(store comObject) (windows.GUID, error) {
	var v propVariant
	if err := store.call(slotPropertyGetValue, uintptr(unsafe.Pointer(&pkeyToastActivatorCLSID)), uintptr(unsafe.Pointer(&v))); err != nil {
		return windows.GUID{}, fmt.Errorf("read the shortcut's ToastActivatorCLSID: %w", err)
	}
	defer func() { _, _, _ = procPropVariantClear.Call(uintptr(unsafe.Pointer(&v))) }()
	switch v.vt {
	case vtEmpty:
		return windows.GUID{}, nil
	case vtCLSID:
		return *(*windows.GUID)(v.val), nil
	default:
		return windows.GUID{}, errUnexpectedPropertyType
	}
}

// writeIdentity sets System.AppUserModel.ID and ToastActivatorCLSID through
// IPropertyStore::SetValue, then Commit. The store copies each value, so the
// string and GUID stay owned by Go for the calls.
func writeIdentity(store comObject, appID string, clsid windows.GUID) error {
	p, err := windows.UTF16PtrFromString(appID)
	if err != nil {
		return err
	}
	id := propVariant{vt: vtLPWSTR, val: unsafe.Pointer(p)}
	if err := store.call(slotPropertySetValue, uintptr(unsafe.Pointer(&pkeyAppUserModelID)), uintptr(unsafe.Pointer(&id))); err != nil {
		return fmt.Errorf("set the shortcut's AppUserModelID: %w", err)
	}
	act := propVariant{vt: vtCLSID, val: unsafe.Pointer(&clsid)}
	if err := store.call(slotPropertySetValue, uintptr(unsafe.Pointer(&pkeyToastActivatorCLSID)), uintptr(unsafe.Pointer(&act))); err != nil {
		return fmt.Errorf("set the shortcut's ToastActivatorCLSID: %w", err)
	}
	runtime.KeepAlive(p)
	runtime.KeepAlive(&clsid)
	if err := store.call(slotPropertyCommit); err != nil {
		return fmt.Errorf("commit the shortcut's properties: %w", err)
	}
	return nil
}

// SetProcessAppID gives the calling process appID, so its windows group with
// the Start menu shortcut that carries the same ID.
func SetProcessAppID(appID string) error {
	p, err := windows.UTF16PtrFromString(appID)
	if err != nil {
		return err
	}
	hr, _, _ := windows.NewLazySystemDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID").Call(uintptr(unsafe.Pointer(p)))
	if int32(hr) < 0 {
		return fmt.Errorf("SetCurrentProcessExplicitAppUserModelID: %w", syscall.Errno(hr))
	}
	return nil
}
