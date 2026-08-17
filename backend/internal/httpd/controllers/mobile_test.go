package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakeBridge struct{ enabled bool }

func (f *fakeBridge) Status() MobileStatusResponse {
	return MobileStatusResponse{Enabled: f.enabled, Host: "192.168.1.42", Port: 3011}
}
func (f *fakeBridge) Enable() (MobileStatusResponse, error) {
	f.enabled = true
	r := f.Status()
	r.Password = "abcd1234"
	return r, nil
}
func (f *fakeBridge) Disable() error { f.enabled = false; return nil }
func (f *fakeBridge) Regenerate() (MobileStatusResponse, error) {
	r := f.Status()
	r.Password = "wxyz5678"
	return r, nil
}
func (f *fakeBridge) SetSecurePairing(on bool) (MobileStatusResponse, error) {
	r := f.Status()
	r.SecurePairing.Enabled = on
	return r, nil
}

// fakeLAN is a minimal LANController for exercising BridgeService directly.
type fakeLAN struct {
	running   bool
	hash      string
	stopCalls int
}

func (f *fakeLAN) Start(port int) (int, error) { f.running = true; return port, nil }
func (f *fakeLAN) Stop(ctx context.Context) error {
	f.stopCalls++
	f.running = false
	return nil
}
func (f *fakeLAN) Running() bool            { return f.running }
func (f *fakeLAN) BoundPort() int           { return 3011 }
func (f *fakeLAN) SetPasswordHash(h string) { f.hash = h }
func (f *fakeLAN) PasswordHash() string     { return f.hash }

// When Save fails during a fresh enable, the listener that Start already opened
// must be torn back down and the armed hash rolled back — otherwise a LAN
// listener stays live on 0.0.0.0 while persisted state/UI say enable failed.
func TestMobileEnableRollsBackListenerWhenSaveFails(t *testing.T) {
	// A ConfigPath whose parent is a regular file makes mobilebridge.Save's
	// MkdirAll (and thus Save) fail deterministically.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	lan := &fakeLAN{}
	b := &BridgeService{LAN: lan, ConfigPath: filepath.Join(blocker, "mobile", "config.json"), DefaultPort: 3011}

	if _, err := b.Enable(); err == nil {
		t.Fatal("expected enable to fail on Save error")
	}
	if lan.Running() {
		t.Fatal("listener still running after failed enable; must be stopped")
	}
	if lan.stopCalls == 0 {
		t.Fatal("expected Stop to be called on rollback")
	}
	if lan.hash != "" {
		t.Fatalf("expected hash rolled back to empty, got %q", lan.hash)
	}
}

func TestMobileEnableReturnsPassword(t *testing.T) {
	c := &MobileController{Bridge: &fakeBridge{}}
	w := httptest.NewRecorder()
	c.Enable(w, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/enable", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var got MobileStatusResponse
	json.NewDecoder(w.Body).Decode(&got)
	if !got.Enabled || got.Password != "abcd1234" || got.Warning == "" {
		t.Fatalf("bad response: %+v", got)
	}
}

// Status must advertise both addresses so the renderer's LAN/Tailscale toggle
// can re-encode the pairing QR without a second round trip.
func TestMobileStatusSurfacesBothHosts(t *testing.T) {
	lan := &fakeLAN{running: true}
	b := &BridgeService{
		LAN:               lan,
		ConfigPath:        filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:       3011,
		PickLANHost:       func() string { return "192.168.1.42" },
		PickTailscaleHost: func() string { return "100.72.46.7" },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	got := b.Status()
	if got.Host != "192.168.1.42" {
		t.Errorf("Host = %q want 192.168.1.42", got.Host)
	}
	if got.TailscaleHost != "100.72.46.7" {
		t.Errorf("TailscaleHost = %q want 100.72.46.7", got.TailscaleHost)
	}
}

// An absent Tailscale install is an empty string, not an error: the renderer
// uses "" to decide to show a hint instead of an unscannable QR.
func TestMobileStatusTailscaleHostEmptyWhenAbsent(t *testing.T) {
	b := &BridgeService{
		LAN:               &fakeLAN{running: true},
		ConfigPath:        filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:       3011,
		PickLANHost:       func() string { return "192.168.1.42" },
		PickTailscaleHost: func() string { return "" },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := b.Status().TailscaleHost; got != "" {
		t.Errorf("TailscaleHost = %q want empty", got)
	}
}

// Unset pickers must fall back to the real autopickers rather than panicking on
// a nil func — production wiring in daemon.go leaves them unset. This also
// proves the call actually completed (not just "didn't panic"): the real
// AutopickLANIP/AutopickTailscaleIP paths run, and Port still comes through
// from the LAN controller untouched by that fallback.
func TestMobileStatusHostPickersDefaultWhenUnset(t *testing.T) {
	b := &BridgeService{
		LAN:         &fakeLAN{running: true},
		ConfigPath:  filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort: 3011,
	}
	got := b.Status()
	if got.Port != 3011 {
		t.Errorf("Port = %d want 3011", got.Port)
	}
}

// The bridge falls back to an ephemeral port when its default is taken, so the
// proxy must be pointed at the port Start actually returned — not DefaultPort.
// This is the stale-port regression this feature exists to avoid.
func TestSecurePairingAppliesActualBoundPort(t *testing.T) {
	// Start returns the actual ephemeral-fallback port (54014); BoundPort is
	// deliberately stale (3011) so the test fails if the implementation
	// re-reads BoundPort instead of using the port Start returned.
	lan := &portOverrideLAN{startPort: 54014, boundPortStale: 3011}
	var applied []int
	b := &BridgeService{
		LAN:         lan,
		ConfigPath:  filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort: 3011,
		ApplyServe:  func(port int) error { applied = append(applied, port); return nil },
		ClearServe:  func() error { return nil },
		QueryTS:     func() mobilebridge.TailscaleInfo { return tsUp },
		ServeTarget: func() int { return 54014 },
	}
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatalf("SetSecurePairing: %v", err)
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if len(applied) == 0 || applied[len(applied)-1] != 54014 {
		t.Errorf("applied = %v, want the bound port 54014", applied)
	}
}

func TestSecurePairingStatusActive(t *testing.T) {
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Enable(); err != nil {
		t.Fatal(err)
	}
	sp := b.Status().SecurePairing
	if !sp.Enabled || !sp.Available || !sp.Active {
		t.Fatalf("securePairing = %+v, want enabled/available/active", sp)
	}
	if sp.Host != "prasads-macbook-pro.tail057d04.ts.net" || sp.Port != 443 {
		t.Errorf("host/port = %q/%d", sp.Host, sp.Port)
	}
	if sp.Reason != "" {
		t.Errorf("Reason = %q, want empty when available", sp.Reason)
	}
}

func TestSecurePairingReasons(t *testing.T) {
	cases := []struct {
		name   string
		info   mobilebridge.TailscaleInfo
		target func() int
		want   string
	}{
		{"no cli", mobilebridge.TailscaleInfo{}, func() int { return 0 }, "no_cli"},
		{"no certs", mobilebridge.TailscaleInfo{Name: "h.tail1.ts.net"}, func() int { return 0 }, "no_certs"},
		{"port mismatch", tsUp, func() int { return 9999 }, "port_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newSecureBridge(t, tc.info, tc.target)
			if _, err := b.SetSecurePairing(true); err != nil {
				t.Fatal(err)
			}
			if _, err := b.Enable(); err != nil {
				t.Fatal(err)
			}
			if got := b.Status().SecurePairing.Reason; got != tc.want {
				t.Errorf("Reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// A serve failure must leave the bridge running in plaintext mode rather than
// taking pairing down entirely.
func TestSecurePairingServeFailureKeepsBridgeUp(t *testing.T) {
	b := newSecureBridge(t, tsUp, func() int { return 0 })
	b.ApplyServe = func(int) error { return errors.New("port 443 in use") }
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	res, err := b.Enable()
	if err != nil {
		t.Fatalf("Enable must succeed despite serve failure: %v", err)
	}
	if !res.Enabled {
		t.Error("bridge not enabled")
	}
	if got := b.Status().SecurePairing.Reason; got != "serve_failed" {
		t.Errorf("Reason = %q, want serve_failed", got)
	}
}

// Turning the mode off must tear the proxy down, or it keeps pointing at a
// bridge the user believes is private.
func TestSecurePairingOffClearsProxy(t *testing.T) {
	cleared := 0
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	b.ClearServe = func() error { cleared++; return nil }
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetSecurePairing(false); err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Errorf("Clear called %d times, want 1", cleared)
	}
}

// If Clear fails when turning the mode off, the flag is already persisted
// off (so the API call must still succeed), but the proxy may still be live
// — Status must surface that as clear_failed rather than silently reporting
// disabled.
func TestSecurePairingOffClearFailureReported(t *testing.T) {
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	b.ClearServe = func() error { return errors.New("tailscale serve clear: exit 1") }
	if _, err := b.SetSecurePairing(false); err != nil {
		t.Fatalf("SetSecurePairing(false) must not return an error: %v", err)
	}
	sp := b.Status().SecurePairing
	if sp.Enabled {
		t.Error("Enabled = true, want false")
	}
	if sp.Reason != "clear_failed" {
		t.Errorf("Reason = %q, want clear_failed", sp.Reason)
	}
}

var tsUp = mobilebridge.TailscaleInfo{
	Name:         "prasads-macbook-pro.tail057d04.ts.net",
	CertsEnabled: true,
}

// portOverrideLAN is fakeLAN with a caller-chosen bound port, so tests can
// exercise the ephemeral-fallback case. Start and BoundPort deliberately
// return different values: Start returns the freshly bound ephemeral port,
// while BoundPort returns a stale port a caller might have cached from
// before the fallback. Do not "fix" this by making them agree — the gap is
// what lets TestSecurePairingAppliesActualBoundPort catch a regression to
// re-reading b.LAN.BoundPort() instead of using the port Start returned.
type portOverrideLAN struct {
	fakeLAN
	startPort      int
	boundPortStale int
}

func (f *portOverrideLAN) BoundPort() int         { return f.boundPortStale }
func (f *portOverrideLAN) Start(int) (int, error) { f.running = true; return f.startPort, nil }

func newSecureBridge(t *testing.T, info mobilebridge.TailscaleInfo, target func() int) *BridgeService {
	t.Helper()
	return &BridgeService{
		LAN:         &fakeLAN{},
		ConfigPath:  filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort: 3011,
		ApplyServe:  func(int) error { return nil },
		ClearServe:  func() error { return nil },
		QueryTS:     func() mobilebridge.TailscaleInfo { return info },
		ServeTarget: target,
	}
}

// `tailscale serve --https=443 off` is node-global: it removes whatever is on
// :443, not merely what AO put there. Disabling a bridge that never enabled
// secure pairing must therefore leave the tailnet proxy strictly alone, or AO
// silently destroys a serve route its user configured for themselves — or one
// belonging to another AO instance on the same node.
func TestDisableLeavesServeAloneWhenSecurePairingNeverEnabled(t *testing.T) {
	cleared := 0
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	b.ClearServe = func() error { cleared++; return nil }
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := b.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if cleared != 0 {
		t.Errorf("clearServe called %d times, want 0 — AO must not touch a proxy it never installed", cleared)
	}
}

// When AO did install the proxy, disabling must still remove it.
func TestDisableClearsServeWhenSecurePairingEnabled(t *testing.T) {
	cleared := 0
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	b.ClearServe = func() error { cleared++; return nil }
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatalf("set secure: %v", err)
	}
	if err := b.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if cleared != 1 {
		t.Errorf("clearServe called %d times, want 1", cleared)
	}
}

// `tailscale serve --bg` outlives this process, so a graceful shutdown that
// stops only the listener leaves the tailnet routing to a local port with
// nothing authenticated behind it. Whatever binds that port next would be
// published to the tailnet in AO's place.
func TestShutdownServeClearsProxyWhenSecurePairingEnabled(t *testing.T) {
	cleared := 0
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	b.ClearServe = func() error { cleared++; return nil }
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatalf("set secure: %v", err)
	}
	b.ShutdownServe()
	if cleared != 1 {
		t.Errorf("clearServe called %d times on shutdown, want 1", cleared)
	}
}

// The preference must survive shutdown so boot restore re-applies the proxy
// against the next bound port without the user re-toggling anything.
func TestShutdownServeKeepsSecurePairingPreference(t *testing.T) {
	b := newSecureBridge(t, tsUp, func() int { return 3011 })
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := b.SetSecurePairing(true); err != nil {
		t.Fatalf("set secure: %v", err)
	}
	b.ShutdownServe()
	st, err := mobilebridge.Load(b.ConfigPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !st.SecurePairing {
		t.Error("SecurePairing was cleared by shutdown; boot restore would not re-apply the proxy")
	}
}

// A shutdown with the bridge off, or with secure pairing never enabled, must
// not issue the node-global clear either.
func TestShutdownServeNoopWhenNotOwned(t *testing.T) {
	for name, setup := range map[string]func(*BridgeService){
		"bridge disabled":         func(b *BridgeService) {},
		"secure pairing never on": func(b *BridgeService) { _, _ = b.Enable() },
	} {
		t.Run(name, func(t *testing.T) {
			cleared := 0
			b := newSecureBridge(t, tsUp, func() int { return 3011 })
			b.ClearServe = func() error { cleared++; return nil }
			setup(b)
			b.ShutdownServe()
			if cleared != 0 {
				t.Errorf("clearServe called %d times, want 0", cleared)
			}
		})
	}
}
