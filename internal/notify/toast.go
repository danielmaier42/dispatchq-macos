package notify

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/danielmaier42/claudeq/internal/system"
)

// WindowsAppID is the AppUserModelID DispatchQ shows its Windows notifications
// under. Windows attributes a toast to the app that owns this ID: the Start menu
// shortcut that carries it gives the name and icon in the toast and the entry in
// Settings > System > Notifications. The window app sets it on its own process
// so a pinned taskbar shortcut and the running window stay one button.
const WindowsAppID = "DispatchQ.DispatchQ"

// WindowsToastActivatorCLSID is the stub System.AppUserModel.ToastActivatorCLSID
// the Start menu shortcut carries next to WindowsAppID. No COM server is
// registered for it: Microsoft documents this "No COM / Stub CLSID" option for
// unpackaged apps so the notification center keeps their toasts, with protocol
// activation as the only supported click action ("Activating app notifications
// from desktop apps", Microsoft Learn, 2025-02-27). It must never change, or
// existing shortcuts are rewritten and Windows sees another activator.
const WindowsToastActivatorCLSID = "{F77FD78B-58B9-495F-85A9-F76722863164}"

// Toast limits. A toast shows a few lines, and the whole script travels on the
// PowerShell command line (at most 32767 UTF-16 units, base64 twice over), so
// longer text is shortened with an ellipsis. The inbox keeps the full text.
const (
	toastTitleMax   = 200
	toastMessageMax = 1000
)

// toastProbeTTL is how long a probed notification setting is trusted before the
// next status read starts a fresh probe in the background.
const toastProbeTTL = 2 * time.Minute

// toastRegisterRetry spaces out repair attempts of a failed registration, so a
// missing Start menu folder does not cost a COM round trip on every send.
const toastRegisterRetry = time.Minute

// toastCommandMax is the longest command line the toast may use, in UTF-16
// units. CreateProcess takes at most 32767 including the terminating NUL; the
// program is counted as a full MAX_PATH path (see commandLineLength) so the
// budget holds however Windows resolves powershell.exe.
const toastCommandMax = 32767 - 1

// toastProgramBudget is what commandLineLength counts for the program name.
const toastProgramBudget = 260

// Toast posts Windows toast notifications through the Windows Runtime
// ToastNotificationManager, which Windows PowerShell 5.1 (powershell.exe, part
// of every Windows 10 and 11 installation) can call without a compiled helper.
// The toast XML is built and escaped here and reaches the script as base64, so
// no notification text is ever PowerShell or XML source.
//
// A toast only appears for a registered AppUserModelID (see Register). Show
// itself reports no error for a notifier Windows will not display, so every
// send first reads the notifier's Setting and fails with the reason instead of
// claiming a delivery it already knows will not reach the screen. A nil error
// means Windows accepted the toast with notifications enabled for DispatchQ;
// Focus/Do not disturb or the per-app banner setting can still keep the banner
// from popping up. Notification-center persistence needs Windows acceptance.
//
// The shortcut carries a stub ToastActivatorCLSID without a COM server.
// Microsoft's archived no-COM guidance supports protocol activation only.
// A web-linked toast uses protocol activation to open the default browser.
// A linkless toast uses default foreground activation, outside that documented
// stub activation path. Its click behavior and notification-center persistence,
// including when an oversized URL is omitted, require Windows acceptance.
type Toast struct {
	Runner system.Runner
	// AppID is the AppUserModelID; empty uses WindowsAppID.
	AppID string
	// Register creates or repairs the per-user registration: the Start menu
	// shortcut carrying AppID and the stub activator CLSID. It may block on
	// COM and the file system; it never runs under the status lock. Nil
	// treats the app as registered.
	Register func() error
	// Now is the clock for the status cache and retry spacing; nil uses time.Now.
	Now func() time.Time

	// regMu guards the registration state. Register itself runs outside
	// every lock; regDone is closed when the attempt in flight finishes.
	regMu      sync.Mutex
	registered bool
	regErr     error
	regAt      time.Time
	regDone    chan struct{}

	// mu guards the cached status; it is never held across external work.
	mu       sync.Mutex
	status   string
	statusAt time.Time
	probing  bool
}

// ErrToastDisabled is returned when Windows is set not to show DispatchQ's
// notifications. The error text names the setting to change.
var ErrToastDisabled = errors.New("windows notifications are turned off for DispatchQ")

// Notify shows n as a Windows toast. It fails, with a reason the daemon log
// carries, when the app is not registered or notifications are switched off.
func (t *Toast) Notify(ctx context.Context, n Notification) error {
	if err := t.EnsureRegistered(ctx); err != nil {
		if ctx.Err() == nil {
			t.setStatus(AuthorizationNotDetermined)
		}
		return fmt.Errorf("windows notification: %w", err)
	}
	doc, ok := toastXMLWithin(t.appID(), n)
	if !ok {
		return errors.New("windows notification: the toast does not fit on a Windows command line")
	}
	setting, perr := t.run(ctx, doc)
	if perr == nil || errors.Is(perr, ErrToastDisabled) {
		t.setStatus(authorizationFromSetting(setting))
	}
	if perr != nil {
		return fmt.Errorf("windows notification: %w", perr)
	}
	return nil
}

// EnsureRegistered runs Register unless an earlier call succeeded. Concurrent
// callers share one attempt; each waits for it only as long as its ctx allows,
// while the attempt itself runs to completion in the background. A failure is
// retried at most once a minute and its error is kept for the status.
func (t *Toast) EnsureRegistered(ctx context.Context) error {
	if t.Register == nil {
		return nil
	}
	for {
		t.regMu.Lock()
		if t.registered {
			t.regMu.Unlock()
			return nil
		}
		if done := t.regDone; done != nil {
			t.regMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return fmt.Errorf("register DispatchQ for Windows notifications: %w", ctx.Err())
			}
		}
		now := t.now()
		if t.regErr != nil && now.Sub(t.regAt) < toastRegisterRetry {
			err := t.regErr
			t.regMu.Unlock()
			return err
		}
		done := make(chan struct{})
		t.regDone, t.regAt = done, now
		t.regMu.Unlock()
		go func() {
			err := t.Register()
			t.regMu.Lock()
			if err != nil {
				t.regErr = fmt.Errorf("register DispatchQ for Windows notifications: %w", err)
			} else {
				t.registered, t.regErr = true, nil
			}
			t.regDone = nil
			t.regMu.Unlock()
			close(done)
		}()
	}
}

// Status reports whether Windows shows DispatchQ's notifications, in the
// values of MacAuthorization: AuthorizationAllowed when the notifier setting is
// Enabled, AuthorizationDenied when the user, the system or a policy turned
// them off, AuthorizationNotDetermined when DispatchQ is not registered, and
// AuthorizationUnknown before the first probe or when it failed. It never
// blocks: a stale answer starts a probe in the background and is returned
// meanwhile.
func (t *Toast) Status() string {
	t.mu.Lock()
	stale := t.statusAt.IsZero() || t.now().Sub(t.statusAt) >= toastProbeTTL
	status := t.status
	if stale && !t.probing {
		t.probing = true
		go t.probe()
	}
	t.mu.Unlock()
	if status == "" {
		return AuthorizationUnknown
	}
	return status
}

// probe reads the notifier's setting without showing anything.
func (t *Toast) probe() {
	defer func() {
		t.mu.Lock()
		t.probing = false
		t.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := t.EnsureRegistered(ctx); err != nil {
		if ctx.Err() == nil {
			t.setStatus(AuthorizationNotDetermined)
		}
		return
	}
	setting, perr := t.run(ctx, "")
	switch {
	case perr == nil, errors.Is(perr, ErrToastDisabled):
		t.setStatus(authorizationFromSetting(setting))
	default:
		t.setStatus(AuthorizationUnknown)
	}
}

// run executes toastScript for doc (empty only probes) in Windows PowerShell.
func (t *Toast) run(ctx context.Context, doc string) (int, error) {
	args := toastCommand(t.appID(), doc)
	out, err := t.Runner.Run(ctx, args[0], args[1:]...)
	return parseToastResult(out, err)
}

// toastCommand is the program and arguments that run toastScript.
func toastCommand(appID, doc string) []string {
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive",
		"-EncodedCommand", encodeUTF16Base64(toastScript(appID, doc))}
}

// commandLineLength is the length, in UTF-16 units without the NUL, of the
// command line Windows builds from args (arguments joined by spaces, each
// quoted when it holds a space, tab or quote). The program counts as
// toastProgramBudget, the longest path it may be resolved to.
func commandLineLength(args []string) int {
	n := toastProgramBudget
	for _, a := range args[1:] {
		n += 1 + len(utf16.Encode([]rune(a)))
		if strings.ContainsAny(a, " \t\"") || a == "" {
			n += 2 + strings.Count(a, `"`) + strings.Count(a, `\`)
		}
	}
	return n
}

// toastXMLWithin builds the toast for n so that its command stays within
// toastCommandMax. When n's link is what makes it too long, the toast is
// shown without the link (never with a shortened one, which would be another
// address); title and message are already bounded so they always fit. The
// inbox keeps the link. ok is false only when even that does not fit.
func toastXMLWithin(appID string, n Notification) (doc string, ok bool) {
	doc = toastXML(n)
	if commandLineLength(toastCommand(appID, doc)) <= toastCommandMax {
		return doc, true
	}
	n.URL = ""
	doc = toastXML(n)
	return doc, commandLineLength(toastCommand(appID, doc)) <= toastCommandMax
}

func (t *Toast) setStatus(s string) {
	t.mu.Lock()
	t.status, t.statusAt = s, t.now()
	t.mu.Unlock()
}

func (t *Toast) appID() string {
	if t.AppID == "" {
		return WindowsAppID
	}
	return t.AppID
}

func (t *Toast) now() time.Time {
	if t.Now == nil {
		return time.Now()
	}
	return t.Now()
}

// NotificationSetting values of Windows.UI.Notifications.NotificationSetting.
const (
	settingEnabled                = 0
	settingDisabledForApplication = 1
	settingDisabledForUser        = 2
	settingDisabledByGroupPolicy  = 3
	settingDisabledByManifest     = 4
)

// authorizationFromSetting maps a NotificationSetting onto the shared status
// values the dashboard reads.
func authorizationFromSetting(setting int) string {
	switch setting {
	case settingEnabled:
		return AuthorizationAllowed
	case settingDisabledForApplication, settingDisabledForUser, settingDisabledByGroupPolicy:
		return AuthorizationDenied
	case settingDisabledByManifest:
		return AuthorizationNotDetermined
	default:
		return AuthorizationUnknown
	}
}

// settingReason says, for a disabled setting, where the operator turns it back on.
func settingReason(setting int) string {
	switch setting {
	case settingDisabledForApplication:
		return "turn DispatchQ on in Settings > System > Notifications"
	case settingDisabledForUser:
		return "notifications are off for this Windows user; turn them on in Settings > System > Notifications"
	case settingDisabledByGroupPolicy:
		return "an administrator turned notifications off by group policy"
	case settingDisabledByManifest:
		return "Windows does not know DispatchQ as an app that may notify; reinstall DispatchQ to repair its Start menu shortcut"
	default:
		return fmt.Sprintf("Windows reports notification setting %d", setting)
	}
}

// toastXML builds the toast content: a ToastGeneric binding with the title and
// the message. Every value goes through XML escaping, which also replaces
// characters XML cannot carry. A web link becomes the toast's protocol
// activation, so clicking the toast opens it in the default browser. Without
// one the toast names no activation: with the stub activator CLSID only
// protocol activation is supported, so such a click opens nothing.
func toastXML(n Notification) string {
	var b strings.Builder
	b.WriteString(`<toast`)
	if n.URL != "" && IsWebURL(n.URL) {
		b.WriteString(` activationType="protocol" launch="`)
		b.WriteString(xmlEscape(n.URL))
		b.WriteString(`"`)
	}
	b.WriteString(`><visual><binding template="ToastGeneric"><text hint-maxLines="1">`)
	b.WriteString(xmlEscape(truncateRunes(n.Title, toastTitleMax)))
	b.WriteString(`</text><text>`)
	b.WriteString(xmlEscape(truncateRunes(n.Message, toastMessageMax)))
	b.WriteString(`</text></binding></visual></toast>`)
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// truncateRunes shortens s to at most limit characters, ending in an ellipsis
// when it was cut. It counts runes, so a multi-byte character is never split.
func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimRightFunc(string(r[:limit-1]), func(c rune) bool { return c == ' ' }) + "…"
}

// toastScript is the PowerShell that reads the notifier's setting and, when doc
// is not empty, shows doc as a toast. It answers on stdout with one line:
// "ok <setting>" when it showed the toast or only probed, and
// "disabled <setting>" when Windows is set not to show it. Any exception ends
// the script with a non-zero exit and the message on stdout.
func toastScript(appID, doc string) string {
	return fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
function Decode([string]$b) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)) }
try {
  $null = [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]
  $null = [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]
  $notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier((Decode '%s'))
  $setting = [int]$notifier.Setting
  if ($setting -ne 0) { [Console]::Out.Write("disabled $setting"); exit 0 }
  $doc = Decode '%s'
  if ($doc) {
    $xml = New-Object Windows.Data.Xml.Dom.XmlDocument
    $xml.LoadXml($doc)
    $notifier.Show((New-Object Windows.UI.Notifications.ToastNotification $xml))
  }
  [Console]::Out.Write("ok $setting")
} catch {
  [Console]::Out.Write("error " + $_.Exception.Message)
  exit 1
}
`, b64UTF8(appID), b64UTF8(doc))
}

// parseToastResult reads the script's answer. It returns the notifier setting
// and an error when the toast was not shown: ErrToastDisabled (wrapped with
// the place to turn it back on) or the failure PowerShell reported.
func parseToastResult(out []byte, runErr error) (int, error) {
	text := strings.TrimSpace(string(out))
	if runErr != nil {
		if text == "" {
			return -1, fmt.Errorf("powershell: %w", runErr)
		}
		return -1, fmt.Errorf("powershell: %w (%s)", runErr, strings.TrimPrefix(text, "error "))
	}
	word, rest, _ := strings.Cut(text, " ")
	setting, convErr := strconv.Atoi(strings.TrimSpace(rest))
	switch {
	case word == "ok" && convErr == nil:
		return setting, nil
	case word == "disabled" && convErr == nil:
		return setting, fmt.Errorf("%w: %s", ErrToastDisabled, settingReason(setting))
	default:
		return -1, fmt.Errorf("powershell: unexpected answer %q", text)
	}
}

func b64UTF8(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// encodeUTF16Base64 is the form -EncodedCommand takes: base64 of UTF-16LE.
func encodeUTF16Base64(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
