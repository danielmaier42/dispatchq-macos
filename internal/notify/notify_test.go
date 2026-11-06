package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type recordRunner struct {
	args []string
	err  error
}

func (r *recordRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.args = append([]string{name}, args...)
	return nil, r.err
}

func TestMacBuildsEscapedOsascript(t *testing.T) {
	r := &recordRunner{}
	m := Mac{Runner: r}
	err := m.Notify(context.Background(), Notification{Title: `cq "run"`, Message: `it failed: "boom"`})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if r.args[0] != "osascript" || r.args[1] != "-e" {
		t.Fatalf("expected osascript -e, got %v", r.args)
	}
	script := r.args[2]
	if !strings.Contains(script, `display notification`) || !strings.Contains(script, `with title`) {
		t.Fatalf("unexpected script: %s", script)
	}
	if !strings.Contains(script, `\"boom\"`) {
		t.Fatalf("quotes not escaped in message: %s", script)
	}
}

func TestFreeDesktopPassesTitleAndEscapedBody(t *testing.T) {
	r := &recordRunner{}
	err := FreeDesktop{Runner: r}.Notify(context.Background(),
		Notification{Title: "-x <task> failed", Message: `exit 3: a < b && "c"`})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	want := []string{"notify-send", "--app-name=DispatchQ", "--icon=dispatchq", "--",
		"-x <task> failed", `exit 3: a &lt; b &amp;&amp; "c"`}
	if strings.Join(r.args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q\nwant   %q", r.args, want)
	}
}

func TestFreeDesktopPropagatesRunnerError(t *testing.T) {
	err := FreeDesktop{Runner: &recordRunner{err: errors.New("no server")}}.Notify(context.Background(), Notification{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "notify-send") {
		t.Fatalf("err = %v, want a notify-send error", err)
	}
}

func TestMacPropagatesRunnerError(t *testing.T) {
	m := Mac{Runner: &recordRunner{err: errors.New("no gui")}}
	if err := m.Notify(context.Background(), Notification{Title: "t", Message: "m"}); err == nil {
		t.Fatal("expected error from runner")
	}
}

func TestPushoverPostsExpectedForm(t *testing.T) {
	var gotForm string
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.Form.Encode()
		gotType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := Pushover{Token: "tok", UserKey: "usr", URL: srv.URL, Client: srv.Client()}
	if err := p.Notify(context.Background(), Notification{Title: "T", Message: "M"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	for _, want := range []string{"token=tok", "user=usr", "title=T", "message=M"} {
		if !strings.Contains(gotForm, want) {
			t.Fatalf("form %q missing %q", gotForm, want)
		}
	}
	if !strings.HasPrefix(gotType, "application/x-www-form-urlencoded") {
		t.Fatalf("content-type = %q", gotType)
	}
}

func TestPushoverSendsLinkAsSupplementaryURL(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.Form
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := Pushover{Token: "tok", UserKey: "usr", URL: srv.URL, Client: srv.Client()}
	if err := p.Notify(context.Background(), Notification{Title: "T", Message: "M"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if _, ok := gotForm["url"]; ok {
		t.Fatalf("a notification without a link must not send a url field, got %v", gotForm)
	}
	link := "https://example.com/status?x=1&y=2"
	if err := p.Notify(context.Background(), Notification{Title: "T", Message: "M", URL: link}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := gotForm.Get("url"); got != link {
		t.Fatalf("url = %q, want %q", got, link)
	}
}

func TestPushoverErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	p := Pushover{Token: "t", UserKey: "u", URL: srv.URL, Client: srv.Client()}
	if err := p.Notify(context.Background(), Notification{Title: "T", Message: "M"}); err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestPushoverNotConfigured(t *testing.T) {
	if (Pushover{}).Configured() {
		t.Fatal("empty pushover should not be configured")
	}
	if err := (Pushover{}).Notify(context.Background(), Notification{}); err == nil {
		t.Fatal("expected error when not configured")
	}
}

type stubNotifier struct {
	called int
	err    error
}

func (s *stubNotifier) Notify(context.Context, Notification) error {
	s.called++
	return s.err
}

func TestMultiFansOutAndJoinsErrors(t *testing.T) {
	ok := &stubNotifier{}
	bad := &stubNotifier{err: errors.New("boom")}
	m := Multi{Notifiers: []Notifier{ok, bad, nil}}
	err := m.Notify(context.Background(), Notification{Title: "t", Message: "m"})
	if ok.called != 1 || bad.called != 1 {
		t.Fatalf("both notifiers should be attempted: ok=%d bad=%d", ok.called, bad.called)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected joined error, got %v", err)
	}
}

// slowNotifier blocks for a fixed duration, so a test can tell whether Multi
// ran its notifiers concurrently or one after another.
type slowNotifier struct{ delay time.Duration }

func (s slowNotifier) Notify(ctx context.Context, _ Notification) error {
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The caller (engine.send) wraps the whole call in one shared context. If
// Multi ran notifiers one after another, three 200ms channels would need a
// 600ms budget; run concurrently, the whole call takes about as long as the
// slowest single channel. This is what stops a hung Pushover from starving
// the ntfy/webhook channels behind it in the fixed order.
func TestMultiRunsNotifiersConcurrently(t *testing.T) {
	m := Multi{Notifiers: []Notifier{
		slowNotifier{delay: 200 * time.Millisecond},
		slowNotifier{delay: 200 * time.Millisecond},
		slowNotifier{delay: 200 * time.Millisecond},
	}}
	start := time.Now()
	if err := m.Notify(context.Background(), Notification{}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 350*time.Millisecond {
		t.Fatalf("took %s, want ~200ms — notifiers ran sequentially instead of concurrently", elapsed)
	}
}

// A click target is a macOS-only extra. The osascript fallback cannot carry it,
// and it must not end up in the visible text of any channel.
func TestArtifactIDStaysOutOfTheMessageText(t *testing.T) {
	r := &recordRunner{}
	m := Mac{Runner: r}
	if err := m.Notify(context.Background(), Notification{
		Title: "DispatchQ", Message: "New artifact", ArtifactID: "a-20260901T030000-abc",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if strings.Contains(r.args[2], "a-20260901T030000-abc") {
		t.Fatalf("artifact id leaked into the osascript text: %s", r.args[2])
	}
}

func TestIsWebURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.com/x?y=1":        true,
		"http://ci.internal:8080/build/42": true,
		"HTTPS://example.com":              true,
		"":                                 false,
		"example.com/x":                    false,
		"https://":                         false,
		"file:///etc/passwd":               false,
		"x-apple.systempreferences:x":      false,
		"javascript:alert(1)":              false,
	}
	for in, want := range cases {
		if got := IsWebURL(in); got != want {
			t.Errorf("IsWebURL(%q) = %v, want %v", in, got, want)
		}
	}
}
