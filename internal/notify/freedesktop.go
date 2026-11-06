package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/danielmaier42/claudeq/internal/system"
)

// FreeDesktop posts desktop notifications on Linux through `notify-send`,
// which talks to the desktop's notification server (org.freedesktop.Notifications).
// It works from a systemd user service, which shares the session bus.
type FreeDesktop struct {
	Runner system.Runner
}

// freeDesktopIcon is the icon name the Linux packages install.
const freeDesktopIcon = "dispatchq"

// Notify displays the notification. The title is plain text. The servers of
// GNOME and KDE read the body as simple markup, so its markup characters are
// escaped.
func (f FreeDesktop) Notify(ctx context.Context, n Notification) error {
	out, err := f.Runner.Run(ctx, "notify-send",
		"--app-name=DispatchQ", "--icon="+freeDesktopIcon,
		"--", n.Title, escapeMarkup(n.Message))
	if err != nil {
		return fmt.Errorf("notify-send: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
