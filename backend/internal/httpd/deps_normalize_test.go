package httpd

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/presence"
)

// TestNormalizeAPIDepsDefaultsDeviceLiveToPresence pins finding 2(a): a caller
// that wires Presence but leaves DeviceLive nil must not silently produce a
// roster that reports every device offline forever. normalizeAPIDeps is the
// single choke point (called once, from NewRouterWithControl) that closes
// this, so the trap is structurally unreachable rather than merely avoided by
// today's daemon.go wiring happening to set both fields to the same tracker.
func TestNormalizeAPIDepsDefaultsDeviceLiveToPresence(t *testing.T) {
	tracker := presence.NewTracker()
	tracker.Touch("inst-1")

	deps := APIDeps{Presence: tracker}
	got := normalizeAPIDeps(deps, discardLogger())

	if got.DeviceLive == nil {
		t.Fatal("DeviceLive should default to Presence when left nil")
	}
	live := got.DeviceLive.Live()
	if !live["inst-1"] {
		t.Fatalf("DeviceLive fallback did not read through to the same tracker: %+v", live)
	}
}

// TestNormalizeAPIDepsDoesNotOverrideExplicitDeviceLive confirms the fallback
// only fires when DeviceLive is nil — an explicitly wired DeviceLive (e.g. a
// test double) is left alone even if it differs from Presence.
func TestNormalizeAPIDepsDoesNotOverrideExplicitDeviceLive(t *testing.T) {
	explicit := fakeLiveSet{"only-explicit": true}
	tracker := presence.NewTracker()
	tracker.Touch("only-presence")

	deps := APIDeps{Presence: tracker, DeviceLive: explicit}
	got := normalizeAPIDeps(deps, discardLogger())

	if _, ok := got.DeviceLive.(fakeLiveSet); !ok {
		t.Fatalf("explicit DeviceLive was overwritten: %#v", got.DeviceLive)
	}
}

// TestNormalizeAPIDepsWarnsWhenRosterHasNoLiveness pins finding 2(b): a live
// DeviceRoster with no liveness source at all (Presence nil, DeviceLive nil)
// after the fallback is a real mis-wiring — every device will silently report
// offline forever — and gets exactly one startup warning.
func TestNormalizeAPIDepsWarnsWhenRosterHasNoLiveness(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	deps := APIDeps{DeviceRoster: fakeDeviceRoster{}}
	normalizeAPIDeps(deps, log)

	if !strings.Contains(buf.String(), "liveness") {
		t.Fatalf("expected a startup warning about missing liveness, got: %q", buf.String())
	}
}

// TestNormalizeAPIDepsNoWarnWhenPresenceCoversRoster confirms the warning does
// NOT fire once the (a) fallback has supplied a DeviceLive from Presence — the
// warning is for the case nothing at all is wired, not merely
// two-different-fields-but-effectively-fine.
func TestNormalizeAPIDepsNoWarnWhenPresenceCoversRoster(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	deps := APIDeps{DeviceRoster: fakeDeviceRoster{}, Presence: presence.NewTracker()}
	normalizeAPIDeps(deps, log)

	if strings.Contains(buf.String(), "liveness") {
		t.Fatalf("unexpected liveness warning once Presence covers the roster: %q", buf.String())
	}
}

// TestNormalizeAPIDepsNoWarnWithoutRoster confirms a nil Presence/DeviceLive is
// not, on its own, an error: only a live DeviceRoster with no liveness source
// triggers the warning. This mirrors the explicit ruling that a missing
// liveness column must not take away a working device list — it also must not
// spam a warning when there's no roster to mislead in the first place.
func TestNormalizeAPIDepsNoWarnWithoutRoster(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	normalizeAPIDeps(APIDeps{}, log)

	if buf.Len() != 0 {
		t.Fatalf("expected no log output without a DeviceRoster, got: %q", buf.String())
	}
}

type fakeLiveSet map[string]bool

func (f fakeLiveSet) Live() map[string]bool { return f }

func (f fakeLiveSet) LastSeen(string) (time.Time, bool) { return time.Time{}, false }

type fakeDeviceRoster struct{}

func (fakeDeviceRoster) List() []mobilebridge.PushDevice             { return nil }
func (fakeDeviceRoster) SetMuted(installID string, muted bool) error { return nil }
func (fakeDeviceRoster) Delete(installID string) error               { return nil }
