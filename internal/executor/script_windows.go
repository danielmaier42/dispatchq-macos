//go:build windows

package executor

import (
	"errors"
	"strings"
)

// defaultShell runs a script job on Windows. Windows PowerShell ships with every
// supported Windows version, so a script job needs no extra install.
const defaultShell = "powershell.exe"

// scriptFileName carries the .ps1 extension PowerShell requires for -File.
const scriptFileName = "job.ps1"

// scriptCommand runs a script job as PowerShell. Windows has no shebang
// execution, so a script that names an interpreter that way is refused with an
// explanation instead of being handed to PowerShell, which would misread it.
// The execution policy is bypassed for this one process only, because the
// script is the operator's own job definition and the default policy refuses
// every unsigned .ps1 file.
func scriptCommand(path, src string) (name string, args []string, err error) {
	if strings.HasPrefix(src, "#!") {
		return "", nil, errors.New("script jobs on Windows run as PowerShell; a shebang line is not supported, " +
			"remove it or call the interpreter explicitly from the script")
	}
	return defaultShell, []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path}, nil
}

// scriptFileBytes is what the script file holds: the job's source with a UTF-8
// byte order mark, so Windows PowerShell 5.1 reads it as UTF-8.
func scriptFileBytes(src string) []byte { return withUTF8BOM(src) }
