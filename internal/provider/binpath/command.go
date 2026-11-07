package binpath

import (
	"fmt"
	"regexp"
	"strings"
)

// A .cmd file cannot be started like a program. Windows hands it to cmd.exe,
// which re-parses the whole command line with its own rules: a percent sign,
// a caret, an ampersand or a stray quote in an argument is read as cmd syntax,
// not as data, so an arbitrary prompt on a provider's command line could run
// something else. Nothing can escape those characters reliably for a batch
// file that forwards its arguments with %*.
//
// planBatch therefore avoids cmd.exe wherever it can. npm and scoop shims are
// tiny scripts that only start a node program or an .exe next to them; those
// are started directly, and every argument reaches the program as data through
// the ordinary CreateProcess quoting. Only a shim that does something else
// goes through cmd.exe, and then only with arguments that contain nothing cmd
// could interpret; anything else is refused with an explanation instead of
// being run in a way that could execute it.

// batchPlan says how to start a batch shim.
type batchPlan struct {
	// Name and Args start a program directly. Unused when CmdLine is set.
	Name string
	Args []string
	// CmdLine is a complete, already quoted command line for cmd.exe. Name is
	// cmd.exe then.
	CmdLine string
}

// batchEnv is what planBatch needs from the machine, injectable for tests.
type batchEnv struct {
	// Exists reports whether a file is there.
	Exists func(path string) bool
	// LookPath finds a program on PATH.
	LookPath func(name string) (string, error)
	// ComSpec is the command interpreter.
	ComSpec string
}

var (
	// shimScript matches the line npm's cmd-shim writes to start a node program:
	// "%_prog%"  "%dp0%\node_modules\pkg\bin\cli.js" %*
	shimScript = regexp.MustCompile(`(?i)"%(?:dp0|~dp0)%?\\([^"%\r\n]+\.(?:js|cjs|mjs))"\s+%\*`)
	// shimExe matches a shim that starts a program next to it:
	// "%dp0%\bin\tool.exe" %*
	shimExe = regexp.MustCompile(`(?i)"%(?:dp0|~dp0)%?\\([^"%\r\n]+\.exe)"\s+%\*`)
)

// planBatch decides how to start the batch file at path with args. shim is the
// file's content.
func planBatch(path string, args []string, shim []byte, env batchEnv) (batchPlan, error) {
	dir := dirOf(path)
	src := string(shim)

	if m := shimScript.FindStringSubmatchIndex(src); m != nil {
		script := dir + `\` + src[m[2]:m[3]]
		if strings.Contains(src[:m[0]], "%_prog%") || strings.Contains(src[:m[0]], "node") {
			node := dir + `\node.exe`
			if !env.Exists(node) {
				found, err := env.LookPath("node")
				if err != nil {
					return batchPlan{}, fmt.Errorf("%s starts a node program but node.exe was not found next to it or on PATH: %w", path, err)
				}
				node = found
			}
			if !env.Exists(script) {
				return batchPlan{}, fmt.Errorf("%s points at %s, which does not exist", path, script)
			}
			return batchPlan{Name: node, Args: append([]string{script}, args...)}, nil
		}
	}
	if m := shimExe.FindStringSubmatchIndex(src); m != nil {
		exe := dir + `\` + src[m[2]:m[3]]
		if env.Exists(exe) {
			return batchPlan{Name: exe, Args: args}, nil
		}
	}

	line, err := batchCommandLine(path, args)
	if err != nil {
		return batchPlan{}, err
	}
	comspec := env.ComSpec
	if comspec == "" {
		comspec = "cmd.exe"
	}
	return batchPlan{
		Name: comspec,
		// /s strips exactly the outer quotes; /d skips AutoRun commands.
		CmdLine: quoteIfNeeded(comspec) + ` /d /s /c "` + line + `"`,
	}, nil
}

// dirOf returns the directory part of a Windows path, accepting either
// separator, without a trailing separator.
func dirOf(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return "."
	}
	return p[:i]
}

// cmdUnsafe are the characters cmd.exe interprets even inside quotes
// (percent expands variables, quote ends the quoting) or that it treats as
// operators and line breaks. There is no escaping that survives a batch file
// forwarding %*, so they are refused.
const cmdUnsafe = "\"%^&|<>()!\r\n\x00"

// batchCommandLine builds `"path" arg...` for cmd.exe, or reports which
// argument cannot be passed to a batch file literally.
func batchCommandLine(path string, args []string) (string, error) {
	var b strings.Builder
	for i, a := range append([]string{path}, args...) {
		if j := strings.IndexAny(a, cmdUnsafe); j >= 0 {
			what := "argument"
			if i == 0 {
				what = "path"
			}
			return "", fmt.Errorf("the %s %s contains %q, which cmd.exe would interpret instead of passing it on; "+
				"%s is a batch file that cannot be started without cmd.exe, install the provider's native executable "+
				"or a version whose shim starts node directly", what, shorten(a), a[j], path)
		}
		if i == 0 {
			b.WriteString(`"` + a + `"`)
			continue
		}
		b.WriteByte(' ')
		b.WriteString(quoteIfNeeded(a))
	}
	return b.String(), nil
}

func shorten(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// quoteIfNeeded quotes an argument that is empty or holds a separator. A
// trailing run of backslashes is doubled so it cannot swallow the closing quote.
func quoteIfNeeded(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t,;=") {
		return a
	}
	trimmed := strings.TrimRight(a, `\`)
	return `"` + a + strings.Repeat(`\`, len(a)-len(trimmed)) + `"`
}
