// Package wslproto is the wire format between the Windows daemon and the
// DispatchQ helper inside a WSL distribution (cmd/dispatchq-wsl).
//
// wsl.exe is started with a fixed argument list that carries no data. All data
// — the program's arguments, its environment including secrets, its working
// directory, its standard input — travels on the helper's standard input as
// frames, so nothing of it appears on a command line, in a file or in WSLENV.
//
// Standard input is one multiplexed channel, never raw program input mixed
// with control messages: every frame is a type byte, a four-byte big-endian
// payload length and the payload. Program input therefore cannot be mistaken
// for a control message, and a control message can never reach the program.
//
//	H  header (JSON), always first and exactly once
//	D  bytes for the program's standard input
//	E  end of the program's standard input
//	C  cancel: stop the program's whole process tree
//	P  liveness ping; carries nothing
//
// The helper treats end of file, a malformed frame, a cancel, and a missing
// frame for longer than the header's lease as "stop the tree". The daemon sends
// a ping well inside the lease, so a daemon that died, or a wsl.exe that did,
// cannot leave a Linux tree running for longer than the lease.
//
// Standard output and standard error of a run are the program's own. Every
// other operation answers with one JSON [Result] on standard output.
package wslproto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version a header carries.
const Version = 1

// Frame types.
const (
	FrameHeader = 'H'
	FrameData   = 'D'
	FrameEnd    = 'E'
	FrameCancel = 'C'
	FramePing   = 'P'
)

// MaxFrame bounds one frame. A header holds the prompt as an argument, so it is
// generous; data frames are written in much smaller chunks.
const MaxFrame = 32 << 20

// DataChunk is the largest data frame a writer produces.
const DataChunk = 64 << 10

// Operations.
const (
	OpRun    = "run"    // start Argv and relay its output; exit code is its own
	OpInfo   = "info"   // who and where: user, home, architecture, mount root, interop
	OpLookup = "lookup" // resolve Path on the login PATH
	OpStat   = "stat"   // describe Path
	OpRead   = "read"   // read at most Limit bytes of Path
	OpUsers  = "users"  // the distribution's login users
	OpReap   = "reap"   // stop trees a dead owner left behind
)

// Special working directories.
const (
	// CwdHome is the user's home directory (also the empty string).
	CwdHome = ""
	// CwdAside is DispatchQ's own empty scratch directory for asides.
	CwdAside = "@aside"
)

// Header describes one operation.
type Header struct {
	V  int    `json:"v"`
	Op string `json:"op"`
	// Argv is the program and its arguments for OpRun. A program without a
	// slash is resolved on the login PATH, skipping Windows programs.
	Argv []string `json:"argv,omitempty"`
	// Env are KEY=VALUE pairs laid over the base environment, in order.
	Env []string `json:"env,omitempty"`
	// ShareWithWindows names variables a Windows program started from the run
	// (through interop) must see; they are added to WSLENV with the /w flag.
	ShareWithWindows []string `json:"share_with_windows,omitempty"`
	// LoginEnv starts from the user's login shell environment, so PATH holds
	// what the user's profile adds.
	LoginEnv bool   `json:"login_env,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	// Path is the subject of lookup, stat and read. A leading "~/" is the home.
	Path  string `json:"path,omitempty"`
	Limit int64  `json:"limit,omitempty"`
	// Owner identifies the DispatchQ installation (its data folder) that owns
	// a run, so a reap only ever touches that owner's records.
	Owner string `json:"owner,omitempty"`
	// LeaseMS is how long the helper keeps a run without hearing a frame.
	LeaseMS int `json:"lease_ms,omitempty"`
	// PipeReleaseMS is how long output is relayed after the program exits.
	PipeReleaseMS int `json:"pipe_release_ms,omitempty"`
}

// Result is the answer of every operation except run.
type Result struct {
	Error string `json:"error,omitempty"`
	// NotFound marks an error that means the path does not exist.
	NotFound bool `json:"not_found,omitempty"`

	// info
	User      string `json:"user,omitempty"`
	UID       int    `json:"uid,omitempty"`
	Home      string `json:"home,omitempty"`
	Arch      string `json:"arch,omitempty"`
	MountRoot string `json:"mount_root,omitempty"`
	Interop   bool   `json:"interop,omitempty"`
	WSL2      bool   `json:"wsl2,omitempty"`

	// lookup, stat
	Path       string `json:"path,omitempty"`
	IsDir      bool   `json:"is_dir,omitempty"`
	Executable bool   `json:"executable,omitempty"`

	// read
	Data []byte `json:"data,omitempty"`

	// users
	Users []string `json:"users,omitempty"`

	// reap
	Reaped int `json:"reaped,omitempty"`
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return fmt.Errorf("wslproto: frame of %d bytes is too large", len(payload))
	}
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload))) //nolint:gosec // bounded by MaxFrame above
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ErrMalformed is a frame stream that does not follow the protocol.
var ErrMalformed = errors.New("wslproto: malformed frame")

// ReadFrame reads one frame. io.EOF means the stream ended between frames.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, ErrMalformed
		}
		return 0, nil, err
	}
	switch hdr[0] {
	case FrameHeader, FrameData, FrameEnd, FrameCancel, FramePing:
	default:
		return 0, nil, ErrMalformed
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, ErrMalformed
	}
	if n == 0 {
		return hdr[0], nil, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, ErrMalformed
	}
	return hdr[0], buf, nil
}
