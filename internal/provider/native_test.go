package provider

import (
	"io"
	"strings"
	"testing"
)

// TestHeadBufferCapsEveryWritePath: exec copies a child's stderr with
// io.Copy, which prefers a destination's ReadFrom over Write. The cap must hold
// on that path too, or a CLI flooding stderr grows the daemon's memory.
func TestHeadBufferCapsEveryWritePath(t *testing.T) {
	for name, write := range map[string]func(*headBuffer, string){
		"Write":   func(b *headBuffer, s string) { _, _ = b.Write([]byte(s)) },
		"io.Copy": func(b *headBuffer, s string) { _, _ = io.Copy(b, io.LimitReader(strings.NewReader(s), int64(len(s)))) },
	} {
		t.Run(name, func(t *testing.T) {
			b := &headBuffer{max: 10}
			write(b, "0123456")
			write(b, strings.Repeat("x", 1000))
			if got := b.String(); got != "0123456xxx" {
				t.Fatalf("kept %q", got)
			}
		})
	}
}
