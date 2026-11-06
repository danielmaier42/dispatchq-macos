//go:build !windows

package executor

import "strings"

// defaultShell runs a script that does not name its own interpreter.
const defaultShell = "/bin/sh"

// scriptFileName is the temporary file a script job is written to.
const scriptFileName = "job"

// scriptCommand decides what runs the script file at path: its own shebang when
// it has one, the default shell otherwise.
func scriptCommand(path, src string) (name string, args []string, err error) {
	if strings.HasPrefix(src, "#!") {
		return path, nil, nil
	}
	return defaultShell, []string{path}, nil
}
