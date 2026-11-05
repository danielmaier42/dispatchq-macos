package api

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSecretEnvArgvLeavesACommandWithoutSecretsAlone(t *testing.T) {
	argv := []string{"/opt/claude", "--resume", "s"}
	got, cleanup, err := secretEnvArgv(nil, argv)
	if err != nil || !reflect.DeepEqual(got, argv) {
		t.Fatalf("secretEnvArgv = %v, %v, want argv unchanged", got, err)
	}
	cleanup()
}

// TestSecretEnvArgvCleanupRemovesTheFile: a terminal that did not open never
// runs the wrapper, so the caller removes the file instead.
func TestSecretEnvArgvCleanupRemovesTheFile(t *testing.T) {
	got, cleanup, err := secretEnvArgv([]string{"K=v"}, []string{"/opt/claude"})
	if err != nil {
		t.Fatalf("secretEnvArgv: %v", err)
	}
	file := got[3]
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("the env file is missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode = %v, want 0600", info.Mode().Perm())
	}
	if strings.Contains(strings.Join(got, " "), "=v") {
		t.Fatalf("argv = %v carries the value", got)
	}
	cleanup()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("env file still there after cleanup: %v", err)
	}
}
