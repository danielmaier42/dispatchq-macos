//go:build !windows

package main

import (
	"testing"

	"github.com/danielmaier42/claudeq/internal/notify"
	"github.com/danielmaier42/claudeq/internal/store"
)

func TestNotifyChannelsAlwaysIncludesMac(t *testing.T) {
	channels := notifyChannels(store.Settings{})
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want just Mac", channels)
	}
	if _, ok := channels[0].(notify.Mac); !ok {
		t.Fatalf("channels[0] = %T, want notify.Mac", channels[0])
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
	channels := notifyChannels(s)
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want only Mac", channels)
	}
}

func TestNotifyChannelsIncludesEveryConfiguredEnabledChannel(t *testing.T) {
	s := store.Settings{
		Pushover: store.Pushover{Enabled: true, Token: "tok", UserKey: "usr"},
		Ntfy:     store.Ntfy{Enabled: true, Topic: "dispatchq"},
		Webhook:  store.Webhook{Enabled: true, URL: "https://hooks.example.com/x"},
	}
	channels := notifyChannels(s)
	if len(channels) != 4 {
		t.Fatalf("channels = %+v, want Mac + 3 remote channels", channels)
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
	channels := notifyChannels(s)
	if len(channels) != 1 {
		t.Fatalf("channels = %+v, want only Mac (channels are configured but not enabled)", channels)
	}
}
