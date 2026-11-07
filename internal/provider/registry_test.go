package provider

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeAdapter is a third harness that exists only in tests. Registering it must
// be all it takes for the rest of dispatchq to run jobs on it — no scheduler,
// store or executor change.
type fakeAdapter struct {
	kind   Kind
	caps   Capabilities
	binary string
	events []Event
	health Health
}

func (f *fakeAdapter) Kind() Kind                 { return f.kind }
func (f *fakeAdapter) Capabilities() Capabilities { return f.caps }

func (f *fakeAdapter) Describe() Description {
	return Description{Name: string(f.kind), DefaultConfigDir: "~/.fake"}
}
func (f *fakeAdapter) DetectBinary() string { return f.binary }

func (f *fakeAdapter) ResolveBinary(inst Instance) string {
	if inst.BinaryPath != "" {
		return inst.BinaryPath
	}
	return f.binary
}

func (f *fakeAdapter) CheckHealth(_ context.Context, _ Instance, _ Host) Health {
	return f.health
}

func (f *fakeAdapter) ListModels(context.Context, Instance, Host) []Model {
	return []Model{{ID: "fake-model", Label: "Fake model"}}
}

func (f *fakeAdapter) InteractiveResumeCommand(_ Instance, req Request) (Command, error) {
	return Command{Path: f.binary, Args: []string{"resume", req.SessionID}}, nil
}

func (f *fakeAdapter) Command(_ Instance, req Request) (Command, error) {
	if !f.caps.SupportsAccess(req.AccessMode.OrDefault()) {
		return Command{}, UnsupportedAccessError(Instance{ID: string(f.kind)}, req.AccessMode)
	}
	return Command{Path: f.binary, Args: []string{req.Prompt}}, nil
}

func (f *fakeAdapter) NewParser() Parser { return &fakeParser{events: f.events} }

func (f *fakeAdapter) AsideCommand(_ Instance, req AsideRequest) (Command, error) {
	return Command{Path: "fake", Args: []string{req.Text}}, nil
}

func (f *fakeAdapter) ParseAside(out []byte) (Aside, error) {
	return Aside{Text: string(out)}, nil
}

type fakeParser struct{ events []Event }

// Parse returns the scripted events on the first line and nothing after, which
// is enough for a test that feeds one line.
func (p *fakeParser) Parse(_ []byte) []Event {
	out := p.events
	p.events = nil
	return out
}

func newFakeAdapter(kind Kind) *fakeAdapter {
	return &fakeAdapter{
		kind:   kind,
		binary: "/fake/" + string(kind),
		caps:   Capabilities{StructuredOutput: true, AccessModes: []AccessMode{AccessProviderDefault}},
		health: Health{State: HealthReady},
	}
}

func TestRegistryLookupByKind(t *testing.T) {
	fake := newFakeAdapter("fake")
	r := NewRegistry(fake)

	got, err := r.Lookup("fake")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != Adapter(fake) {
		t.Fatal("Lookup returned a different adapter")
	}
	if _, err := r.Lookup("gremlin"); err == nil {
		t.Fatal("an unregistered kind must not resolve")
	} else if !strings.Contains(err.Error(), "gremlin") {
		t.Fatalf("error %q should name the kind", err)
	}
}

func TestRegistryAcceptsAThirdAdapterWithoutOtherChanges(t *testing.T) {
	r := NewRegistry(newFakeAdapter("fake-a"))
	if err := r.Register(newFakeAdapter("fake-b")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, want := r.Kinds(), []Kind{"fake-a", "fake-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Kinds() = %v, want %v", got, want)
	}
}

func TestRegistryRejectsDuplicateAndMalformedAdapters(t *testing.T) {
	r := NewRegistry(newFakeAdapter("fake"))
	if err := r.Register(newFakeAdapter("fake")); err == nil {
		t.Fatal("a kind must not be registered twice")
	}
	if err := r.Register(newFakeAdapter("")); err == nil {
		t.Fatal("an adapter without a kind must be rejected")
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("a nil adapter must be rejected")
	}
}

func TestFakeAdapterHonoursItsOwnAccessModes(t *testing.T) {
	// The capability check is the adapter's, not the caller's: an adapter that
	// cannot enforce a restriction refuses instead of running with more
	// authority than asked for.
	fake := newFakeAdapter("fake")
	_, err := fake.Command(Instance{}, Request{Prompt: "p", AccessMode: AccessReadOnly})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if _, err := fake.Command(Instance{}, Request{Prompt: "p"}); err != nil {
		t.Fatalf("the zero access mode must be accepted as provider-default: %v", err)
	}
}
