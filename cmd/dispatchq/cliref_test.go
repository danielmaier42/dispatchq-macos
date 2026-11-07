package main

import (
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/danielmaier42/claudeq/internal/cliref"
)

// dispatchers are the functions that pick a command by its words, and the
// command group each one serves.
var dispatchers = map[string][]string{
	"run":          nil,
	"cmdProvider":  {"provider"},
	"cmdPool":      {"pool"},
	"cmdArtifacts": {"artifacts"},
	"cmdUpdate":    {"update"},
}

// aliases are words the parser accepts for a command the reference names
// otherwise.
var aliases = map[string]bool{"-version": true}

// parsedCommands reads the command words the CLI dispatches on out of its
// source, so a command added to a switch shows up here without anyone
// remembering to list it.
func parsedCommands(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			group, ok := dispatchers[fn.Name.Name]
			if !ok {
				continue
			}
			add := func(e ast.Expr) {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return
				}
				w, _ := strconv.Unquote(lit.Value)
				if w == "" || aliases[w] {
					return
				}
				out = append(out, strings.Join(append(slices.Clone(group), w), " "))
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CaseClause:
					for _, e := range x.List {
						add(e)
					}
				case *ast.BinaryExpr:
					if x.Op == token.EQL || x.Op == token.NEQ {
						add(x.Y)
					}
				}
				return true
			})
		}
	}
	// A group word only leads to its own dispatcher.
	var leaves []string
	for _, c := range out {
		if _, isGroup := dispatchers["cmd"+strings.ToUpper(c[:1])+c[1:]]; isGroup && !strings.Contains(c, " ") {
			continue
		}
		leaves = append(leaves, c)
	}
	slices.Sort(leaves)
	return slices.Compact(leaves)
}

func referenceNames() []string {
	var out []string
	for _, c := range cliref.Commands() {
		out = append(out, c.Name())
	}
	slices.Sort(out)
	return out
}

func TestReferenceNamesEveryCommand(t *testing.T) {
	got, want := referenceNames(), parsedCommands(t)
	for _, c := range want {
		if !slices.Contains(got, c) {
			t.Errorf("the CLI has command %q, internal/cliref does not describe it", c)
		}
	}
	for _, c := range got {
		if !slices.Contains(want, c) {
			t.Errorf("internal/cliref describes %q, the CLI has no such command", c)
		}
	}
}

var flagLine = regexp.MustCompile(`(?m)^[ \t]+-([a-z0-9-]+)( [a-z]+)?$`)

// helpFlags runs the command with -h and reads the flags its parser prints.
// ok is false when the command has no flag parser at all.
func helpFlags(t *testing.T, argv []string) (flags []string, ok bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := run(argv)
	os.Stdout, os.Stderr = stdout, stderr
	_ = w.Close()
	out := <-done
	if !errors.Is(runErr, flag.ErrHelp) {
		return nil, false
	}
	for _, m := range flagLine.FindAllStringSubmatch(out, -1) {
		// A boolean flag prints no value type; the reference marks it Bool.
		if m[2] == "" {
			m[1] += "(bool)"
		}
		flags = append(flags, m[1])
	}
	slices.Sort(flags)
	return flags, true
}

func TestReferenceNamesEveryFlag(t *testing.T) {
	t.Setenv("DISPATCHQ_HOME", t.TempDir())
	for _, c := range cliref.Commands() {
		argv := slices.Clone(c.Path)
		for range c.Args {
			argv = append(argv, "x")
		}
		got, hasParser := helpFlags(t, append(argv, "-h"))
		var want []string
		for _, f := range c.Flags {
			if f.Bool {
				want = append(want, f.Name+"(bool)")
			} else {
				want = append(want, f.Name)
			}
		}
		slices.Sort(want)
		if !hasParser {
			if len(want) > 0 {
				t.Errorf("%s: the reference lists flags %v, the command parses none", c.Name(), want)
			}
			continue
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: the parser has flags %v, the reference %v", c.Name(), got, want)
		}
	}
}

// TestUsageNamesEveryFlag keeps the help text in step with the parser: every
// flag the reference knows appears in the usage block of its command.
func TestUsageNamesEveryFlag(t *testing.T) {
	text := strings.Join([]string{usage, providerUsage, poolUsage, artifactsUsage}, "\n")
	for _, c := range cliref.Commands() {
		if len(c.Flags) == 0 {
			continue
		}
		block := usageBlock(text, c.Name())
		if block == "" {
			t.Errorf("the usage text has no line for dispatchq %s", c.Name())
			continue
		}
		for _, f := range c.Flags {
			if f.OldName != "" {
				continue
			}
			if !strings.Contains(block, "--"+f.Name) {
				t.Errorf("usage of dispatchq %s does not name --%s", c.Name(), f.Name)
			}
		}
	}
}

// usageBlock is the part of the usage text that documents one command: from
// its line to the next command's.
func usageBlock(text, name string) string {
	lines := strings.Split(text, "\n")
	var b strings.Builder
	in := false
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		if strings.HasPrefix(trim, "dispatchq ") {
			in = strings.HasPrefix(trim, "dispatchq "+name+" ") || trim == "dispatchq "+name
			if !in {
				continue
			}
		}
		if in {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
