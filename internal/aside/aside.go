// Package aside runs the questions dispatchq asks a harness on its own behalf:
// the prompt review that checks a task before it is queued, the assistant chat
// that operates dispatchq, and the feedback assistant that drafts a GitHub issue.
//
// It is the one place that turns a provider instance plus a question into a
// child process and an answer. The features themselves compose the question and
// read the answer; which harness they are put to, and how it is invoked, is not
// their business.
package aside

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/danielmaier42/claudeq/internal/provider"
	"github.com/danielmaier42/claudeq/internal/randid"
)

// Runner asks a configured provider instance a question.
type Runner struct {
	// Registry resolves an instance's adapter.
	Registry *provider.Registry
	// Exec runs the built command and returns its stdout. Nil uses the real one;
	// tests replace it to keep the whole pipeline hermetic.
	Exec func(ctx context.Context, dir string, cmd provider.Command) ([]byte, error)
	// NewID issues a session id when the caller does not carry one. Nil uses a
	// random UUID.
	NewID func() string
	// Dir overrides the directory asides run in. Empty uses the throwaway one
	// described at sessionDir, which is what the daemon wants.
	Dir string

	mu     sync.Mutex
	dir    string
	dirErr error
}

// ErrUnavailable means the chosen instance cannot answer dispatchq's questions —
// it is not configured, its adapter does not support asides, or its CLI could
// not be found. Callers report it as "unavailable", never as an answer.
var ErrUnavailable = errors.New("this provider cannot answer dispatchq's own questions")

// Ask runs one turn against inst and returns what the harness said.
func (r *Runner) Ask(ctx context.Context, inst provider.Instance, req provider.AsideRequest) (provider.Aside, error) {
	ad, err := r.Registry.Lookup(inst.Kind)
	if err != nil {
		return provider.Aside{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !ad.Capabilities().Asides {
		return provider.Aside{}, fmt.Errorf("%w: %s", ErrUnavailable, inst.Label())
	}
	if ad.ResolveBinary(inst) == "" {
		return provider.Aside{}, fmt.Errorf("%w: %s has no CLI to run", ErrUnavailable, inst.Label())
	}
	if req.SessionID == "" {
		req.SessionID = r.newID()
	}

	cmd, err := ad.AsideCommand(inst, req)
	if err != nil {
		if errors.Is(err, provider.ErrUnsupported) {
			return provider.Aside{}, fmt.Errorf("%w: %s", ErrUnavailable, inst.Label())
		}
		return provider.Aside{}, err
	}
	dir, err := r.sessionDir()
	if err != nil {
		return provider.Aside{}, err
	}
	out, err := r.exec(ctx, dir, cmd)
	if err != nil {
		// A refused request still prints its envelope before the CLI exits
		// non-zero, and that names why: a used-up credit balance must read as
		// that, not as a crashed command.
		var blocked *provider.AccountBlockedError
		if _, perr := ad.ParseAside(out); errors.As(perr, &blocked) {
			return provider.Aside{}, perr
		}
		return provider.Aside{}, err
	}
	res, err := ad.ParseAside(out)
	if err != nil {
		return provider.Aside{}, err
	}
	// Harnesses that name their own sessions report the id back; the ones that
	// take dispatchq's report nothing, and the caller must still get one back to
	// continue with.
	if res.SessionID == "" {
		res.SessionID = req.SessionID
	}
	return res, nil
}

func (r *Runner) exec(ctx context.Context, dir string, cmd provider.Command) ([]byte, error) {
	if r.Exec != nil {
		return r.Exec(ctx, dir, cmd)
	}
	return execCommand(ctx, dir, cmd)
}

// execCommand runs the command and returns stdout only, so a warning the CLI
// writes to stderr can never end up inside the JSON that gets parsed.
func execCommand(ctx context.Context, dir string, cmd provider.Command) ([]byte, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // the adapter built this from the configured provider
	c.Dir = dir
	if len(cmd.Env) > 0 {
		c.Env = append(os.Environ(), cmd.Env...)
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}
	out, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, shorten(string(ee.Stderr), 300))
		}
		return out, err
	}
	return out, nil
}

// dirName is the throwaway directory asides run in, inside the user's own
// temporary directory (per-user on macOS, so the fixed name is not shared).
const dirName = "dispatchq-aside"

// sessionDir is a stable empty directory to run in.
//
// Empty, because no real project's cwd may ever be the one an aside runs in:
// that is what keeps a repository's own instructions and history out of a
// review of a prompt that merely mentions it. Stable, because a harness that
// keys its session store by directory would otherwise leave a new stale project
// behind at every daemon start, and a conversation could not be resumed at all.
func (r *Runner) sessionDir() (string, error) {
	if r.Dir != "" {
		return r.Dir, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dir != "" || r.dirErr != nil {
		return r.dir, r.dirErr
	}
	dir := filepath.Join(os.TempDir(), dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.dirErr = fmt.Errorf("preparing the working directory: %w", err)
		return "", r.dirErr
	}
	r.dir = dir
	return r.dir, nil
}

func (r *Runner) newID() string {
	if r.NewID != nil {
		return r.NewID()
	}
	return randid.UUID()
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
