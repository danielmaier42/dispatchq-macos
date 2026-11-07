package execenv

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/danielmaier42/claudeq/internal/wslproto"
)

// frameConn is the daemon's end of a run's control channel: wsl.exe's
// standard input, carried through to the helper. Every write is one whole
// frame, so program input, cancel and pings never interleave.
type frameConn struct {
	mu sync.Mutex
	w  io.WriteCloser
	// sent is closed once the header is written (or failed); no other frame
	// may go first.
	sent chan struct{}
	err  error

	interruptOnce sync.Once
}

func newFrameConn(w io.WriteCloser) *frameConn {
	return &frameConn{w: w, sent: make(chan struct{})}
}

// errChannelClosed is a frame after the channel broke or closed.
var errChannelClosed = errors.New("the helper's channel is closed")

func (c *frameConn) write(typ byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if err := wslproto.WriteFrame(c.w, typ, payload); err != nil {
		c.err = err
		return err
	}
	return nil
}

// send writes a frame after the header.
func (c *frameConn) send(typ byte, payload []byte) error {
	<-c.sent
	return c.write(typ, payload)
}

// sendData writes program input in data frames.
func (c *frameConn) sendData(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), wslproto.DataChunk)
		if err := c.send(wslproto.FrameData, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// serve writes the header and, for a run that is not a conversation, its
// whole input and the end of it. It then pings until the helper's side of
// the channel is gone or ctx ends. A ping that cannot be written means
// wsl.exe has exited; nothing else is lost by stopping then.
func (c *frameConn) serve(ctx context.Context, header []byte, stdin string, stream bool) {
	err := c.write(wslproto.FrameHeader, header)
	close(c.sent)
	if err != nil {
		return
	}
	if !stream {
		if c.sendData([]byte(stdin)) != nil || c.send(wslproto.FrameEnd, nil) != nil {
			return
		}
	}
	t := time.NewTicker(leaseInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.send(wslproto.FramePing, nil) != nil {
				return
			}
		}
	}
}

// interrupt asks the helper to stop the program's whole Linux process tree
// and waits, bounded, until wsl.exe is gone: the helper exits once the tree is
// down, and from then on a frame can no longer be written. The caller's hard
// stop follows either way. It never waits on a write that is stuck behind a
// full pipe for longer than interruptWait.
func (c *frameConn) interrupt() {
	c.interruptOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-c.sent:
			case <-time.After(interruptWait):
				return
			}
			if c.write(wslproto.FrameCancel, nil) != nil {
				return
			}
			deadline := time.Now().Add(interruptWait)
			for time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				if c.write(wslproto.FramePing, nil) != nil {
					return
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(interruptWait + time.Second):
		}
	})
}

// frameStdin is a conversation's standard input as data frames. Closing it
// ends the program's input; the channel itself stays open for pings and a
// cancel until wsl.exe exits.
type frameStdin struct {
	conn   *frameConn
	closed sync.Once
}

func (s *frameStdin) Write(p []byte) (int, error) {
	if err := s.conn.sendData(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *frameStdin) Close() error {
	err := errChannelClosed
	s.closed.Do(func() { err = s.conn.send(wslproto.FrameEnd, nil) })
	return err
}
