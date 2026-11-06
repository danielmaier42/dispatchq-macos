package binpath

import (
	"errors"
	"strings"
	"testing"
)

const npmShim = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\n" +
	"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n)\r\n\r\n" +
	"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\node_modules\\@openai\\codex\\bin\\codex.js\" %*\r\n"

func envWith(files map[string]bool, node string) batchEnv {
	return batchEnv{
		Exists: func(p string) bool { return files[p] },
		LookPath: func(string) (string, error) {
			if node == "" {
				return "", errors.New("not found")
			}
			return node, nil
		},
		ComSpec: `C:\Windows\System32\cmd.exe`,
	}
}

func TestPlanBatchNpmShimStartsNodeDirectly(t *testing.T) {
	dir := `C:\Users\Jo Ann\AppData\Roaming\npm`
	script := dir + `\node_modules\@openai\codex\bin\codex.js`
	args := []string{"exec", `say "hi" & 100% $HOME $(whoami) ^ | > <`, "line1\nline2", ""}

	// node on PATH
	p, err := planBatch(dir+`\codex.cmd`, args, []byte(npmShim), envWith(map[string]bool{script: true}, `C:\node\node.exe`))
	if err != nil {
		t.Fatal(err)
	}
	if p.CmdLine != "" || p.Name != `C:\node\node.exe` {
		t.Fatalf("expected a direct node start, got %+v", p)
	}
	if p.Args[0] != script || len(p.Args) != len(args)+1 {
		t.Fatalf("script not first / arg count: %q", p.Args)
	}
	for i, a := range args {
		if p.Args[i+1] != a {
			t.Errorf("argument %d changed: %q != %q", i, p.Args[i+1], a)
		}
	}

	// node.exe bundled next to the shim wins over PATH
	p, err = planBatch(dir+`\codex.cmd`, nil, []byte(npmShim), envWith(map[string]bool{script: true, dir + `\node.exe`: true}, ""))
	if err != nil || p.Name != dir+`\node.exe` {
		t.Fatalf("bundled node: %+v %v", p, err)
	}
}

func TestPlanBatchNodeMissingIsAnError(t *testing.T) {
	dir := `C:\npm`
	_, err := planBatch(dir+`\x.cmd`, nil, []byte(npmShim), envWith(map[string]bool{dir + `\node_modules\@openai\codex\bin\codex.js`: true}, ""))
	if err == nil || !strings.Contains(err.Error(), "node.exe was not found") {
		t.Fatalf("want a node-not-found error, got %v", err)
	}
}

func TestPlanBatchDirectExeShim(t *testing.T) {
	shim := "@echo off\r\n\"%~dp0\\vendor\\tool.exe\" %*\r\n"
	dir := `C:\bin`
	p, err := planBatch(dir+`\tool.cmd`, []string{"a b", "50%"}, []byte(shim), envWith(map[string]bool{dir + `\vendor\tool.exe`: true}, ""))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != dir+`\vendor\tool.exe` || p.CmdLine != "" || len(p.Args) != 2 || p.Args[1] != "50%" {
		t.Fatalf("got %+v", p)
	}
}

func TestPlanBatchFallbackThroughCmd(t *testing.T) {
	shim := "@echo off\r\nsomething-else %*\r\n"
	p, err := planBatch(`C:\Program Files\x\tool.cmd`, []string{"plain", "with space", "trail\\ ", `dir\`, "", "a=b"}, []byte(shim), envWith(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	want := `C:\Windows\System32\cmd.exe /d /s /c ""C:\Program Files\x\tool.cmd" plain "with space" "trail\ " dir\ "" "a=b""`
	if p.CmdLine != want {
		t.Fatalf("command line\n got %s\nwant %s", p.CmdLine, want)
	}
	// A trailing backslash inside quotes must not escape the closing quote.
	line, _ := batchCommandLine(`C:\t.cmd`, []string{`C:\my dir\`})
	if line != `"C:\t.cmd" "C:\my dir\\"` {
		t.Fatalf("trailing backslash: %s", line)
	}
}

func TestBatchCommandLineRefusesWhatCmdWouldInterpret(t *testing.T) {
	for _, bad := range []string{`a"b`, `100%`, `%PATH%`, `a&calc`, `a|b`, `a>b`, `a<b`, `^`, `(x)`, `hi!`, "a\nb", "a\rb", "a\x00b"} {
		if _, err := batchCommandLine(`C:\t.cmd`, []string{bad}); err == nil {
			t.Errorf("argument %q should be refused", bad)
		}
		if _, err := batchCommandLine(`C:\x`+bad+`\t.cmd`, nil); err == nil {
			t.Errorf("path containing %q should be refused", bad)
		}
	}
	// Characters that are only special to PowerShell or sh stay literal.
	for _, ok := range []string{"$HOME", "$whoami", "`x`", "it's", "ünï ¢ödé", `C:\Apps\$cash\x`, "[a]{b}", "*", "?"} {
		if _, err := batchCommandLine(`C:\t.cmd`, []string{ok}); err != nil {
			t.Errorf("argument %q should pass: %v", ok, err)
		}
	}
	_, err := batchCommandLine(`C:\t.cmd`, []string{"100%"})
	if err == nil || !strings.Contains(err.Error(), "native executable") {
		t.Fatalf("the error should say what to do: %v", err)
	}
}
