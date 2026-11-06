package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// dataBaseDir is the folder that holds the data directory on Linux:
// $XDG_DATA_HOME, by default ~/.local/share. Run logs, history and artifacts
// are data, not configuration, so they do not go to ~/.config.
func dataBaseDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}
