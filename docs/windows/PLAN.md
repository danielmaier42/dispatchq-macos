# DispatchQ Windows port and shared application repository

Status: a native Windows beta for x64 and ARM64 is implemented on `feature/windows-port-poc` (PR 151; the branch keeps its earlier name). It supersedes the earlier "implementation deferred" note and the proof of concept it grew from; the maintainer asked for it explicitly. Native Windows needs no WSL and starts no `wsl.exe` unless a provider is set to run in WSL. It is built by `scripts/build-windows.sh`, locally or by the CI workflow's `windows-installer` job, which runs the script on macOS for x64 and ARM64 on every pull request and `main` push, checks the PE machine types, checksum and `BUILD-INFO.json` against the checked-out commit, and keeps each installer as a 14-day workflow artifact for beta testers. It is never released or advertised by GitHub Releases, the release workflow or the website. No repository rename, base-repo extraction, relay or iOS work is part of it.

### Beta status (2026-11-07)

| Area | State |
| --- | --- |
| Store and daemon locks | Implemented with `LockFileEx` (lock byte placed past EOF so the pid stays readable); atomic replace retries on sharing violations. Not run on Windows. |
| Process tree | Each run is created suspended, assigned to a Job Object with kill-on-close, then resumed, so descendants started at once are members; if ownership cannot be established the run fails to start and is stopped. Cancel ends the job. Assistant and prompt-review asides, readiness/model probes and the Codex app server go through the same `procown.Start`, so a cancelled or expired request ends their trees too, and a process that escaped the tree cannot hold the request open past a bounded output drain. No console windows. Windows fixtures (immediate descendants, cancel, abrupt owner death, interrupt order, release, start failure) are written and cross-compiled but have not run on Windows. |
| Provider discovery | `.exe`/`.cmd` candidates (own installers, npm, scoop), then `PATH`; unit-tested. `.cmd`/`.bat` launchers are resolved to the node program or `.exe` they start and run directly, so prompts reach the provider as data; a shim that needs `cmd.exe` is run only when no argument contains a character cmd would interpret (`" % ^ & \| < > ( ) !`, line breaks), otherwise the run fails with an explanation. Same path for runs, readiness/model/limit probes and asides. The shim logic is unit-tested on macOS; real provider runs on Windows are unverified. |
| Native window | WebView2 via the existing `webview_go` (built-in loader, no DLL to ship); links, accent colour. Cross-compiled only; never opened on Windows. |
| Platform layer | Built on the platform abstraction Lukas Wolfrum (@Kryolisc) contributed in [PR 159](https://github.com/danielmaier42/dispatchq-app/pull/159), adopted in full: the abstraction (merged at `2bcaac0034d8b2f28e17fd56bd6d27033b7537aa`) and the contributor's later run-now sleep fix (`c2a54d6`, PR 159's current head; its files are identical here or carried with the same behaviour), so PR 151 no longer waits for a separate PR 159 merge. Windows fills the `platform` bundle in `cmd/dispatchqd/platform_windows.go`: Run-key `serviceManager`, `stopStrayDaemons` (stops only the processes the Restart Manager reports as having the data directory's daemon lock file open, matched by process instance (pid and start time) and executable path; another installation or `DISPATCHQ_HOME` is reported, never killed; the installer stops the old installation with a copy of the new `dispatchqd stop-installation` before it changes anything, aborts with the reason on refusal and never uses `taskkill /IM`), PowerShell folder/save dialogs, OS version; `platformInit` (GUI-subsystem log redirect) and `exeSuffix` stay per-OS functions. The sleep hold is `system.DefaultSleepInhibitor` (`internal/system/sleep_*.go`): thread-pinned `SetThreadExecutionState` on Windows, `caffeinate -i` on macOS, none elsewhere. The daemon and `dispatchq run-now` both wire it. `platform_other.go` (`!darwin && !windows`) is the explicit unsupported fallback for Linux and other systems. Cross-compiled and unit-tested on macOS only. |
| Native dialogs | Folder chooser (task working directory, data folder) and "Save as" (export) are Windows Forms dialogs shown by PowerShell (`internal/api/folder_powershell.go`). Background commands run through `system.Real`, which starts a child with `CREATE_NO_WINDOW` and `HideWindow` (`STARTF_USESHOWWINDOW` + `SW_HIDE`). Windows applies that `wShowWindow` to the first window a process shows, so the dialog opened invisible and the dashboard spun until the 3-minute timeout (reported on the maintainer's laptop: New Task > Choose spins, no dialog). Dialogs now ask `system.ShowingWindows(r)` for `system.Interactive`, which keeps `CREATE_NO_WINDOW` (no console) but does not start the child hidden; every other command keeps `NoWindow`. A Windows-only test starts the test binary through both runners and reads its own `STARTUPINFO` and console; it is cross-compiled, not yet run on Windows. Whether the dialog appears in front of the browser is unverified until tested on the laptop. |
| Daemon lifecycle | Run-key autostart and start-now in `dispatchqd install`, log file, single daemon. Behaviour at lock, logout and reboot unverified. |
| Architectures | x64 and ARM64, each a separate installer from `scripts/build-windows.sh --arch amd64\|arm64` (default amd64, `x64` accepted). ARM64 cross-compiles the daemon and CLI with Go and the window's cgo/C++ WebView2 part and icon resource with the official llvm-mingw toolchain (pinned release, checksum-verified download into `build/windows/cache`). The build fails unless all three programs carry the target PE machine type (0x8664 / 0xaa64). ARM64 window: statically linked C++ runtime, imports only Windows system DLLs and the UCRT, and the built-in WebView2 loader selects the runtime's `EBWebView\arm64` client. Both installers use NSIS's x86 stub (no ARM64 stub exists), emulated on Windows on ARM; the ARM64 installer refuses non-ARM machines, the x64 one refuses 32-bit Windows. The architecture mapping, PE check and installer guards are unit-tested on macOS. The ARM64 build has never run on Windows. |
| Installer | NSIS per-user installer with WebView2 bootstrapper (it re-reads the runtime's registration afterwards and, when it is still missing, shows the bootstrapper's result and the download link; the rest of the installation completes), PATH entry, uninstaller, `/PURGE`. The install directory reaches the PATH script (`path-helper.ps1`) through an environment variable, never as PowerShell source; helper exit codes are checked and reported. The script is round-trip tested under PowerShell on macOS with `$`, backtick, space, quote, `%` and Unicode paths. The installer itself was built, never executed on Windows. |
| Native notifications | `dispatchq notify` and run outcomes raise a WinRT toast through Windows PowerShell 5.1 (`internal/notify/toast.go`); the daemon registers the per-user Start menu shortcut's AppUserModelID and a stub ToastActivatorCLSID at start, outside every lock, and status reads never wait for it; the encoded command line is bounded and an oversized link is dropped. Settings and the warning banner open `ms-settings:notifications` through a fixed native binding. Unit-tested on macOS, compiled for both architectures. Not verified on Windows: visible delivery, linkless toast click behaviour and notification-center persistence (linkless default activation is outside Microsoft's archived stub-CLSID guidance). |
| Disabled and documented | Wake from sleep, continue in terminal, in-app update and uninstall, appearance pinning, shebang scripts. |
| Providers in WSL | Implemented, unrun on WSL. Optional per provider (`[providers.execution] type = "wsl"`, distro, optional user), fixed after creation; absent is native, so existing configurations stay native. Every provider process and file access goes through `provider.Host` (runs, readiness, models, limits, asides, Codex app server); `NativeHost` is the previous code, and a test with a recording fake `wsl.exe` proves native providers never start it. `internal/execenv` starts `wsl.exe` with a data-free argument list and the Linux helper `dispatchq-wsl` (`internal/wslhelper`, both architectures in both installers, picked by the distribution's `uname -m`, ELF machine checked); arguments, environment and input travel as frames (header, data, end, cancel, ping) on the helper's standard input. Cancel stops the Linux tree through the helper; a 15 s liveness lease and channel EOF cover a dead daemon; the start-up reap touches only this installation's records whose helper is provably gone (start time checked). Pools and fallbacks skip providers that cannot open the task's folder; WSL runs self-queue through interop to the Windows CLI. Tested on macOS against a fake `wsl.exe` running the real helper code. Not verified: `wsl.exe` EOF and exit-code forwarding, distro start latency, interop from a WSL run, ARM64 distributions, WSL 1. Script jobs stay native. |
| Open | Running the ARM64 build on Windows on ARM, running WSL providers on real WSL, execution after logout, code signing, updater, Windows tests on a Windows runner (CI only cross-builds), website selection (planned until publication), real Windows end-to-end verification. |

Verification gap: no Windows machine or VM was available to the implementer (no Parallels/UTM/VirtualBox, no Wine). An x64 build is now installed on the maintainer's physical Windows laptop; it appears to work and testing continues, so individual items above are still unverified until that testing reports them. No Windows on ARM device has run the ARM64 build. Everything above marked unrun needs the first-install checklist in `scripts/windows/INSTALL-WINDOWS.txt` on a Windows 11 PC of each architecture.

Created: 2026-11-05.

## Goal

Publish a native Windows edition of DispatchQ while retaining the existing macOS application. Reuse the queue, scheduling, provider adapters, dashboard and data formats. Preserve reliable unattended execution, cancellation and recovery on both platforms.

The preferred repository direction is to rename the existing `dispatchq-macos` repository to `dispatchq-app` and build both editions there. Confirm this tentative direction before performing the external rename. A separate base repository is the alternative, not an additional extraction step.

## Scope and boundaries

- This branch contains documentation only. The existing root `PLAN.md` remains the macOS implementation history.
- Future implementation covers the daemon, CLI, native window, operating-system integrations, installer, updater, CI and user documentation.
- Reuse the current WebView dashboard. Do not replace the UI framework merely to support Windows.
- Preserve existing macOS install locations, binary names, LaunchAgent identity, data and backward compatibility unless a separate migration is explicitly required.
- Native Windows and WSL are different execution environments. A native application does not imply that WSL providers, Unix scripts or macOS MCP configurations work automatically.
- Proposed initial baseline: Windows 11 x64 (ARM64 builds exist locally, unverified on hardware), native providers, background execution in the signed-in user's session. Windows ARM64, Windows 10, WSL execution and execution after logout need explicit scope decisions.
- Relay and iOS remain separate projects and are not prerequisites for this port.

## Feasibility evidence

Assessment against `origin/main` at `6cb461b`, on 2026-11-05:

- Most product logic is Go; the dashboard is a separate web frontend.
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` was attempted with local Go 1.27.1. It failed at `syscall.Flock` and Unix lock constants in `internal/store/lock.go` and `internal/store/daemonlock.go`. This establishes a build blocker, not a complete inventory of compiler failures. CI currently selects Go 1.26.
- `cmd/dispatchqapp/main_other.go` is a stub that exits with a macOS-only message. It is not a Windows UI.
- Process cancellation and update-process detachment have Unix-only implementations. Replacing compiler errors alone cannot establish correct Windows execution.
- The current WebView dependency supports Windows, but the pinned version, C/C++ build requirements and WebView2 runtime still need a Windows build and runtime check.
- Claude Code and Codex support native Windows. OpenCode supports native Windows but recommends WSL for the best compatibility. These upstream capabilities do not verify DispatchQ's adapters.

## Repository direction

Prefer one application repository because macOS and Windows share product behavior, dashboard, provider interfaces and releases. Keep shared logic and platform implementations in that repository, using build constraints and small interfaces around operating-system operations. Use the existing code structure where possible rather than creating a second framework.

A separate base repository would add versioning and coordinated dependency updates for changes that often affect both apps. Reconsider extraction only if independently released consumers establish a concrete need.

For a future rename:

1. Inventory repository URLs in update checks, feedback links, module paths, CI, release scripts, website, wiki, agent instructions and external automation.
2. Rename the existing GitHub repository so history, issues and stars remain attached to it. Do not replace it with a newly initialized repository or reuse the old name afterward.
3. Update remotes and explicit URLs. Test an already-installed macOS release's update lookup and download through the old URLs before relying on GitHub redirects.
4. Keep the current Go module path change separate from the repository rename unless required. It still uses the historical `claudeq` name; changing it is not needed merely to build Windows.
5. Update workspace paths and authoritative instructions deliberately, preserving worktrees and concurrent sessions. Never assume that a GitHub rename renames local directories.

## Platform work

| Area | Existing dependency | Required outcome |
| --- | --- | --- |
| Store ownership and writes | Unix `flock` in store and daemon locks | Windows cross-process locks; exactly one daemon per store; safe concurrent CLI and daemon writes; tested replace/rename semantics. |
| Process lifecycle | Unix process groups, `ps`, signals | Windows process-tree ownership and termination, initially investigate Job Objects; cancellation, watchdog, shutdown and limit teardown leave no tools running. Verify interaction with each provider's sandbox. |
| Background process | LaunchAgent, `launchctl`, macOS user detection | Start and recover under the intended Windows user identity, independently of the app window; define login, lock, logout and reboot behavior. Avoid a system account that loses provider logins or user files. |
| Native window | WKWebView plus Cocoa menu, appearance, file picker and notification handlers | Windows WebView2 shell, external links, file/folder dialogs, appearance, app activation and notification navigation. Verify the existing dependency before considering a replacement. |
| Providers and accounts | Homebrew paths, login-shell discovery, Unix executable bits | Discover native `.exe` binaries and installation shims safely; handle paths with spaces, environment names and account-specific config directories. Verify health checks, model discovery, headless output, limits and resume. |
| Credentials | Claude Keychain lookup with credentials-file fallback; provider-owned logins | Inspect actual Windows storage for supported CLI versions. Verify account isolation and credential renewal without printing secrets. Use Windows access controls for temporary secrets, not Unix mode assumptions. |
| Scripts and terminal resume | `/bin/sh`, shebang execution, AppleScript, shell exports | Define explicit interpreters and PowerShell behavior; construct arguments safely; preserve secret handling and interactive session resume. Never silently translate existing Bash scripts. |
| Power management | `pmset`, `caffeinate`, sudoers | Prevent idle sleep during work and schedule wake events through Windows mechanisms; handle unsupported power states and disabled wake policies visibly. |
| Notifications and artifacts | Native macOS notification helper and click handler | Windows notifications with correct app identity and click-to-open behavior. Retain HTTP-based Pushover, ntfy and webhooks; verify links and file opening. |
| Local API and persistence | Loopback HTTP plus shared file store | Preserve local-only binding and existing request protection; platform-specific opening/dialog behavior must not broaden network exposure. |
| Machine-aware prompt review | Unix path recognition, shell command inspection and Mac wording | Recognize Windows paths and interpreters; check the actual execution environment; replace platform-specific instructions where appropriate. |
| Distribution | `.app`, `.pkg`, bundle-version inspection, `.pkg` asset selection | Windows installer, uninstall, autostart, version detection and update handover; select assets by OS and architecture; preserve macOS updates. |
| CI and tests | macOS Go job, Unix shell stubs, macOS release scripts | Windows builds and real Windows tests alongside macOS checks; portable fake providers and platform-specific tests; native runner builds for the UI. |

## Work sequence and exit criteria

All implementation phases are not started.

| Phase | Work | Exit criteria |
| --- | --- | --- |
| 0 | Revalidate baseline and settle initial support scope | Read current instructions and provider/model availability; confirm Windows baseline, native versus WSL scope and user-session behavior. Recheck code because implementation may start later. |
| 1 | End-to-end Windows feasibility spike in the existing repo | Build daemon/CLI and a minimal window; run a real native provider; cancel a job with tool descendants; verify account access and restart recovery. Use disposable fixtures. Record failures before broad refactoring. |
| 2 | Shared core and platform separation | Both platforms build; portable persistence and lifecycle implementations pass meaningful tests. No macOS behavior is lost while moving platform code. |
| 3 | Provider and desktop integration | Verify supported providers/accounts, limit handling, resume, scripts, dialogs, terminal continuation, notifications and artifact opening. Publish an explicit capability matrix. |
| 4 | Background operation and power management | Login startup and crash recovery work; closing UI leaves scheduling active; sleep/wake is exercised on Windows hardware; unsupported cases are accurately documented. |
| 5 | Installer, updater and release matrix | Clean install, upgrade, interrupted handover and uninstall are tested on Windows; macOS update regression passes; CI publishes correctly identified artifacts without partial-release confusion. |
| 6 | Repository rename, documentation and launch | Confirm rename timing, verify old update URLs, update website and wiki, provide installable builds and complete cross-platform acceptance checks before advertising Windows availability. |

The spike is a feasibility checkpoint. Do not commit to full parity or a release date from a successful crosscompile alone. A repository rename can be coordinated earlier if explicitly decided, but is not required for the spike.

## Verification matrix

- Fresh Windows user profile without developer-specific PATH customizations; installation directories and repositories containing spaces and non-ASCII characters.
- Supported native provider versions, separate accounts/config directories, missing login, expired login, malformed output and unavailable CLI.
- Real headless execution, structured output, session resume, rate-limit waiting, provider fallback and cancellation while a tool is running.
- Parallel tasks, concurrent CLI/UI writes and a second daemon attempting to own the same store.
- Script interpreters, CRLF input, stdin dependencies, exit codes, terminal continuation and prompt path checks.
- Locked desktop, closed window, process crash, login startup, reboot recovery and documented logout behavior.
- Idle-sleep prevention and scheduled wakes on actual Windows hardware. A VM is useful for installer tests but does not establish hardware wake reliability.
- Clean install, upgrade while idle and during work, update failure recovery, asset selection, uninstall with and without retained data.
- Website download selection on Windows and macOS, manual selection of the other platform, and a usable platform chooser on mobile, unknown systems and with JavaScript disabled. Verify that every button links to the matching available installer and installation guide.
- Notification clicks during app startup and while running; folder/file opening and `.dispatchq` import/export with incompatible paths reported clearly.
- Existing macOS build, vet, lint, race tests, app behavior and installer/update verification remain required for implementation changes. Follow the toolchain pinned by CI.

Regression tests added during implementation must fail without the fix and pass with it. A successful provider version check is not a successful agent run.

## Cross-repository work

- Website: Detect the visitor's operating system and show the matching primary download button: the Windows installer on Windows and the macOS installer on macOS. Always provide an explicit choice of both platforms. On mobile, unsupported or unrecognized systems, or when detection is unavailable, show a neutral platform chooser instead of assuming a platform. Use the corresponding release asset and installation guide; never start a download automatically. Include platform-specific prerequisites, screenshots, release metadata and accurate feature claims. Keep macOS downloads working throughout.
- Wiki: shared platform responsibilities, capability matrix, compatibility policy, repository links and workspace structure. Edit authoritative guidelines, not their symlinks or copied files.
- Relay/iOS planning: represent the execution host as an environment that may be macOS or Windows. Preserve credential ownership on that host. Do not implement relay/iOS as part of the desktop port.
- Existing users and automation: keep installed macOS paths and CLI contracts stable; document that job prompts, scripts, working directories and external integrations can require platform adaptation.

## Decisions to settle when implementation resumes

1. Confirm the preferred single `dispatchq-app` repository and rename timing.
2. Confirm Windows 11 x64 as the first target and whether WSL or ARM64 belongs in the first release.
3. Confirm whether the daemon only needs to run while the user remains signed in. Running after logout is a separate service and credential requirement.
4. Select the installer/update format and signing route after a Windows prototype. Check current provider eligibility and distribution requirements before recommending a signing service.
5. Identify available Windows test hardware for native providers and sleep/wake validation; define any first-release capability differences explicitly.

These are future implementation decisions, not requests to start work now. Follow the current project model-selection and worktree rules when resuming; resolve live provider availability rather than pinning today's model or account.

## References

Upstream documentation consulted during the feasibility assessment; recheck it when implementation starts:

- [Claude Code Windows setup](https://code.claude.com/docs/en/setup)
- [Codex Windows sandbox](https://learn.chatgpt.com/docs/windows/windows-sandbox)
- [OpenCode on Windows and WSL](https://opencode.ai/docs/windows-wsl)
- [Go WebView bindings](https://github.com/webview/webview_go)
- [Windows wake-up events](https://learn.microsoft.com/en-us/windows/win32/power/system-wake-up-events)
- [Windows SmartScreen distribution considerations](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation)
- [GitHub repository renaming](https://docs.github.com/en/repositories/creating-and-managing-repositories/renaming-a-repository)
