package main

import (
	"context"
	"testing"

	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/store"
)

// desktopStub stands in for the platform's on-screen channel.
type desktopStub struct{}

func (desktopStub) Notify(context.Context, notify.Notification) error { return nil }

func TestNotifyChannelsAlwaysIncludesTheDesktop(t *testing.T) {
	channels := notifyChannels(desktopStub{}, store.Settings{})
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want just the desktop", channels)
	}
	if _, ok := channels[0].(desktopStub); !ok {
		t.Fatalf("channels[0] = %T, want the desktop channel", channels[0])
	}
}

func TestNotifyChannelsWithoutADesktopKeepsTheRemoteChannels(t *testing.T) {
	if channels := notifyChannels(nil, store.Settings{}); len(channels) != 0 {
		t.Fatalf("channels = %+v, want none", channels)
	}
	s := store.Settings{Ntfy: store.Ntfy{Enabled: true, Topic: "dispatchq"}}
	channels := notifyChannels(nil, s)
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want only ntfy", channels)
	}
	if _, ok := channels[0].(notify.Ntfy); !ok {
		t.Fatalf("channels[0] = %T, want notify.Ntfy", channels[0])
	}
}

func TestNotifyChannelsSkipsEnabledButUnconfigured(t *testing.T) {
	// Each channel is switched on but missing what it needs to deliver: a
	// half-filled-in channel must not turn into a failed request on every send.
	s := store.Settings{
		Pushover: store.Pushover{Enabled: true},
		Ntfy:     store.Ntfy{Enabled: true},
		Webhook:  store.Webhook{Enabled: true},
	}
	channels := notifyChannels(desktopStub{}, s)
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want only the desktop", channels)
	}
}

func TestNotifyChannelsIncludesEveryConfiguredEnabledChannel(t *testing.T) {
	s := store.Settings{
		Pushover: store.Pushover{Enabled: true, Token: "tok", UserKey: "usr"},
		Ntfy:     store.Ntfy{Enabled: true, Topic: "dispatchq"},
		Webhook:  store.Webhook{Enabled: true, URL: "https://hooks.example.com/x"},
	}
	channels := notifyChannels(desktopStub{}, s)
	if len(channels) != 4 {
		t.Fatalf("channels = %+v, want the desktop + 3 remote channels", channels)
	}
	var sawPushover, sawNtfy, sawWebhook bool
	for _, c := range channels {
		switch c.(type) {
		case notify.Pushover:
			sawPushover = true
		case notify.Ntfy:
			sawNtfy = true
		case notify.Webhook:
			sawWebhook = true
		}
	}
	if !sawPushover || !sawNtfy || !sawWebhook {
		t.Fatalf("channels = %+v, missing an enabled+configured channel", channels)
	}
}

func TestNotifyChannelsSkipsDisabledEvenWhenConfigured(t *testing.T) {
	s := store.Settings{
		Ntfy:    store.Ntfy{Topic: "dispatchq"},
		Webhook: store.Webhook{URL: "https://hooks.example.com/x"},
	}
	channels := notifyChannels(desktopStub{}, s)
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want only the desktop (channels are configured but not enabled)", channels)
	}
}
