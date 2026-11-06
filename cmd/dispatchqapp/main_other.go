//go:build !darwin

// Command dispatchqapp is the native DispatchQ window. On systems without a
// window implementation it exits with a message.
package main

import (
	"fmt"
	"os"
)

// The native window targets macOS. This stub keeps non-darwin builds compiling.
func main() {
	fmt.Fprintln(os.Stderr, "dispatchqapp is macOS-only")
	os.Exit(1)
}
