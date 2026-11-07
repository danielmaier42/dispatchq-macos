package webview2

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RequiredExport is the function the window's loader calls in the client
// library (webview.h webview2_symbols).
const RequiredExport = "CreateWebViewEnvironmentWithOptionsInternal"

// LoadCheckTimeout bounds loading the client library in the separate check
// process: a library whose initialisation hangs is reported, not waited for.
const LoadCheckTimeout = 20 * time.Second

// ExitClientRefused is the exit code of the load-check process when Windows
// cannot load the library or it lacks RequiredExport; the reason is its
// output. Any other non-zero exit is a failed check.
const ExitClientRefused = 3

// machineFor is the PE machine type of the client a window built for arch
// loads natively.
var machineFor = map[string]uint16{"x64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64, "x86": pe.IMAGE_FILE_MACHINE_I386}

var machineName = map[uint16]string{pe.IMAGE_FILE_MACHINE_AMD64: "x64", pe.IMAGE_FILE_MACHINE_ARM64: "ARM64", pe.IMAGE_FILE_MACHINE_I386: "x86"}

// Inspection is what the client library's headers say.
type Inspection struct {
	Machine     uint16
	WantMachine uint16
	HasExport   bool
}

// Mismatch names what in the headers does not fit a window of the wanted
// architecture, or "" when nothing does. Windows has the last word: a
// hybrid (ARM64X) image can load in a process its main header does not name.
func (in Inspection) Mismatch() string {
	var p []string
	if in.WantMachine != 0 && in.Machine != in.WantMachine {
		p = append(p, fmt.Sprintf("it is built for %s, the window is %s", nameOf(in.Machine), nameOf(in.WantMachine)))
	}
	if !in.HasExport {
		p = append(p, "it does not export "+RequiredExport)
	}
	return strings.Join(p, "; ")
}

func nameOf(m uint16) string {
	if n, ok := machineName[m]; ok {
		return n
	}
	return fmt.Sprintf("machine 0x%04x", m)
}

// InspectClient reads the client library's PE headers without loading it.
// The error is set for a file that cannot be a loadable library at all:
// missing, a folder, empty, not a PE image, damaged headers, or no DLL.
func InspectClient(path, arch string) (Inspection, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Inspection{}, errors.New("the file does not exist")
		}
		return Inspection{}, fmt.Errorf("the file cannot be opened: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	switch {
	case err != nil:
		return Inspection{}, fmt.Errorf("the file cannot be read: %w", err)
	case info.IsDir():
		return Inspection{}, errors.New("it is a folder")
	case info.Size() == 0:
		return Inspection{}, errors.New("the file is empty")
	}
	img, err := pe.NewFile(f)
	if err != nil {
		return Inspection{}, fmt.Errorf("the file is not a valid Windows library (%w)", err)
	}
	defer func() { _ = img.Close() }()
	if img.Characteristics&pe.IMAGE_FILE_DLL == 0 {
		return Inspection{}, errors.New("the file is not a library (DLL)")
	}
	in := Inspection{Machine: img.Machine, WantMachine: machineFor[arch]}
	names, err := exportNames(img)
	if err != nil {
		return Inspection{}, fmt.Errorf("the library's export table is damaged (%w)", err)
	}
	for _, n := range names {
		if n == RequiredExport {
			in.HasExport = true
		}
	}
	return in, nil
}

// exportNames lists the names in the image's export directory.
func exportNames(img *pe.File) ([]string, error) {
	var dir pe.DataDirectory
	switch oh := img.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if oh.NumberOfRvaAndSizes > pe.IMAGE_DIRECTORY_ENTRY_EXPORT {
			dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
		}
	case *pe.OptionalHeader32:
		if oh.NumberOfRvaAndSizes > pe.IMAGE_DIRECTORY_ENTRY_EXPORT {
			dir = oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_EXPORT]
		}
	default:
		return nil, errors.New("no optional header")
	}
	if dir.VirtualAddress == 0 || dir.Size == 0 {
		return nil, nil
	}
	// read returns n bytes at rva, or with partial up to n bytes that end
	// with the section.
	read := func(rva, n uint32, partial bool) ([]byte, error) {
		for _, s := range img.Sections {
			end := uint64(s.VirtualAddress) + uint64(s.Size)
			if rva < s.VirtualAddress || uint64(rva) >= end {
				continue
			}
			if uint64(rva)+uint64(n) > end {
				if !partial {
					break
				}
				n = uint32(end - uint64(rva))
			}
			b := make([]byte, n)
			if _, err := s.ReadAt(b, int64(rva-s.VirtualAddress)); err != nil {
				return nil, err
			}
			return b, nil
		}
		return nil, fmt.Errorf("address 0x%x outside the file", rva)
	}
	hdr, err := read(dir.VirtualAddress, 40, false)
	if err != nil {
		return nil, err
	}
	count := binary.LittleEndian.Uint32(hdr[24:])
	table := binary.LittleEndian.Uint32(hdr[32:])
	if count > 1<<16 {
		return nil, fmt.Errorf("%d names", count)
	}
	if count == 0 {
		return nil, nil
	}
	ptrs, err := read(table, count*4, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		rva := binary.LittleEndian.Uint32(ptrs[i*4:])
		// Only names up to RequiredExport's length matter.
		b, err := read(rva, uint32(len(RequiredExport)+1), true)
		if err != nil {
			return nil, err
		}
		if i := bytes.IndexByte(b, 0); i >= 0 {
			b = b[:i]
		}
		names = append(names, string(b))
	}
	return names, nil
}

// CheckClient answers whether a window built for arch can use the client
// library at path: nil, or why not. Files that cannot be a library are
// refused from their headers without loading them; otherwise load (Windows
// loading the library the way the window does) decides, and its refusal is
// explained with what the headers say.
func CheckClient(path, arch string, load func(path string) error) error {
	in, err := InspectClient(path, arch)
	if err != nil {
		return err
	}
	if err := load(path); err != nil {
		if m := in.Mismatch(); m != "" {
			return fmt.Errorf("%w (%s)", err, m)
		}
		return err
	}
	return nil
}

// RunLoadCheck runs the load-check process name args and waits at most
// timeout for it. Exit 0 means the library loaded and has RequiredExport;
// ExitClientRefused means it did not, with the reason as output. A process
// that does not finish in time is ended and reported, never taken as success.
func RunLoadCheck(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out := &capped{max: 2048}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	msg := strings.TrimSpace(out.String())
	var exit *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("loading it did not finish within %s", timeout)
	case err == nil:
		return nil
	case errors.As(err, &exit) && exit.ExitCode() == ExitClientRefused:
		if msg == "" {
			msg = "Windows refused to load it"
		}
		return errors.New(msg)
	case msg != "":
		return fmt.Errorf("the load check failed (%w): %s", err, msg)
	}
	return fmt.Errorf("the load check failed: %w", err)
}

// capped keeps the first max bytes written to it.
type capped struct {
	buf bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *capped) String() string { return c.buf.String() }
