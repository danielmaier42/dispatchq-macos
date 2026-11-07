package windowstest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The installer's stop-and-replace flow is checked by running the real script,
// preprocessed by makensis (macros and LogicLib expanded into plain jumps),
// in a small interpreter for the NSIS instructions the sections use. The
// interpreter models the installed files, the commands the script runs with
// their exit codes, and every change to the PC. Nothing runs on Windows and
// no process is touched. An instruction it does not model fails the test
// rather than being skipped.

// nsisProgram is the preprocessed script: functions and sections by name.
type nsisProgram struct {
	blocks map[string][]nsisInstr
}

type nsisInstr struct {
	op   string
	args []string
	line int
}

// nsisTokens splits one instruction line into NSIS words: quoted with ", '
// or `, or separated by blanks. $\x escapes stay inside a word.
func nsisTokens(line string) []string {
	var out []string
	i := 0
	for i < len(line) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i >= len(line) {
			break
		}
		if q := line[i]; q == '"' || q == '\'' || q == '`' {
			j := i + 1
			var b strings.Builder
			for j < len(line) && line[j] != q {
				if line[j] == '$' && j+2 < len(line) && line[j+1] == '\\' {
					b.WriteString(line[j : j+3])
					j += 3
					continue
				}
				b.WriteByte(line[j])
				j++
			}
			out = append(out, b.String())
			i = j + 1
			continue
		}
		j := i
		for j < len(line) && line[j] != ' ' && line[j] != '\t' {
			j++
		}
		out = append(out, line[i:j])
		i = j
	}
	return out
}

// nsisLines joins physical lines into instructions: makensis -PPO writes a
// string's $\r$\n as real line breaks, so a line with an open quote continues.
func nsisLines(src string) ([]string, []int) {
	var lines []string
	var nums []int
	var cur strings.Builder
	start := 0
	var quote byte
	for n, phys := range strings.Split(src, "\n") {
		if cur.Len() == 0 {
			start = n + 1
			if t := strings.TrimSpace(phys); strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
				lines = append(lines, "")
				nums = append(nums, start)
				continue
			}
		} else {
			cur.WriteString(" ")
		}
		for i := 0; i < len(phys); i++ {
			c := phys[i]
			if quote == 0 && (c == '"' || c == '\'' || c == '`') && (i == 0 || phys[i-1] == ' ' || phys[i-1] == '\t' || phys[i-1] == '=') {
				quote = c
			} else if quote != 0 && c == quote {
				quote = 0
			}
		}
		cur.WriteString(phys)
		if quote == 0 {
			lines = append(lines, strings.TrimSpace(cur.String()))
			nums = append(nums, start)
			cur.Reset()
		}
	}
	return lines, nums
}

func parseNSIS(t *testing.T, src string) nsisProgram {
	t.Helper()
	p := nsisProgram{blocks: map[string][]nsisInstr{}}
	lines, nums := nsisLines(src)
	block := ""
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!verbose") {
			continue
		}
		tok := nsisTokens(line)
		switch tok[0] {
		case "Function":
			block = "Function " + tok[1]
			continue
		case "Section":
			block = "Section " + tok[1]
			continue
		case "FunctionEnd", "SectionEnd":
			block = ""
			continue
		}
		if block == "" {
			continue
		}
		p.blocks[block] = append(p.blocks[block], nsisInstr{op: tok[0], args: tok[1:], line: nums[i]})
	}
	return p
}

func isLabel(in nsisInstr) bool { return strings.HasSuffix(in.op, ":") && len(in.args) == 0 }

// installerRun is one simulated run and what it did.
type installerRun struct {
	t       *testing.T
	prog    nsisProgram
	vars    map[string]string
	stack   []string
	files   map[string]string // path -> "old-unsafe", "new"
	outPath string
	errors  bool
	aborted bool
	// exits answers a command: exit code and output.
	exits func(program, sub string) (string, string)
	// answers answers MessageBoxes in order; the default is the /SD answer.
	answers []string
	// registry holds values by physical location (see regLocation); reads
	// go through the current SetRegView view.
	registry map[string]string
	view     string
	// messages are the texts of the MessageBoxes shown, expanded.
	messages []string
	// trace lists the commands run and the changes made, in order.
	trace      []string
	mutations  []string
	violations []string
	steps      int
}

func newRun(t *testing.T, prog nsisProgram, files map[string]string, exits func(program, sub string) (string, string), answers ...string) *installerRun {
	return &installerRun{t: t, prog: prog, vars: map[string]string{}, files: files, exits: exits, answers: answers,
		registry: map[string]string{}, view: "32"}
}

var userVar = regexp.MustCompile(`\$(InstallFailed|Purge|R[0-9]|[0-9])\b`)

// regLocation is where a key read by the installer's x86 process on 64-bit
// Windows is stored. HKCU\Software is shared by both views. HKLM\SOFTWARE in
// the 32-bit view (the default of an x86 process) is stored under
// SOFTWARE\WOW6432Node, which the 64-bit view (SetRegView 64) does not
// redirect. A path that names WOW6432Node itself is modelled as reaching the
// same stored key, the most generous reading of a location Windows reserves.
func regLocation(root, path, view string) string {
	p := strings.ToLower(path)
	switch {
	case root != "HKLM":
		return strings.ToLower(root) + `\` + p
	case view == "64" || strings.HasPrefix(p, `software\wow6432node\`):
		return `hklm\` + p
	case strings.HasPrefix(p, `software\`):
		return `hklm\software\wow6432node\` + strings.TrimPrefix(p, `software\`)
	}
	return `hklm\` + p
}

func (r *installerRun) expand(s string) string {
	return userVar.ReplaceAllStringFunc(s, func(v string) string { return r.vars[v] })
}

func (r *installerRun) mutate(what string) {
	r.mutations = append(r.mutations, what)
	r.trace = append(r.trace, "change: "+what)
}

// call runs a function or section; it reports false when the run aborted.
func (r *installerRun) call(name string) bool {
	body, ok := r.prog.blocks[name]
	if !ok {
		r.t.Fatalf("no %s in the script", name)
	}
	labels := map[string]int{}
	for i, in := range body {
		if isLabel(in) {
			labels[strings.TrimSuffix(in.op, ":")] = i
		}
	}
	// index of the next real instruction after i, n instructions on (labels
	// do not count).
	rel := func(i, n int) int {
		for n > 0 {
			i++
			if i >= len(body) || !isLabel(body[i]) {
				n--
			}
		}
		return i
	}
	jump := func(i int, target string) int {
		switch {
		case target == "" || target == "0":
			return i + 1
		case strings.HasPrefix(target, "+") || strings.HasPrefix(target, "-"):
			n, err := strconv.Atoi(target)
			if err != nil || n < 0 {
				r.t.Fatalf("unsupported relative jump %q", target)
			}
			return rel(i, n)
		}
		if at, ok := labels[target]; ok {
			return at
		}
		r.t.Fatalf("%s: no label %q", name, target)
		return 0
	}
	arg := func(in nsisInstr, k int) string {
		if k < len(in.args) {
			return in.args[k]
		}
		return ""
	}
	for i := 0; i < len(body); {
		r.steps++
		if r.steps > 10000 {
			r.t.Fatal("the installer loops")
		}
		in := body[i]
		next := i + 1
		switch op := in.op; {
		case isLabel(in):
		case strings.EqualFold(op, "Goto"):
			next = jump(i, arg(in, 0))
		case op == "StrCmp":
			if r.expand(arg(in, 0)) == r.expand(arg(in, 1)) {
				next = jump(i, arg(in, 2))
			} else {
				next = jump(i, arg(in, 3))
			}
		case op == "IntCmp":
			a, _ := strconv.Atoi(r.expand(arg(in, 0)))
			b, _ := strconv.Atoi(r.expand(arg(in, 1)))
			switch {
			case a == b:
				next = jump(i, arg(in, 2))
			case a < b:
				next = jump(i, arg(in, 3))
			default:
				next = jump(i, arg(in, 4))
			}
		case op == "IfFileExists":
			if _, ok := r.files[arg(in, 0)]; ok {
				next = jump(i, arg(in, 1))
			} else {
				next = jump(i, arg(in, 2))
			}
		case op == "IfErrors":
			if r.errors {
				next = jump(i, arg(in, 0))
			} else {
				next = jump(i, arg(in, 1))
			}
			r.errors = false
		case op == "ClearErrors":
			r.errors = false
		case op == "StrCpy":
			r.vars[arg(in, 0)] = r.expand(arg(in, 1))
		case op == "Pop":
			if len(r.stack) == 0 {
				r.t.Fatalf("line %d: Pop on an empty stack", in.line)
			}
			r.vars[arg(in, 0)] = r.stack[len(r.stack)-1]
			r.stack = r.stack[:len(r.stack)-1]
		case op == "Push":
			r.stack = append(r.stack, r.expand(arg(in, 0)))
		case op == "Call":
			if !r.call("Function " + arg(in, 0)) {
				return false
			}
		case op == "Return":
			return true
		case op == "Abort":
			r.aborted = true
			r.trace = append(r.trace, "abort: "+r.expand(arg(in, 0)))
			return false
		case op == "MessageBox":
			answer := ""
			for k := 2; k < len(in.args); k++ {
				if in.args[k] == "/SD" && k+1 < len(in.args) {
					answer = in.args[k+1]
				}
			}
			if len(r.answers) > 0 {
				answer, r.answers = r.answers[0], r.answers[1:]
			}
			r.trace = append(r.trace, "ask: "+answer)
			r.messages = append(r.messages, r.expand(arg(in, 1)))
			for k := 2; k+1 < len(in.args); k += 2 {
				if in.args[k] != "/SD" && in.args[k] == answer {
					next = jump(i, in.args[k+1])
				}
			}
		case op == "nsExec::ExecToStack" || op == "nsExec::ExecToLog" || op == "nsExec::Exec" || op == "Exec" || op == "ExecWait":
			// nsExec options (/TIMEOUT=ms, /OEM, /MBCS) come before the command.
			k := 0
			for strings.HasPrefix(op, "nsExec::") && strings.HasPrefix(arg(in, k), "/") {
				r.trace = append(r.trace, "option: "+arg(in, k))
				k++
			}
			code, output := r.run(r.expand(arg(in, k)))
			switch op {
			case "nsExec::ExecToStack":
				r.stack = append(r.stack, output, code)
			case "nsExec::ExecToLog", "nsExec::Exec":
				r.stack = append(r.stack, code)
			case "ExecWait":
				if code == "error" {
					r.errors = true
				} else if v := arg(in, 1); v != "" {
					r.vars[v] = code
				}
			}
		case op == "File":
			dest := r.outPath + `\` + filepath.Base(strings.ReplaceAll(arg(in, 0), `\`, "/"))
			if strings.HasPrefix(arg(in, 0), "/oname=") {
				dest = strings.TrimPrefix(arg(in, 0), "/oname=")
			}
			r.files[dest] = "new"
			if !strings.HasPrefix(dest, `$PLUGINSDIR\`) {
				r.mutate("file " + dest)
			}
		case op == "SetOutPath":
			r.outPath = arg(in, 0)
			if strings.HasPrefix(r.outPath, "$INSTDIR") {
				r.mutate("folder " + r.outPath)
			}
		case op == "Delete":
			delete(r.files, arg(in, 0))
			r.mutate("delete " + arg(in, 0))
		case op == "RMDir", op == "WriteRegStr", op == "WriteRegDWORD", op == "WriteRegExpandStr", op == "DeleteRegKey",
			op == "DeleteRegValue", op == "CreateShortcut", op == "CreateDirectory", op == "WriteUninstaller", op == "Rename":
			r.mutate(op + " " + strings.Join(in.args, " "))
		case op == "ReadRegStr":
			v, ok := r.registry[regLocation(arg(in, 1), r.expand(arg(in, 2)), r.view)+"|"+strings.ToLower(arg(in, 3))]
			r.vars[arg(in, 0)] = v
			if !ok {
				r.errors = true
			}
			r.trace = append(r.trace, fmt.Sprintf("read: %s %s view %s = %q", arg(in, 1), arg(in, 2), r.view, v))
		case op == "SetRegView":
			switch v := arg(in, 0); v {
			case "32", "64":
				r.view = v
			case "default":
				r.view = "32"
			default:
				r.t.Fatalf("line %d: unsupported SetRegView %q", in.line, v)
			}
		case op == "IntFmt":
			n, err := strconv.ParseInt(r.expand(arg(in, 2)), 0, 64)
			if err != nil || arg(in, 1) != "0x%08X" {
				r.t.Fatalf("line %d: unsupported IntFmt %v", in.line, in.args)
			}
			r.vars[arg(in, 0)] = fmt.Sprintf("0x%08X", uint32(int32(n)))
		case op == "DetailPrint", op == "Sleep", op == "SetErrorLevel", op == "InitPluginsDir", op == "System::Call":
		default:
			r.t.Fatalf("%s line %d: the simulation does not model %q", name, in.line, op)
		}
		i = next
	}
	return true
}

// run models a command line: which program it starts, whether that program
// may be an old build, and the exit code the scenario gives it.
func (r *installerRun) run(cmd string) (string, string) {
	tok := nsisTokens(cmd)
	program := tok[0]
	sub := ""
	if len(tok) > 1 {
		sub = tok[1]
	}
	r.trace = append(r.trace, "run: "+cmd)
	low := strings.ToLower(cmd)
	if strings.Contains(low, "taskkill") || strings.Contains(low, "stop-process") {
		r.violations = append(r.violations, "ends processes by name: "+cmd)
	}
	if strings.HasPrefix(program, "$INSTDIR") || strings.HasPrefix(program, "$PLUGINSDIR") {
		switch r.files[program] {
		case "new":
		case "old-unsafe":
			r.violations = append(r.violations, "runs the installed, older "+program+": "+cmd)
		default:
			return "error", ""
		}
	}
	if strings.Contains(program, "powershell") || sub == "install" {
		r.mutate("command " + cmd)
	}
	return r.exits(program, sub)
}

// preprocess runs makensis -PPO on an installer script.
// Extra defines (such as -DBOOTSTRAPPER) are passed on.
func preprocess(t *testing.T, nsi string, defines ...string) nsisProgram {
	t.Helper()
	if _, err := exec.LookPath("makensis"); err != nil {
		t.Skip("makensis not installed")
	}
	args := append([]string{"-PPO", "-INPUTCHARSET", "UTF8"}, defines...)
	args = append(args, "-DVERSION=test", "-DARCH=x64", "-DSTAGE=stage", "-DOUTFILE=out.exe", nsi)
	out, err := exec.Command("makensis", args...).Output()
	if err != nil {
		t.Fatalf("makensis -PPO: %v", err)
	}
	return parseNSIS(t, string(out))
}

// installerScript is the script under test; INSTALLER_NSI overrides it so the
// same scenarios can be run against another revision.
func installerScript() string {
	if p := os.Getenv("INSTALLER_NSI"); p != "" {
		return p
	}
	return filepath.Join(scripts, "windows", "dispatchq.nsi")
}

// oldBeta is an installed earlier build: its dispatchqd ends every dispatchqd
// by name, so the new installer must never run it.
func oldBeta() map[string]string {
	return map[string]string{`$INSTDIR\dispatchqd.exe`: "old-unsafe", `$INSTDIR\dispatchqapp.exe`: "old-unsafe"}
}

// exitsFor answers the stop helper, install and uninstall with the given
// codes in order (the last one repeats), everything else with success.
func exitsFor(stop, install, uninstall []string) func(string, string) (string, string) {
	pick := func(codes *[]string) (string, string) {
		c := (*codes)[0]
		if len(*codes) > 1 {
			*codes = (*codes)[1:]
		}
		if c != "0" {
			return c, "the data directory is in use by pid 300 running C:\\beta\\dispatchqd.exe; quit that copy, then try again"
		}
		return c, "ok"
	}
	return func(_, sub string) (string, string) {
		switch sub {
		case "stop-installation":
			return pick(&stop)
		case "install":
			return pick(&install)
		case "uninstall":
			return pick(&uninstall)
		}
		return "0", ""
	}
}

func (r *installerRun) finishShown() bool {
	if _, ok := r.prog.blocks["Function SkipFinishAfterFailure"]; !ok {
		return !r.aborted
	}
	return !r.aborted && r.call("Function SkipFinishAfterFailure")
}

func (r *installerRun) dump() string {
	return strings.Join(r.trace, "\n")
}

func indexOf(trace []string, prefix string) int {
	for i, s := range trace {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

// Upgrading an older beta whose stop is refused (another installation owns the
// data directory): setup never runs the old dispatchqd, changes nothing,
// stops with the reason and does not offer to open DispatchQ.
func TestInstallerRefusalChangesNothing(t *testing.T) {
	prog := preprocess(t, installerScript())
	r := newRun(t, prog, oldBeta(), exitsFor([]string{"1"}, []string{"0"}, []string{"0"}), "IDCANCEL")
	r.call("Section DispatchQ")
	if len(r.violations) > 0 {
		t.Errorf("unsafe commands: %v\n%s", r.violations, r.dump())
	}
	if !r.aborted {
		t.Errorf("setup completed although the running DispatchQ could not be stopped\n%s", r.dump())
	}
	if len(r.mutations) > 0 {
		t.Errorf("setup changed the PC before or despite the refusal: %v", r.mutations)
	}
	if r.finishShown() {
		t.Error("the finish page (Open DispatchQ) is shown after a refusal")
	}
}

// Retry after the user quit the other copy: the new helper runs again, and
// only after it succeeds is anything replaced. The installed old dispatchqd
// is never run; the service is registered by the new one.
func TestInstallerUpgradeRunsOnlyTheNewBuild(t *testing.T) {
	prog := preprocess(t, installerScript())
	r := newRun(t, prog, oldBeta(), exitsFor([]string{"1", "0"}, []string{"0"}, []string{"0"}), "IDRETRY")
	r.call("Section DispatchQ")
	if len(r.violations) > 0 {
		t.Fatalf("unsafe commands: %v\n%s", r.violations, r.dump())
	}
	if r.aborted || !r.finishShown() {
		t.Fatalf("setup did not complete after a successful retry\n%s", r.dump())
	}
	helperRuns := 0
	lastHelper := -1
	for i, s := range r.trace {
		if strings.HasPrefix(s, "run: ") && strings.Contains(s, "stop-installation") {
			helperRuns++
			lastHelper = i
			if !strings.Contains(s, `$PLUGINSDIR\`) || !strings.Contains(s, `--dir "$INSTDIR"`) {
				t.Errorf("the stop helper is not the new build for this folder: %s", s)
			}
		}
	}
	if helperRuns != 2 {
		t.Errorf("stop helper ran %d times, want 2 (refused, then retried)\n%s", helperRuns, r.dump())
	}
	if first := indexOf(r.trace, "change: "); first < lastHelper {
		t.Errorf("setup changed %q before the stop succeeded\n%s", r.trace[first], r.dump())
	}
	if indexOf(r.trace, `run: "$INSTDIR\dispatchqd.exe" install`) < 0 {
		t.Errorf("the new dispatchqd did not register the service\n%s", r.dump())
	}
}

// A failed service registration ends setup as failed before it records the
// installation, and the finish page does not offer to open DispatchQ.
func TestInstallerRegistrationFailureIsAFailure(t *testing.T) {
	prog := preprocess(t, installerScript())
	r := newRun(t, prog, map[string]string{}, exitsFor([]string{"0"}, []string{"1"}, []string{"0"}))
	r.call("Section DispatchQ")
	if !r.aborted {
		t.Errorf("setup completed although the service was not registered\n%s", r.dump())
	}
	for _, m := range r.mutations {
		if strings.Contains(m, `Software\DispatchQ`) || strings.Contains(m, `CurrentVersion\Uninstall\DispatchQ`) {
			t.Errorf("setup recorded the installation after the registration failed: %s", m)
		}
	}
	if r.finishShown() {
		t.Error("the finish page (Open DispatchQ) is shown after a failed registration")
	}
}

// A fresh installation completes and offers to open DispatchQ.
func TestInstallerFreshInstallCompletes(t *testing.T) {
	prog := preprocess(t, installerScript())
	r := newRun(t, prog, map[string]string{}, exitsFor([]string{"0"}, []string{"0"}, []string{"0"}))
	r.call("Section DispatchQ")
	if r.aborted || !r.finishShown() || len(r.violations) > 0 {
		t.Fatalf("fresh install: aborted %v, violations %v\n%s", r.aborted, r.violations, r.dump())
	}
}

// The uninstaller removes nothing when the daemon cannot be stopped safely,
// and removes the files after it was stopped.
func TestUninstallerRefusalRemovesNothing(t *testing.T) {
	prog := preprocess(t, installerScript())
	installed := func() map[string]string {
		return map[string]string{`$INSTDIR\dispatchqd.exe`: "new", `$INSTDIR\dispatchqapp.exe`: "new"}
	}
	r := newRun(t, prog, installed(), exitsFor([]string{"0"}, []string{"0"}, []string{"1"}), "IDCANCEL")
	r.call("Section Uninstall")
	if len(r.violations) > 0 {
		t.Errorf("unsafe commands: %v\n%s", r.violations, r.dump())
	}
	if !r.aborted || len(r.mutations) > 0 {
		t.Errorf("refused uninstall: aborted %v, changes %v\n%s", r.aborted, r.mutations, r.dump())
	}

	r = newRun(t, prog, installed(), exitsFor([]string{"0"}, []string{"0"}, []string{"0"}))
	r.call("Section Uninstall")
	if r.aborted || len(r.violations) > 0 || indexOf(r.trace, `change: delete $INSTDIR\dispatchqd.exe`) < 0 {
		t.Fatalf("uninstall did not complete: aborted %v, violations %v\n%s", r.aborted, r.violations, r.dump())
	}
}

// The tokenizer keeps quoted words and $\ escapes together.
func TestNSISTokens(t *testing.T) {
	got := fmt.Sprintf("%q", nsisTokens(`nsExec::ExecToStack '"$PLUGINSDIR\x.exe" stop --dir "$INSTDIR"'  StrCmp `+"`$0` ``"+` "a$\"b"`))
	want := `["nsExec::ExecToStack" "\"$PLUGINSDIR\\x.exe\" stop --dir \"$INSTDIR\"" "StrCmp" "$0" "" "a$\\\"b"]`
	if got != want {
		t.Fatalf("tokens = %s\nwant     %s", got, want)
	}
}

// Without makensis, the plain script is still checked for the two patterns
// the flow test rules out: ending processes by name, and running the
// installed (possibly older) dispatchqd while stopping it before an upgrade.
func TestInstallerStopPathStatic(t *testing.T) {
	raw, err := os.ReadFile(installerScript())
	if err != nil {
		t.Fatal(err)
	}
	inStop := false
	for i, line := range strings.Split(string(raw), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, ";") {
			continue
		}
		if strings.Contains(strings.ToLower(code), "taskkill") {
			t.Errorf("dispatchq.nsi:%d ends processes by name: %s", i+1, code)
		}
		switch {
		case strings.HasPrefix(code, "Function StopRunning"):
			inStop = true
		case code == "FunctionEnd":
			inStop = false
		case inStop && strings.Contains(code, "Exec") && strings.Contains(code, `$INSTDIR\dispatchqd.exe`):
			t.Errorf("dispatchq.nsi:%d runs the installed dispatchqd before replacing it: %s", i+1, code)
		}
	}
}
