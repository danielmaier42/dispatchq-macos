# DispatchQ on Linux

Status: in progress. The port follows the direction of `docs/windows/PLAN.md` (#151): one repository, build constraints and small interfaces around operating-system operations, no lost macOS behavior.

## Goal

Run the daemon, the CLI, the dashboard and the native window on Linux desktops with systemd, and ship them as a `.deb` and an AppImage. The queue, the providers, the data formats and the dashboard stay shared with macOS.

## Decisions

| Topic | Decision | Reason |
| --- | --- | --- |
| Platform split | `cmd/dispatchqd/platform_<os>.go` wires each system's services into one `platform` struct. A missing service is a nil field, and the daemon skips it. | One place per system. Windows adds `platform_windows.go`. |
| Autostart | systemd user service with `Restart=always`, `KillMode=mixed` and `TimeoutStopSec=60`. | The daemon winds down its runs itself within `engine.ShutdownGrace` (30 s). systemd signals only the daemon, as launchd does. |
| Session scope | The service runs while the user is logged in. | It needs the user's provider logins and the desktop session. Running after logout is a separate decision. |
| Data directory | `$XDG_DATA_HOME/dispatchq`, by default `~/.local/share/dispatchq`. | Run logs, history and artifacts are data, not configuration. |
| Sleep prevention | `systemd-inhibit --what=idle:sleep --mode=block` from `system.DefaultSleepInhibitor()`, held for as long as runs are in flight. The daemon and `dispatchq run-now` share it. | A user service gets the logind block lock without a prompt (tested on Ubuntu, systemd 259). |
| Wake from sleep | Not offered. | `rtcwake` and `WakeSystem=` need root. A privileged helper would be its own change. |
| Notifications | `notify-send`. | It talks to every freedesktop notification server. No new Go dependency. |
| Dialogs | `kdialog` on KDE Plasma, `zenity` elsewhere. | Synchronous programs, like the `osascript` dialogs on macOS, and testable with a fake runner. |
| Terminal | `xdg-terminal-exec`, then Konsole or GNOME Terminal, then `x-terminal-emulator`, started through `systemd-run --user`. | A transient unit keeps the terminal open when the daemon's service restarts. |
| Self-update and uninstaller | Not from the app until packages exist. | There is no release installer for Linux yet. |
| Packaging | `.deb` and AppImage. | Requested for the first Linux release. |

## Phases

| # | Phase | Status | Notes |
| --- | --- | --- | --- |
| 1 | Platform layer, no macOS behavior change | ✅ | Upstream draft PR #159. |
| 2 | Linux services for the daemon, dashboard wording, Linux CI job | ✅ | This document's PR. Verified with a real systemd user service: install, run, sleep lock, notification, crash restart, uninstall. |
| 3 | Native window (webview on GTK/WebKitGTK) | ⏳ | Notification clicks and the window's appearance belong here. |
| 4 | Packaging: `.deb` and AppImage, release workflow, update and uninstall paths | ⏳ | WebKitGTK is large and spawns helpers from fixed paths. The AppImage may depend on the system's `libwebkit2gtk-4.1`. |

## Known gaps

- Text that the agents read still names macOS paths: the system prompt of every run (`internal/executor`) names `~/Library/Application Support/dispatchq` as the fallback data directory, and the feedback assistant describes DispatchQ as a macOS app. A run always gets `DISPATCHQ_HOME`, so the fallback is never used.
- A desktop notification has no click action yet. Clicking it does nothing (phase 3).
- The notification icon `dispatchq` exists only once a package installs it (phase 4).
- A sleep-lock helper that dies during a run is not noticed until all runs end. macOS has the same gap with `caffeinate`.
- Untested on GNOME: its power daemon may suspend an idle machine although dispatchq holds the logind `idle:sleep` lock. KDE Plasma was tested. Test on GNOME before the first release, and add `gnome-session-inhibit` if needed.
- Untested live: the folder and save dialogs and the terminal. Unit tests cover their commands, and `systemd-run --user --wait --pipe` was checked to pass output, exit status and the session's display through.
