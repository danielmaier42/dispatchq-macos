package execenv

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// The installer ships the Linux helper for both architectures WSL runs:
// x86-64 distributions on x64 PCs, and arm64 distributions on Windows on Arm.
// Which one a distribution needs follows the distribution, never the installer:
// an x64 installer also runs on Windows on Arm through emulation, and its WSL
// distributions are still arm64. So the daemon asks the distribution and then
// checks the file it is about to start really is a Linux program for that CPU.

// helperName is the helper's file name for a Go architecture.
func helperName(arch string) string { return "dispatchq-wsl-linux-" + arch }

// linuxArch turns `uname -m` into a Go architecture with a shipped helper.
func linuxArch(machine string) (string, bool) {
	switch machine {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64":
		return "arm64", true
	}
	return "", false
}

// elfMachines are the ELF e_machine values of the shipped architectures.
var elfMachines = map[string]uint16{"amd64": 0x3e, "arm64": 0xb7}

// CheckELF reports whether path is a 64-bit little-endian Linux executable for
// the Go architecture arch.
func CheckELF(path, arch string) error {
	want, ok := elfMachines[arch]
	if !ok {
		return fmt.Errorf("no Linux helper exists for %s", arch)
	}
	f, err := os.Open(path) //nolint:gosec // the helper shipped next to the daemon
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var h [20]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return fmt.Errorf("%s is not a Linux program: %w", path, err)
	}
	switch {
	case string(h[:4]) != "\x7fELF":
		return fmt.Errorf("%s is not a Linux program", path)
	case h[4] != 2 || h[5] != 1:
		return fmt.Errorf("%s is not a 64-bit little-endian Linux program", path)
	}
	if typ := binary.LittleEndian.Uint16(h[16:]); typ != 2 && typ != 3 {
		return fmt.Errorf("%s is not an executable", path)
	}
	if m := binary.LittleEndian.Uint16(h[18:]); m != want {
		return fmt.Errorf("%s is a Linux program for another processor (ELF machine 0x%x, %s needs 0x%x)", path, m, arch, want)
	}
	return nil
}
