package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// recordingService is a service manager that records what install asked of it.
type recordingService struct{ installed, uninstalled bool }

func (*recordingService) RetireLegacy(context.Context) error { return nil }
func (r *recordingService) Install(context.Context, serviceSpec, io.Writer) error {
	r.installed = true
	return nil
}
func (r *recordingService) Uninstall(context.Context, io.Writer) error {
	r.uninstalled = true
	return nil
}

// When the daemon of the data directory may not or cannot be stopped,
// install fails with that reason before it creates the data directory or
// registers and starts a daemon.
func TestInstallRefusalRegistersNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "store")
	t.Setenv("DISPATCHQ_HOME", home)
	refusal := &foreignDaemonError{Home: home, PID: 300, Image: betaImage, Want: []string{ourImage}}
	for _, failAt := range []int{1, 2} {
		svc := &recordingService{}
		calls := 0
		p := platform{service: svc, stopStrayDaemons: func() error {
			calls++
			if calls == failAt {
				return refusal
			}
			return nil
		}}
		err := cmdInstall(p)
		if !errors.Is(err, refusal) {
			t.Fatalf("stop failing at call %d: install = %v, want the refusal", failAt, err)
		}
		if svc.installed {
			t.Fatalf("stop failing at call %d: install registered the service anyway", failAt)
		}
		if failAt == 1 {
			if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("install created %s before the refusal: %v", home, err)
			}
		}
	}
}

// The installer's helper command needs an absolute folder and refuses where
// there is no installer integration.
func TestCmdStopInstallation(t *testing.T) {
	var got string
	p := platform{stopInstallation: func(dir string, _ io.Writer) error { got = dir; return nil }}
	abs := filepath.Join(t.TempDir(), "DispatchQ")
	if err := cmdStopInstallation(p, []string{"--dir", abs + string(filepath.Separator)}, io.Discard); err != nil || got != abs {
		t.Fatalf("stop-installation = %v, dir %q; want %q", err, got, abs)
	}
	for _, args := range [][]string{nil, {"--dir", "relative"}, {"--dir", abs, "extra"}} {
		got = ""
		if err := cmdStopInstallation(p, args, io.Discard); err == nil || got != "" {
			t.Errorf("args %q: err = %v, called with %q", args, err, got)
		}
	}
	if err := cmdStopInstallation(platform{}, []string{"--dir", abs}, io.Discard); !errors.Is(err, errUnsupported) {
		t.Errorf("without integration: err = %v, want errUnsupported", err)
	}
	refusal := errors.New("refused")
	p.stopInstallation = func(string, io.Writer) error { return refusal }
	var out bytes.Buffer
	if err := cmdStopInstallation(p, []string{"--dir", abs}, &out); !errors.Is(err, refusal) {
		t.Errorf("refusal not returned: %v", err)
	}
}
