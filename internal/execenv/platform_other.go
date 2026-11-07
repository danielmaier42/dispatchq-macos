//go:build !windows

package execenv

import "github.com/danielmaier42/claudeq/internal/provider"

// Platform returns nil: WSL exists only for DispatchQ for Windows, so every
// provider elsewhere is native.
func Platform(string) provider.WSLService { return nil }
