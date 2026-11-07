//go:build windows

package execenv

import (
	"os"
	"path/filepath"

	"github.com/danielmaier42/claudeq/internal/provider"
)

// Platform returns DispatchQ for Windows' WSL service for the data folder
// home. Creating it starts nothing; wsl.exe runs only when a WSL provider is
// configured or used.
func Platform(home string) provider.WSLService {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	dir := ""
	if self, err := os.Executable(); err == nil {
		dir = filepath.Join(filepath.Dir(self), "wsl")
	}
	return &Service{
		Exe:       filepath.Join(root, "System32", "wsl.exe"),
		HelperDir: dir,
		Owner:     OwnerFor(home),
	}
}
