//go:build unix

package codex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/procown/owntest"
	"github.com/danielmaier42/claudeq/internal/provider"
)

// These run the real native host's streaming path against a stand-in
// `codex app-server` that starts a tool of its own, as a real server's MCP
// servers and helpers are. Before the app server owned its process tree, a
// deadline killed only the server: the tool survived, and one holding the
// server's output kept the limits request reading forever.

const limitsReply = `{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":5,"windowDurationMins":300}}}}`

// appServerWith writes a stand-in app server that reads the handshake and the
// question, then runs before, then answers with reply (none if empty) and
// keeps running until its input closes.
func appServerWith(t *testing.T, before, reply string) string {
	t.Helper()
	answer := ""
	if reply != "" {
		answer = "echo '" + reply + "'"
	}
	return owntest.Script(t, "codex", `read -r init
echo '{"id":1,"result":{"userAgent":"fake"}}'
read -r initialized
read -r question
`+before+`
`+answer+`
cat > /dev/null
wait`)
}

func readLimits(ctx context.Context, t *testing.T, bin string) error {
	t.Helper()
	return owntest.Within(t, 15*time.Second, func() error {
		_, err := New().ReadLimits(ctx, provider.Instance{BinaryPath: bin}, provider.NativeHost{})
		return err
	})
}

// Cancelling the request stops the server's tree, also a tool holding its
// output, and the request returns at once.
func TestReadLimitsCancelStopsTheServersTree(t *testing.T) {
	for _, hold := range []bool{false, true} {
		name := map[bool]string{false: "own output", true: "holding the output"}[hold]
		t.Run(name, func(t *testing.T) {
			pidFile := owntest.PIDFile(t)
			bin := appServerWith(t, owntest.Spawn(pidFile, hold), "")
			ctx, cancel := owntest.CancelWhenRecorded(pidFile, 10*time.Second)
			defer cancel()
			err := readLimits(ctx, t, bin)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want the cancellation", err)
			}
			if child := owntest.PID(t, pidFile); !owntest.Gone(child) {
				t.Fatalf("tool %d survived the cancelled request", child)
			}
		})
	}
}

// Once the answer is in the server is stopped, and with it every tool it
// started: a limits reading leaves nothing running.
func TestReadLimitsAnswerStopsTheServersTree(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := appServerWith(t, owntest.Spawn(pidFile, true), limitsReply)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := readLimits(ctx, t, bin); err != nil {
		t.Fatalf("ReadLimits: %v", err)
	}
	if child := owntest.PID(t, pidFile); !owntest.Gone(child) {
		t.Fatalf("tool %d survived the finished reading", child)
	}
}

// A process that escaped the server's tree but holds its output cannot keep
// the request reading past the deadline plus the bounded drain.
func TestReadLimitsDeadlineBoundsAnEscapedHolder(t *testing.T) {
	pidFile := owntest.PIDFile(t)
	bin := appServerWith(t, owntest.Escape(t, pidFile), "")
	const deadline = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	start := time.Now()
	err := readLimits(ctx, t, bin)
	owntest.PID(t, pidFile) // killed at cleanup
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if took := time.Since(start); took > deadline+4*time.Second {
		t.Fatalf("the request returned only after %s", took)
	}
}

// A server that cannot start is reported, not waited for.
func TestReadLimitsStartFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := readLimits(ctx, t, t.TempDir()+"/codex"); err == nil {
		t.Fatal("a missing server reported limits")
	}
}
