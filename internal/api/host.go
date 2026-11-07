package api

import (
	"net/http"
	"runtime"
)

// hostInfo tells the dashboard which operating system the daemon runs on and
// which of its system services exist there, so the UI shows instructions that
// hold on this system (System Settings on macOS, Installed apps on Windows)
// and offers no button for a service this build lacks.
type hostInfo struct {
	// OS is runtime.GOOS: "darwin", "windows", "linux", ...
	OS string `json:"os"`
	// Uninstall reports whether POST /api/uninstall opens an uninstaller.
	Uninstall bool `json:"uninstall"`
}

// hostOS is the operating system getHost reports; tests replace it.
var hostOS = runtime.GOOS

func (s *server) getHost(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, hostInfo{OS: hostOS, Uninstall: s.d.Uninstall != nil})
}
