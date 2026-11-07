//go:build unix

// Command dispatchq-wsl is DispatchQ's helper inside a WSL distribution. The
// Windows daemon starts it through wsl.exe; see internal/wslhelper and
// internal/wslproto. It is built for linux/amd64 and linux/arm64 and shipped
// in both Windows installers.
package main

import (
	"os"

	"github.com/danielmaier42/claudeq/internal/wslhelper"
)

func main() { os.Exit(wslhelper.Main()) }
