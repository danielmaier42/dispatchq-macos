package main

import (
	"os"
	"runtime"

	"github.com/danielmaier42/claudeq/internal/execenv"
)

// callerGOOS is the system this CLI runs on, a variable for tests.
var callerGOOS = runtime.GOOS

// fromCaller turns a path an agent in WSL passed to DispatchQ for Windows into
// one Windows opens. A run in WSL starts this CLI through interop and hands it
// Linux paths ("/home/dm/repo/out.md"); WSL shares WSL_DISTRO_NAME and the
// daemon adds the distribution's drive mount root, so the path becomes the
// drive path under that root (/mnt/c/x is C:\x) or the WSL share
// (\\wsl.localhost\Ubuntu\home\dm\repo\out.md). Anything else is returned
// unchanged, and nothing changes outside Windows or outside a WSL run.
func fromCaller(p string) string {
	if callerGOOS != "windows" || !execenv.IsLinuxPath(p) {
		return p
	}
	distro := os.Getenv("WSL_DISTRO_NAME")
	if distro == "" {
		return p
	}
	return execenv.FromLinux(p, distro, os.Getenv("DISPATCHQ_WSL_MNT"))
}
