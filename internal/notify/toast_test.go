package notify

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// toastRunner records the PowerShell calls and answers with a fixed result.
type toastRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   string
	err   error
}

func (r *toastRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return []byte(r.out), r.err
}

func (r *toastRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// decodeScript turns the -EncodedCommand argument back into the script.
func decodeScript(t *testing.T, args []string) string {
	t.Helper()
	if len(args) < 2 || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("no -EncodedCommand in %q", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("encoded command is not base64 UTF-16: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

// scriptValues returns the base64 values the script decodes, in order.
func scriptValues(t *testing.T, script string) []string {
	t.Helper()
	var vals []string
	for _, m := range regexp.MustCompile(`Decode '([A-Za-z0-9+/=]*)'`).FindAllStringSubmatch(script, -1) {
		b, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			t.Fatalf("value %q is not base64: %v", m[1], err)
		}
		vals = append(vals, string(b))
	}
	return vals
}

// parsedToast is the part of the toast XML the tests check.
type parsedToast struct {
	ActivationType string
	Launch         string
	Template       string
	Texts          []string
}

func parseToast(t *testing.T, doc string) parsedToast {
	t.Helper()
	var raw struct {
		ActivationType string `xml:"activationType,attr"`
		Launch         string `xml:"launch,attr"`
		Binding        struct {
			Template string   `xml:"template,attr"`
			Texts    []string `xml:"text"`
		} `xml:"visual>binding"`
	}
	if err := xml.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("toast XML does not parse: %v\n%s", err, doc)
	}
	return parsedToast{raw.ActivationType, raw.Launch, raw.Binding.Template, raw.Binding.Texts}
}

// Text that is markup, PowerShell syntax or outside ASCII arrives in the toast
// exactly as sent, and none of it appears in the script's source.
func TestToastEscapesTextAndKeepsUnicode(t *testing.T) {
	title := `Build "done" <ok> & 'quoted' $(Remove-Item x) ` + "`n"
	message := "Grüße, 日本語, emoji 🚀 and ]]> plus \u2028 line"
	r := &toastRunner{out: "ok 0"}
	toast := &Toast{Runner: r}
	if err := toast.Notify(context.Background(), Notification{Title: title, Message: message}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0][0] != "powershell.exe" {
		t.Fatalf("calls = %q, want one powershell.exe", r.calls)
	}
	script := decodeScript(t, r.calls[0][1:])
	for _, raw := range []string{"Remove-Item", "Grüße", "🚀", "<ok>"} {
		if strings.Contains(script, raw) {
			t.Errorf("script source contains notification text %q", raw)
		}
	}
	vals := scriptValues(t, script)
	if len(vals) != 2 || vals[0] != WindowsAppID {
		t.Fatalf("script values = %q, want the app id then the toast", vals)
	}
	p := parseToast(t, vals[1])
	if p.Template != "ToastGeneric" || len(p.Texts) != 2 || p.Texts[0] != title || p.Texts[1] != message {
		t.Fatalf("toast = %+v, want title %q and message %q", p, title, message)
	}
	if p.Launch != "" || p.ActivationType != "" {
		t.Errorf("toast without link has activation %q %q", p.ActivationType, p.Launch)
	}
}

// Characters XML cannot carry are replaced, not passed on to break LoadXml.
func TestToastXMLReplacesInvalidCharacters(t *testing.T) {
	p := parseToast(t, toastXML(Notification{Title: "a\x00b\x1bc", Message: "x\uFFFEy"}))
	for _, s := range p.Texts {
		if strings.ContainsAny(s, "\x00\x1b\uFFFE") {
			t.Errorf("invalid XML character kept in %q", s)
		}
	}
}

// A web link becomes the protocol activation; anything else is left out.
func TestToastLink(t *testing.T) {
	link := "https://example.com/a?b=1&c=\"2\""
	p := parseToast(t, toastXML(Notification{Title: "t", Message: "m", URL: link}))
	if p.ActivationType != "protocol" || p.Launch != link {
		t.Fatalf("activation = %q %q, want protocol %q", p.ActivationType, p.Launch, link)
	}
	for _, bad := range []string{"file:///C:/Windows/System32/calc.exe", "javascript:alert(1)", "ms-settings:"} {
		if p := parseToast(t, toastXML(Notification{Title: "t", Message: "m", URL: bad})); p.Launch != "" {
			t.Errorf("link %q became launch %q", bad, p.Launch)
		}
	}
}

// Long text is cut on a character boundary with an ellipsis, and the whole
// script stays well inside the Windows command-line limit.
func TestToastTruncatesLongText(t *testing.T) {
	long := strings.Repeat("ü🚀", 5000)
	doc := toastXML(Notification{Title: long, Message: long})
	p := parseToast(t, doc)
	if n := utf8.RuneCountInString(p.Texts[0]); n != toastTitleMax {
		t.Errorf("title has %d characters, want %d", n, toastTitleMax)
	}
	if n := utf8.RuneCountInString(p.Texts[1]); n != toastMessageMax {
		t.Errorf("message has %d characters, want %d", n, toastMessageMax)
	}
	for _, s := range p.Texts {
		if !utf8.ValidString(s) || !strings.HasSuffix(s, "…") {
			t.Errorf("truncated text %q is not valid UTF-8 ending in an ellipsis", s[len(s)-8:])
		}
	}
	// Worst case: every character escaped to an entity.
	worst := strings.Repeat("&", toastTitleMax+toastMessageMax)
	cmd := encodeUTF16Base64(toastScript(WindowsAppID, toastXML(Notification{Title: worst, Message: worst})))
	if len(cmd) > 30000 {
		t.Errorf("encoded command is %d characters, too close to the 32767 limit", len(cmd))
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Errorf("short text changed to %q", got)
	}
}

func TestParseToastResult(t *testing.T) {
	for _, tc := range []struct {
		out      string
		err      error
		setting  int
		disabled bool
		fails    bool
		contains string
	}{
		{out: "ok 0", setting: 0},
		{out: "disabled 1", setting: 1, disabled: true, contains: "Settings > System > Notifications"},
		{out: "disabled 2", setting: 2, disabled: true, contains: "this Windows user"},
		{out: "disabled 3", setting: 3, disabled: true, contains: "group policy"},
		{out: "disabled 4", setting: 4, disabled: true, contains: "reinstall DispatchQ"},
		{out: "error Element not found. (0x80070490)", err: errors.New("exit status 1"), setting: -1, fails: true, contains: "Element not found"},
		{out: "", err: errors.New("executable file not found"), setting: -1, fails: true, contains: "executable file not found"},
		{out: "garbage", setting: -1, fails: true, contains: "unexpected answer"},
	} {
		setting, err := parseToastResult([]byte(tc.out), tc.err)
		if setting != tc.setting {
			t.Errorf("%q: setting = %d, want %d", tc.out, setting, tc.setting)
		}
		if got := errors.Is(err, ErrToastDisabled); got != tc.disabled {
			t.Errorf("%q: disabled = %v, want %v (%v)", tc.out, got, tc.disabled, err)
		}
		if (err != nil) != (tc.disabled || tc.fails) {
			t.Errorf("%q: err = %v", tc.out, err)
		}
		if tc.contains != "" && (err == nil || !strings.Contains(err.Error(), tc.contains)) {
			t.Errorf("%q: err = %v, want it to mention %q", tc.out, err, tc.contains)
		}
	}
}

// A toast Windows will not show is a failed delivery with the reason, and the
// status follows what the notifier reported.
func TestToastDisabledIsAnErrorAndUpdatesStatus(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	r := &toastRunner{out: "disabled 1"}
	toast := &Toast{Runner: r, Now: func() time.Time { return now }}
	err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"})
	if !errors.Is(err, ErrToastDisabled) || !strings.Contains(err.Error(), "windows notification") {
		t.Fatalf("err = %v, want a windows notification ErrToastDisabled", err)
	}
	if got := toast.Status(); got != AuthorizationDenied {
		t.Fatalf("Status = %q, want %q", got, AuthorizationDenied)
	}
	r.out = "ok 0"
	if err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	if got := toast.Status(); got != AuthorizationAllowed {
		t.Fatalf("Status = %q, want %q", got, AuthorizationAllowed)
	}
	if r.count() != 2 {
		t.Fatalf("fresh status started a probe: %d calls", r.count())
	}
}

// A failed PowerShell run is reported and does not claim a status.
func TestToastRunFailure(t *testing.T) {
	r := &toastRunner{out: "error boom", err: errors.New("exit status 1")}
	toast := &Toast{Runner: r}
	err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the PowerShell message", err)
	}
}

// Registration runs once on success; a failure blocks sending with the reason,
// is retried at most once a minute, and shows as not determined.
func TestToastRegistration(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	attempts := 0
	fail := true
	r := &toastRunner{out: "ok 0"}
	toast := &Toast{Runner: r, Now: func() time.Time { return now }, Register: func() error {
		attempts++
		if fail {
			return errors.New("shortcut missing")
		}
		return nil
	}}
	err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"})
	if err == nil || !strings.Contains(err.Error(), "shortcut missing") {
		t.Fatalf("err = %v, want the registration failure", err)
	}
	if r.count() != 0 {
		t.Fatal("sent a toast without a registration")
	}
	if got := toast.Status(); got != AuthorizationNotDetermined {
		t.Fatalf("Status = %q, want %q", got, AuthorizationNotDetermined)
	}
	_ = toast.Notify(context.Background(), Notification{Title: "t", Message: "m"})
	if attempts != 1 {
		t.Fatalf("registration retried within a minute: %d attempts", attempts)
	}
	fail = false
	now = now.Add(toastRegisterRetry)
	if err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Notify after repair: %v", err)
	}
	for range 3 {
		if err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	if attempts != 2 {
		t.Fatalf("registration ran %d times, want 2", attempts)
	}
}

// Status never blocks: the first read answers unknown and probes in the
// background, without showing a toast; the probe's answer is cached until it
// is stale.
func TestToastStatusProbes(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := &toastRunner{out: "ok 0"}
	toast := &Toast{Runner: r, Now: clock}
	if got := toast.Status(); got != AuthorizationUnknown {
		t.Fatalf("first Status = %q, want %q", got, AuthorizationUnknown)
	}
	waitFor(t, func() bool { return toast.Status() == AuthorizationAllowed })
	if n := r.count(); n != 1 {
		t.Fatalf("probe ran %d times, want 1", n)
	}
	vals := scriptValues(t, decodeScript(t, r.calls[0][1:]))
	if len(vals) != 2 || vals[1] != "" {
		t.Fatalf("probe would show a toast: %q", vals)
	}
	mu.Lock()
	now = now.Add(toastProbeTTL)
	mu.Unlock()
	r.mu.Lock()
	r.out = "disabled 2"
	r.mu.Unlock()
	waitFor(t, func() bool { return toast.Status() == AuthorizationDenied })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAuthorizationFromSetting(t *testing.T) {
	want := map[int]string{
		0: AuthorizationAllowed, 1: AuthorizationDenied, 2: AuthorizationDenied,
		3: AuthorizationDenied, 4: AuthorizationNotDetermined, 9: AuthorizationUnknown,
	}
	for setting, status := range want {
		if got := authorizationFromSetting(setting); got != status {
			t.Errorf("setting %d = %q, want %q", setting, got, status)
		}
	}
}

// The registration's lifecycle depends on the installer's order: install
// writes the Start menu shortcut before it starts the daemon (which adds the
// AppUserModelID to it), and uninstall stops the daemon before it deletes the
// shortcut, so a running daemon cannot recreate a shortcut for a removed app.
func TestInstallerToastLifecycle(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/windows/dispatchq.nsi")
	if err != nil {
		t.Fatal(err)
	}
	nsi := string(raw)
	install, uninstall, ok := strings.Cut(nsi, `Section "Uninstall"`)
	if !ok {
		t.Fatal("no uninstall section")
	}
	const lnk = `"$SMPROGRAMS\DispatchQ\DispatchQ.lnk"`
	shortcut := strings.Index(install, "CreateShortcut "+lnk+` "$INSTDIR\dispatchqapp.exe"`)
	start := strings.Index(install, `"$INSTDIR\dispatchqd.exe" install`)
	if shortcut < 0 || start < 0 || shortcut > start {
		t.Fatalf("install must create %s for dispatchqapp.exe before it starts dispatchqd (shortcut at %d, start at %d)", lnk, shortcut, start)
	}
	stop := strings.Index(uninstall, `"$INSTDIR\dispatchqd.exe" uninstall`)
	remove := strings.Index(uninstall, "Delete "+lnk)
	if stop < 0 || remove < 0 || stop > remove {
		t.Fatalf("uninstall must stop dispatchqd before it deletes %s (stop at %d, delete at %d)", lnk, stop, remove)
	}
	if strings.Contains(nsi, "SetShellVarContext all") {
		t.Fatal("the shortcut must stay per-user: registration writes the user's Start menu")
	}
}

// windowsCommandLine is the command line Windows gets for args, in UTF-16
// units, with the program counted as a full MAX_PATH path. Base64 arguments
// need no quoting, so the arguments only add a separating space each.
func windowsCommandLine(args []string) int {
	n := 260
	for _, a := range args[1:] {
		n += 1 + len(utf16.Encode([]rune(a)))
	}
	return n
}

// sentToast decodes the toast document of the runner's only call.
func sentToast(t *testing.T, r *toastRunner) (parsedToast, int) {
	t.Helper()
	if r.count() != 1 {
		t.Fatalf("%d PowerShell calls, want 1", r.count())
	}
	args := r.calls[0]
	vals := scriptValues(t, decodeScript(t, args))
	return parseToast(t, vals[len(vals)-1]), windowsCommandLine(args)
}

// A link too long for the Windows command line (CreateProcess takes 32767
// UTF-16 units including the NUL) must not cost the toast: it is shown with
// title and message and without the link, never with a shortened one. A link
// that fits is kept whole. Checked at the boundary, for ASCII and Unicode.
func TestToastLongURLStaysWithinCommandLine(t *testing.T) {
	send := func(url string) (parsedToast, int, error) {
		r := &toastRunner{out: "ok 0"}
		err := (&Toast{Runner: r}).Notify(context.Background(), Notification{Title: "Tïtle ✓", Message: "Bödy 🚀", URL: url})
		if err != nil {
			return parsedToast{}, 0, err
		}
		p, n := sentToast(t, r)
		return p, n, nil
	}
	for _, unit := range []string{"a", "é", "🚀"} {
		t.Run(unit, func(t *testing.T) {
			base := "https://example.com/"
			fits := func(k int) bool {
				p, n, err := send(base + strings.Repeat(unit, k))
				if err != nil {
					t.Fatalf("Notify with a %d-unit path: %v", k, err)
				}
				if n > 32766 {
					t.Fatalf("%d-unit path: command line of %d UTF-16 units exceeds 32766", k, n)
				}
				if len(p.Texts) != 2 || p.Texts[0] != "Tïtle ✓" || p.Texts[1] != "Bödy 🚀" {
					t.Fatalf("%d-unit path lost the text: %q", k, p.Texts)
				}
				switch p.Launch {
				case base + strings.Repeat(unit, k):
					if p.ActivationType != "protocol" {
						t.Fatalf("link without protocol activation: %q", p.ActivationType)
					}
					return true
				case "":
					if p.ActivationType != "" {
						t.Fatalf("activation %q without a link", p.ActivationType)
					}
					return false
				default:
					t.Fatalf("%d-unit path became another link: %d chars", k, len(p.Launch))
					return false
				}
			}
			if !fits(10) {
				t.Fatal("a short link was dropped")
			}
			if fits(9000) {
				t.Fatal("a 9000-unit link was kept")
			}
			lo, hi := 10, 9000 // fits(lo), !fits(hi)
			for hi-lo > 1 {
				mid := (lo + hi) / 2
				if fits(mid) {
					lo = mid
				} else {
					hi = mid
				}
			}
			t.Logf("longest kept path: %d × %q", lo, unit)
		})
	}
}

// The longest title and message escape to the most XML (a quote becomes five
// characters) and still fit, so dropping the link always leaves a toast.
func TestToastWorstCaseTextFits(t *testing.T) {
	for _, c := range []string{`"`, "🚀", "\x01"} {
		r := &toastRunner{out: "ok 0"}
		n := Notification{Title: strings.Repeat(c, 5000), Message: strings.Repeat(c, 50000), URL: "https://example.com/" + strings.Repeat("x", 30000)}
		if err := (&Toast{Runner: r}).Notify(context.Background(), n); err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if _, size := sentToast(t, r); size > 32766 {
			t.Fatalf("%q: command line of %d UTF-16 units", c, size)
		}
	}
}

// A slow registration (COM and the Start menu folder) must not hold up the
// health endpoint: Status answers from its cache while Register is blocked,
// and a send gives up when its context ends instead of waiting it out. The
// blocked attempt is shared, not repeated, and its result is used afterwards.
func TestToastRegistrationDoesNotBlockStatus(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	attempts := 0
	r := &toastRunner{out: "ok 0"}
	toast := &Toast{Runner: r, Register: func() error {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			close(entered)
		}
		<-release
		return nil
	}}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	sendDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		sendDone <- toast.Notify(ctx, Notification{Title: "t", Message: "m"})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Register never ran")
	}

	statusDone := make(chan string, 1)
	go func() { statusDone <- toast.Status() }()
	select {
	case got := <-statusDone:
		if got != AuthorizationUnknown {
			t.Fatalf("Status during registration = %q, want %q", got, AuthorizationUnknown)
		}
	case <-time.After(time.Second):
		t.Fatal("Status blocked on the registration")
	}

	select {
	case err := <-sendDone:
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Notify during a blocked registration: %v, want the deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Notify waited past its context for the registration")
	}
	if r.count() != 0 {
		t.Fatal("a toast was sent before registration finished")
	}

	close(release)
	if err := toast.Notify(context.Background(), Notification{Title: "t", Message: "m"}); err != nil {
		t.Fatalf("Notify after registration: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Fatalf("Register ran %d times, want 1", attempts)
	}
}
