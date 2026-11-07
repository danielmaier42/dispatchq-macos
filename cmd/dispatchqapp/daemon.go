package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const dashboardURL = "http://127.0.0.1:10765"

// pingURL is the endpoint the daemon probe asks. It is answered without reading
// the store, checking a provider or taking a lock, so a daemon in the middle of
// a tick answers it as fast as an idle one — unlike listing the queue, which
// the probe used to ask and which re-checks every configured harness.
const pingURL = dashboardURL + "/api/ping"

// How the app decides whether a daemon is already there. Several attempts with a
// generous timeout, because the cost of a false "no" is a second daemon on the
// same store while the cost of asking again is a few hundred milliseconds at
// startup.
const (
	daemonProbeAttempts = 3
	daemonProbeTimeout  = 2 * time.Second
	daemonProbePause    = 250 * time.Millisecond
	// daemonStartWait bounds how long the app waits for a daemon it just started.
	daemonStartWait = 5 * time.Second
)

// ensureDaemon starts dispatchqd if the dashboard isn't already responding.
func ensureDaemon() {
	if daemonReachable() {
		return
	}
	bin := "dispatchqd" + exeSuffix
	if exe, err := os.Executable(); err == nil {
		if cand := filepath.Join(filepath.Dir(exe), "dispatchqd"+exeSuffix); fileExists(cand) {
			bin = cand
		}
	}
	cmd := daemonCommand(bin)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatchqapp: could not start dispatchqd:", err)
		return
	}
	// Reap it: it may exit right away (another daemon owns the store) and must not
	// be left as a zombie for the lifetime of the window.
	go func() { _ = cmd.Wait() }()
	// Wait briefly for it to come up. Short probes, because all this waits for is
	// the port to open — a long per-probe timeout here would keep the window from
	// opening at all.
	deadline := time.Now().Add(daemonStartWait)
	for {
		if answersWithin(pingURL, 300*time.Millisecond) {
			return
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "dispatchqapp: dispatchqd did not come up; the window will be empty")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// daemonReachable reports whether a daemon already serves the dashboard. It asks
// several times: a single 400 ms probe was too tight, and a daemon busy with a
// tick (a provider check, a run starting) that missed it had the app start a
// second daemon against the same store. Nothing is lost on the answer "no" — the
// port is then refused right away rather than timing out, so an actually absent
// daemon is still detected in milliseconds.
func daemonReachable() bool { return reachable(pingURL, daemonProbeAttempts) }

// reachable polls url until something answers or the attempts run out.
func reachable(url string, attempts int) bool {
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(daemonProbePause)
		}
		if answers(url) {
			return true
		}
	}
	return false
}

func answers(url string) bool { return answersWithin(url, daemonProbeTimeout) }

// answersWithin reports whether anything answered within timeout. Any status
// counts, including the 404 of a daemon too old to know /api/ping: the question
// is whether a daemon is there at all, not what it makes of the request.
func answersWithin(url string, timeout time.Duration) bool {
	c := http.Client{Timeout: timeout}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
