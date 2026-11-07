package cliref

import (
	"strings"
	"testing"
)

func TestQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"plain/path-1.txt": "plain/path-1.txt",
		"two words":        "'two words'",
		"it's":             `'it'\''s'`,
		"$HOME":            "'$HOME'",
		"Prüfung":          "Prüfung",
		"Läuft's":          `'Läuft'\''s'`,
		"":                 "''",
	} {
		if got := QuotePOSIX(in); got != want {
			t.Errorf("QuotePOSIX(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`C:\Program Files\DispatchQ\dispatchq.exe`: `'C:\Program Files\DispatchQ\dispatchq.exe'`,
		"it's":        "'it''s'",
		"it\u2019s":   "'it\u2019\u2019s'",
		"$env:TEMP ü": "'$env:TEMP ü'",
	} {
		if got := QuotePowerShell(in); got != want {
			t.Errorf("QuotePowerShell(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestCheckNormalizesAndRefuses(t *testing.T) {
	p, err := Check([]string{"pool", "edit", "night", "-member", "a=2", "--member=b", "--name", "Night"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.String(); got != "dispatchq pool edit night --member=a=2 --member=b --name=Night" {
		t.Fatalf("String = %s", got)
	}
	for _, argv := range [][]string{
		{"notify", "--title", "a", "--title", "b"},
		{"notify", "--title"},
		{"provider", "edit", "x", "--env", "K=V"},
		{"import", "relative.dispatchq"},
		{"pool", "edit", "night"},
	} {
		if _, err := Check(argv, false); err == nil {
			t.Errorf("Check(%q) accepted", argv)
		}
	}
	if _, err := Check([]string{"provider", "edit", "x", "--env", "K=V"}, true); err != nil {
		t.Errorf("a credential the caller allows was refused: %v", err)
	}
}

func TestRedact(t *testing.T) {
	got := Redact([]string{"settings", "--ntfy-token", "tk_1", "--pushover-user=u_2", "--ntfy-topic", "alerts"})
	want := "settings --ntfy-token *** --pushover-user=*** --ntfy-topic alerts"
	if strings.Join(got, " ") != want {
		t.Fatalf("Redact = %q", got)
	}
}
