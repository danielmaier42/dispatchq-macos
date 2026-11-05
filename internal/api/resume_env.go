package api

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// withoutEntries returns env without the entries that appear in drop.
func withoutEntries(env, drop []string) []string {
	var out []string
	for _, kv := range env {
		if !slices.Contains(drop, kv) {
			out = append(out, kv)
		}
	}
	return out
}

// secretEnvArgv wraps argv so that the shell Terminal starts exports the given
// NAME=value entries before running it. The entries are written to a private
// temporary file that the wrapper sources and deletes at once, so a secret
// never appears in the visible command line or in the shell's history. Without
// entries argv comes back as it is. The returned function removes the file for
// a caller whose terminal did not open, so the wrapper never got to.
func secretEnvArgv(pairs, argv []string) ([]string, func(), error) {
	if len(pairs) == 0 {
		return argv, func() {}, nil
	}
	f, err := os.CreateTemp("", "dispatchq-resume-*") // created 0600
	if err != nil {
		return nil, nil, fmt.Errorf("prepare the session's environment: %w", err)
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	var b strings.Builder
	for _, kv := range pairs {
		name, value, _ := strings.Cut(kv, "=")
		fmt.Fprintf(&b, "export %s=%s\n", name, shellQuote(value))
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		cleanup()
		return nil, nil, fmt.Errorf("prepare the session's environment: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("prepare the session's environment: %w", err)
	}
	return append([]string{"/bin/sh", "-c", `. "$0"; rm -f "$0"; exec "$@"`, f.Name()}, argv...), cleanup, nil
}
