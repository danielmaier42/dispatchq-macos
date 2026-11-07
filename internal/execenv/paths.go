// Package execenv runs provider instances inside WSL from DispatchQ for
// Windows: the daemon side of the helper protocol (internal/wslproto), the
// discovery questions the provider dialog asks, and the translation between
// the folders a task stores and the paths Linux uses.
//
// Nothing here runs for a native instance: provider.Registry.HostFor returns
// the native host for those without consulting this package, and the daemon
// installs a Service only in the Windows build.
package execenv

import (
	"errors"
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// A task stores its folder in a form Windows can show and open: a drive path
// (C:\repo) or a WSL share (\\wsl.localhost\Ubuntu\home\dm\repo). A WSL run
// translates it to the Linux path at start; a native run uses it as it is.

// uncPrefixes are the two names Windows gives the WSL share.
var uncPrefixes = []string{`\\wsl.localhost\`, `\\wsl$\`}

// splitWSLShare returns the distribution and the Linux path of a WSL share
// path, with ok false for anything else.
func splitWSLShare(p string) (distro, linux string, ok bool) {
	q := strings.ReplaceAll(p, "/", `\`)
	for _, pre := range uncPrefixes {
		if len(q) >= len(pre) && strings.EqualFold(q[:len(pre)], pre) {
			rest := q[len(pre):]
			distro, tail, _ := strings.Cut(rest, `\`)
			if distro == "" {
				return "", "", false
			}
			return distro, "/" + strings.ReplaceAll(strings.Trim(tail, `\`), `\`, "/"), true
		}
	}
	return "", "", false
}

// drivePath reports a Windows drive path ("C:\x", "C:/x") and its parts.
func drivePath(p string) (letter byte, rest string, ok bool) {
	if len(p) < 2 || p[1] != ':' {
		return 0, "", false
	}
	c := p[0] | 0x20
	if c < 'a' || c > 'z' {
		return 0, "", false
	}
	if len(p) > 2 && p[2] != '\\' && p[2] != '/' {
		return 0, "", false
	}
	return c, strings.Trim(strings.ReplaceAll(p[2:], `\`, "/"), "/"), true
}

func isUNC(p string) bool { return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") }

// IsLinuxPath reports an absolute Linux path.
func IsLinuxPath(p string) bool { return strings.HasPrefix(p, "/") && !isUNC(p) }

// ErrUnreachable is a folder an environment cannot reach.
var ErrUnreachable = errors.New("can't reach this task's folder")

// CanReach reports whether an instance can run in the stored folder, without
// starting anything. A native instance keeps every folder it accepted before,
// except that on Windows a Linux path is not a folder Windows can run in.
func CanReach(ex provider.Execution, stored, goos string) error {
	if ex.IsNative() {
		if goos == "windows" && IsLinuxPath(stored) {
			return fmt.Errorf("%w: %s is a Linux path, and this provider runs in Windows", ErrUnreachable, stored)
		}
		return nil
	}
	if ex.Type != provider.ExecWSL {
		return fmt.Errorf("%w: unknown environment %q", ErrUnreachable, ex.Type)
	}
	switch {
	case stored == "":
		return nil
	case IsLinuxPath(stored):
		return nil
	}
	if d, _, ok := splitWSLShare(stored); ok {
		if !strings.EqualFold(d, ex.Distro) {
			return fmt.Errorf("%w: the folder is inside the Linux system %s, and this provider runs in %s", ErrUnreachable, d, ex.Distro)
		}
		return nil
	}
	if isUNC(stored) {
		return fmt.Errorf("%w: %s is a network folder, which Linux (WSL) cannot open by that name", ErrUnreachable, stored)
	}
	if _, _, ok := drivePath(stored); ok {
		return nil
	}
	return fmt.Errorf("%w: %s is not an absolute path", ErrUnreachable, stored)
}

// ToLinux turns a stored folder into the path a WSL run uses. mountRoot is
// where Windows drives appear in that distribution ("/mnt/"), empty when
// drive mounting is off.
func ToLinux(ex provider.Execution, stored, mountRoot string) (string, error) {
	if err := CanReach(ex, stored, "windows"); err != nil {
		return "", err
	}
	if stored == "" || IsLinuxPath(stored) {
		return stored, nil
	}
	if _, linux, ok := splitWSLShare(stored); ok {
		return linux, nil
	}
	letter, rest, _ := drivePath(stored)
	if mountRoot == "" {
		return "", fmt.Errorf("%w: Windows drives are not mounted in %s ([automount] is off), so %s cannot be opened there", ErrUnreachable, ex.Distro, stored)
	}
	p := strings.TrimSuffix(mountRoot, "/") + "/" + string(letter)
	if rest != "" {
		p += "/" + rest
	}
	return p, nil
}

// FromLinux turns a Linux path seen inside distro into the form a task
// stores: a drive path under the mount root, the WSL share otherwise.
func FromLinux(linux, distro, mountRoot string) string {
	if root := strings.TrimSuffix(mountRoot, "/") + "/"; mountRoot != "" && strings.HasPrefix(linux, root) {
		rest := linux[len(root):]
		letter, tail, _ := strings.Cut(rest, "/")
		if len(letter) == 1 && letter[0]|0x20 >= 'a' && letter[0]|0x20 <= 'z' {
			return strings.ToUpper(letter) + `:\` + strings.ReplaceAll(tail, "/", `\`)
		}
	}
	return `\\wsl.localhost\` + distro + strings.ReplaceAll(linux, "/", `\`)
}
