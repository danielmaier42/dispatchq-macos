//go:build unix

package system

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHoldKeepsTheProcessUntilReleased(t *testing.T) {
	// A unique argument makes the helper findable among all processes.
	marker := "3601.25"
	release, err := Hold{Argv: []string{"sleep", marker}}.Inhibit()
	if err != nil {
		t.Fatalf("Inhibit: %v", err)
	}
	if !running(t, marker) {
		release()
		t.Fatal("helper process is not running after Inhibit")
	}
	release()
	deadline := time.Now().Add(2 * time.Second)
	for running(t, marker) {
		if time.Now().After(deadline) {
			t.Fatal("helper process still runs after release")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHoldReportsAMissingCommand(t *testing.T) {
	if _, err := (Hold{Argv: []string{"dispatchq-no-such-helper"}}).Inhibit(); err == nil {
		t.Fatal("Inhibit of a missing command: want an error")
	}
	if _, err := (Hold{}).Inhibit(); err == nil {
		t.Fatal("Inhibit without a command: want an error")
	}
}

// running reports whether a live (not zombie) `sleep marker` process exists.
func running(t *testing.T, marker string) bool {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "stat=,args=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[1] == "sleep" && fields[2] == marker && !strings.HasPrefix(fields[0], "Z") {
			return true
		}
	}
	return false
}
