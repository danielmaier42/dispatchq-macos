package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielmaier42/claudeq/internal/store"
)

// fakeProc is one entry of a synthetic process inventory.
type fakeProc struct {
	image    string
	created  time.Time
	imageErr error
	timeErr  error
	// home is the data directory whose daemon lock file this process has open
	// (and holds), if any.
	home string
}

// inventory is a synthetic process table with per-store daemon locks. The
// lock file's pid and the system's list of processes using the file are kept
// separately, as on Windows: the pid can be stale, the list cannot.
type inventory struct {
	t       *testing.T
	procs   map[uint32]*fakeProc
	lockPID map[string]int
	held    map[string]bool
	openErr map[uint32]error
	usesErr error
	// extraUsers are listed as using a store's lock file in addition to the
	// processes whose home is that store (stale or injected instances).
	extraUsers map[string][]processInstance
	killed     []uint32
	opened     []uint32
	afterKill  func(pid uint32)
	termErr    error
	neverExits bool
}

func newInventory(t *testing.T) *inventory {
	return &inventory{t: t, procs: map[uint32]*fakeProc{}, lockPID: map[string]int{}, held: map[string]bool{},
		openErr: map[uint32]error{}, extraUsers: map[string][]processInstance{}}
}

// daemon adds a process started at started that holds home's lock and wrote
// its pid.
func (inv *inventory) daemon(pid uint32, image, home string, started time.Time) {
	inv.procs[pid] = &fakeProc{image: image, created: started, home: home}
	inv.held[home] = true
	inv.lockPID[home] = int(pid)
}

func (inv *inventory) probe(home string) func() (store.DaemonHolder, error) {
	return func() (store.DaemonHolder, error) {
		return store.DaemonHolder{Held: inv.held[home], PID: inv.lockPID[home]}, nil
	}
}

func (inv *inventory) users(home string) func() ([]processInstance, error) {
	return func() ([]processInstance, error) {
		if inv.usesErr != nil {
			return nil, inv.usesErr
		}
		var out []processInstance
		for pid, p := range inv.procs {
			if p.home == home {
				out = append(out, processInstance{PID: pid, Started: p.created})
			}
		}
		return append(out, inv.extraUsers[home]...), nil
	}
}

func (inv *inventory) open(pid uint32) (daemonProcess, error) {
	inv.opened = append(inv.opened, pid)
	if err := inv.openErr[pid]; err != nil {
		return nil, err
	}
	p, ok := inv.procs[pid]
	if !ok {
		return nil, errors.New("no such process")
	}
	return &fakeHandle{inv: inv, pid: pid, p: p}, nil
}

type fakeHandle struct {
	inv  *inventory
	pid  uint32
	p    *fakeProc
	done bool
}

func (h *fakeHandle) Image() (string, error)      { return h.p.image, h.p.imageErr }
func (h *fakeHandle) Created() (time.Time, error) { return h.p.created, h.p.timeErr }
func (h *fakeHandle) Close() error                { return nil }

func (h *fakeHandle) Terminate() error {
	if h.inv.termErr != nil {
		return h.inv.termErr
	}
	h.inv.killed = append(h.inv.killed, h.pid)
	if h.inv.neverExits {
		return nil
	}
	h.done = true
	delete(h.inv.procs, h.pid)
	if home := h.p.home; home != "" {
		still := false
		for _, p := range h.inv.procs {
			if p.home == home {
				still = true
			}
		}
		h.inv.held[home] = still
	}
	if h.inv.afterKill != nil {
		h.inv.afterKill(h.pid)
	}
	return nil
}

func (h *fakeHandle) Wait(time.Duration) bool { return h.done }

// sameFold compares paths by name, as Windows does.
func sameFold(a, b string) bool { return strings.EqualFold(a, b) }

const (
	ourImage     = `C:\Users\me\AppData\Local\Programs\DispatchQ\dispatchqd.exe`
	betaImage    = `C:\Users\me\beta\DispatchQ\dispatchqd.exe`
	homeA        = `C:\Users\me\AppData\Roaming\dispatchq`
	homeB        = `D:\isolated-store`
	selfPID      = 4242
	stopAttempts = 5
)

func (inv *inventory) stopper(home string, images ...string) daemonStopper {
	if len(images) == 0 {
		images = []string{ourImage}
	}
	return daemonStopper{
		home: home, self: selfPID, images: images,
		probe: inv.probe(home), lockUsers: inv.users(home), open: inv.open, sameFile: sameFold,
		sleep: func(time.Duration) {}, attempts: stopAttempts, exitWait: time.Second,
	}
}

// assertNotKilled fails when any of the foreign pids was terminated.
func (inv *inventory) assertNotKilled(foreign ...uint32) {
	inv.t.Helper()
	for _, k := range inv.killed {
		for _, f := range foreign {
			if k == f {
				inv.t.Fatalf("terminated foreign pid %d (killed %v)", f, inv.killed)
			}
		}
	}
}

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

// The audit case: daemons of another installation, of another data directory
// and an unrelated dispatchqd.exe run beside this installation's daemon. Only
// the process using this data directory's lock, running this installation's
// executable, is stopped.
func TestStopperStopsOnlyThisStoresDaemon(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.daemon(200, ourImage, homeB, t0)                                     // same executable, other store
	inv.procs[300] = &fakeProc{image: betaImage, created: t0}                // same name, other installation
	inv.procs[400] = &fakeProc{image: `C:\Temp\dispatchqd.exe`, created: t0} // same name, unrelated

	stopped, err := inv.stopper(homeA).stop()
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(stopped) != 1 || stopped[0] != 100 {
		t.Fatalf("stopped = %v, want [100]", stopped)
	}
	inv.assertNotKilled(200, 300, 400, selfPID)
	for _, pid := range inv.opened {
		if pid != 100 {
			t.Errorf("opened pid %d; only users of the lock file may be examined", pid)
		}
	}
	if !inv.held[homeB] {
		t.Error("the other data directory lost its daemon")
	}
}

// The reviewer's counterexample. Store A's lock file still names pid 100 from
// an earlier owner. The clock was set back, and pid 100 now belongs to the same
// executable serving store B, started "before" the stale write. A new daemon
// (pid 150) holds store A's lock but has not rewritten the pid yet. Only 150,
// which the system reports as using store A's lock file, may be stopped.
func TestStopperClockRollbackCannotKillOtherStore(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeB, t0.Add(-time.Hour)) // started "earlier" after the rollback
	inv.daemon(150, ourImage, homeA, t0.Add(-2*time.Hour))
	inv.lockPID[homeA] = 100 // stale

	stopped, err := inv.stopper(homeA).stop()
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	inv.assertNotKilled(100)
	if len(stopped) != 1 || stopped[0] != 150 {
		t.Fatalf("stopped = %v, want [150]", stopped)
	}
}

// The same rollback with the new owner not reported (yet): nothing is stopped,
// and the stale pid is named only as information.
func TestStopperStalePIDWithoutReportedUserStopsNothing(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeB, t0.Add(-time.Hour))
	inv.held[homeA] = true
	inv.lockPID[homeA] = 100
	_, err := inv.stopper(homeA).stop()
	var unverified *unverifiedDaemonError
	if !errors.As(err, &unverified) {
		t.Fatalf("err = %v, want unverifiedDaemonError", err)
	}
	inv.assertNotKilled(100)
	if len(inv.opened) != 0 {
		t.Errorf("opened %v from the stale lock pid", inv.opened)
	}
}

// A reported instance whose pid now belongs to another process (any start
// time, earlier or later) is never acted on.
func TestStopperRejectsReusedPID(t *testing.T) {
	for name, created := range map[string]time.Time{
		"later":   t0.Add(time.Minute),
		"earlier": t0.Add(-time.Hour), // clock set back
	} {
		t.Run(name, func(t *testing.T) {
			inv := newInventory(t)
			inv.held[homeA] = true
			inv.extraUsers[homeA] = []processInstance{{PID: 100, Started: t0}}
			inv.procs[100] = &fakeProc{image: ourImage, created: created, home: homeB}
			stopped, err := inv.stopper(homeA).stop()
			var unverified *unverifiedDaemonError
			if !errors.As(err, &unverified) {
				t.Fatalf("err = %v, want unverifiedDaemonError", err)
			}
			if len(stopped) != 0 {
				t.Fatalf("stopped = %v", stopped)
			}
			inv.assertNotKilled(100)
			if !strings.Contains(err.Error(), "no longer the process") || !strings.Contains(err.Error(), "was not stopped") {
				t.Errorf("error %q does not explain the reused pid", err)
			}
		})
	}
}

// The same executable serving another DISPATCHQ_HOME is not this store's daemon.
func TestStopperLeavesSameImageOtherStore(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(200, ourImage, homeB, t0)
	stopped, err := inv.stopper(homeA).stop()
	if err != nil || len(stopped) != 0 {
		t.Fatalf("stop = %v, %v; want nothing stopped, no error", stopped, err)
	}
	inv.assertNotKilled(200)
	if len(inv.opened) != 0 {
		t.Errorf("opened %v although nothing owns the store", inv.opened)
	}
}

// A daemon of another installation that owns this data directory is reported,
// not stopped; neither is this installation's process beside it.
func TestStopperRefusesForeignInstallation(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(300, betaImage, homeA, t0)
	stopped, err := inv.stopper(homeA).stop()
	var foreign *foreignDaemonError
	if !errors.As(err, &foreign) {
		t.Fatalf("err = %v, want foreignDaemonError", err)
	}
	if len(stopped) != 0 {
		t.Fatalf("stopped = %v", stopped)
	}
	inv.assertNotKilled(300)
	for _, want := range []string{"pid 300", betaImage, "nothing was stopped", "dispatchqd uninstall", "Task Manager"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	inv = newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.procs[300] = &fakeProc{image: betaImage, created: t0, home: homeA} // waiting on the same lock
	if _, err := inv.stopper(homeA).stop(); !errors.As(err, &foreign) {
		t.Fatalf("err = %v, want foreignDaemonError", err)
	}
	if len(inv.killed) != 0 {
		t.Fatalf("killed %v beside a foreign user of the lock", inv.killed)
	}
}

// The installation recorded by the installer counts as this one.
func TestStopperAcceptsRegisteredInstallation(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(300, betaImage, homeA, t0)
	stopped, err := inv.stopper(homeA, ourImage, betaImage).stop()
	if err != nil || len(stopped) != 1 || stopped[0] != 300 {
		t.Fatalf("stop = %v, %v; want [300]", stopped, err)
	}
}

// Missing or unreadable identity never leads to a termination.
func TestStopperFailsSafeWithoutIdentity(t *testing.T) {
	cases := map[string]func(inv *inventory){
		"lock held, no process reported": func(inv *inventory) {
			inv.held[homeA] = true
			inv.lockPID[homeA] = 100
		},
		"process list unavailable": func(inv *inventory) {
			inv.daemon(100, ourImage, homeA, t0)
			inv.usesErr = errors.New("RmGetList: access denied")
		},
		"only the caller is reported": func(inv *inventory) {
			inv.daemon(selfPID, ourImage, homeA, t0)
		},
		"holder cannot be opened": func(inv *inventory) {
			inv.daemon(100, ourImage, homeA, t0)
			inv.openErr[100] = errors.New("access denied")
		},
		"reported holder is gone": func(inv *inventory) {
			inv.held[homeA] = true
			inv.extraUsers[homeA] = []processInstance{{PID: 100, Started: t0}}
		},
		"image unreadable": func(inv *inventory) {
			inv.daemon(100, ourImage, homeA, t0)
			inv.procs[100].imageErr = errors.New("access denied")
		},
		"start time unreadable": func(inv *inventory) {
			inv.daemon(100, ourImage, homeA, t0)
			inv.procs[100].timeErr = errors.New("access denied")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			inv := newInventory(t)
			setup(inv)
			stopped, err := inv.stopper(homeA).stop()
			var unverified *unverifiedDaemonError
			if !errors.As(err, &unverified) {
				t.Fatalf("err = %v, want unverifiedDaemonError", err)
			}
			if len(inv.killed) != 0 || len(stopped) != 0 {
				t.Fatalf("terminated %v without a confirmed identity", inv.killed)
			}
			if !strings.Contains(err.Error(), homeA) || !strings.Contains(err.Error(), "Task Manager") {
				t.Errorf("error %q is not actionable", err)
			}
		})
	}
}

// When this installation's daemon is replaced by a foreign owner while it is
// stopped, the error says both what was stopped and what was left alone.
func TestStopperReportsForeignSuccessor(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.afterKill = func(uint32) { inv.daemon(300, betaImage, homeA, t0.Add(time.Minute)) }
	stopped, err := inv.stopper(homeA).stop()
	var foreign *foreignDaemonError
	if !errors.As(err, &foreign) || foreign.PID != 300 {
		t.Fatalf("err = %v, want foreignDaemonError for 300", err)
	}
	if len(stopped) != 1 || stopped[0] != 100 {
		t.Fatalf("stopped = %v, want [100]", stopped)
	}
	inv.assertNotKilled(300)
}

// A daemon that cannot be terminated, or that does not exit, is an error and
// is not reported as stopped.
func TestStopperReportsDaemonThatStays(t *testing.T) {
	inv := newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.termErr = errors.New("access denied")
	if _, err := inv.stopper(homeA).stop(); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v, want the termination error", err)
	}

	inv = newInventory(t)
	inv.daemon(100, ourImage, homeA, t0)
	inv.neverExits = true
	stopped, err := inv.stopper(homeA).stop()
	var unverified *unverifiedDaemonError
	if !errors.As(err, &unverified) || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("err = %v, want the daemon that did not exit", err)
	}
	if len(stopped) != 0 || strings.Contains(err.Error(), "has exited") {
		t.Fatalf("a daemon that did not exit is reported as stopped: %v, %v", stopped, err)
	}
}

// Nobody owns the data directory: nothing is examined or stopped.
func TestStopperWithoutDaemon(t *testing.T) {
	inv := newInventory(t)
	inv.procs[300] = &fakeProc{image: betaImage, created: t0}
	stopped, err := inv.stopper(homeA).stop()
	if err != nil || len(stopped) != 0 || len(inv.opened) != 0 {
		t.Fatalf("stop = %v, %v, opened %v; want nothing", stopped, err, inv.opened)
	}
}

// The window app of this installation is closed; same-named windows of other
// installations, inaccessible ones and the caller are not. A window that does
// not exit is reported.
func TestStopInstallationProcessesMatchesPath(t *testing.T) {
	inv := newInventory(t)
	ours := `C:\Users\me\AppData\Local\Programs\DispatchQ\dispatchqapp.exe`
	inv.procs[10] = &fakeProc{image: ours}
	inv.procs[11] = &fakeProc{image: `C:\Users\me\beta\DispatchQ\dispatchqapp.exe`}
	inv.procs[12] = &fakeProc{image: ours, imageErr: errors.New("access denied")}
	inv.openErr[13] = errors.New("access denied")
	stopped, err := stopInstallationProcesses([]uint32{10, 11, 12, 13, 14, selfPID}, selfPID, []string{ours}, inv.open, sameFold, time.Second)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(stopped) != 1 || stopped[0] != 10 {
		t.Fatalf("stopped = %v, want [10]", stopped)
	}
	inv.assertNotKilled(11, 12, 13, 14, selfPID)

	inv = newInventory(t)
	inv.procs[10] = &fakeProc{image: ours}
	inv.neverExits = true
	stopped, err = stopInstallationProcesses([]uint32{10}, selfPID, []string{ours}, inv.open, sameFold, time.Second)
	if err == nil || len(stopped) != 0 {
		t.Fatalf("stop = %v, %v; want a did-not-exit error", stopped, err)
	}
}

// Uninstall leaves the autostart entry alone when it may not or cannot stop
// the daemon, and removes it only after the daemon is gone.
func TestUninstallRefusalChangesNothing(t *testing.T) {
	refusal := &foreignDaemonError{Home: homeA, PID: 300, Image: betaImage, Want: []string{ourImage}}
	for name, tc := range map[string]struct {
		windowsErr, daemonErr error
	}{
		"foreign owner":      {daemonErr: refusal},
		"unverified owner":   {daemonErr: &unverifiedDaemonError{Home: homeA}},
		"window not stopped": {windowsErr: errors.New("did not exit")},
	} {
		t.Run(name, func(t *testing.T) {
			removed := false
			var out bytes.Buffer
			err := uninstallInOrder(func() error { return tc.windowsErr },
				func() ([]uint32, error) { return nil, tc.daemonErr },
				func() error { removed = true; return nil }, "DispatchQ", homeA, &out)
			if err == nil {
				t.Fatal("refusal reported as success")
			}
			if removed {
				t.Fatal("autostart entry removed although the daemon was not stopped")
			}
		})
	}
	removed := false
	var out bytes.Buffer
	if err := uninstallInOrder(func() error { return nil }, func() ([]uint32, error) { return []uint32{100}, nil },
		func() error { removed = true; return nil }, "DispatchQ", homeA, &out); err != nil || !removed {
		t.Fatalf("uninstall = %v, removed %v", err, removed)
	}
	if !strings.Contains(out.String(), "pid 100") {
		t.Errorf("output %q does not name the stopped daemon", out.String())
	}
}

// sameExecutable compares files, not names: a same-named file in another
// folder is different, a second path to the same file is the same, and a path
// that cannot be examined matches nothing, not even itself.
func TestSameExecutable(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "one", "dispatchqd.exe")
	b := filepath.Join(dir, "two", "dispatchqd.exe")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if sameExecutable(a, b) {
		t.Error("two installations' daemons compare equal")
	}
	if !sameExecutable(a, filepath.Join(dir, "one", ".", "dispatchqd.exe")) {
		t.Error("the same file under two spellings compares different")
	}
	link := filepath.Join(dir, "link.exe")
	if err := os.Link(a, link); err == nil && !sameExecutable(a, link) {
		t.Error("a hard link to the daemon compares different")
	}
	missing := filepath.Join(dir, "missing", "dispatchqd.exe")
	if sameExecutable(missing, a) || sameExecutable(missing, missing) {
		t.Error("a missing path matches")
	}
}
