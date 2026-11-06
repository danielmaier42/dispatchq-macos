//go:build !linux

package store

import (
	"fmt"
	"os"
)

// dataBaseDir is the folder that holds the data directory:
// ~/Library/Application Support on macOS.
func dataBaseDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return base, nil
}
