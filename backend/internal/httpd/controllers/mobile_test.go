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
func (f *fakeBridge) StartRemoteAccess() (MobileStatusResponse, error) {
	return MobileStatusResponse{}, nil
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
func (f *fakeLAN) Running() bool { return f.running }
func (f *fakeLAN) BoundPort() int {
	// Mirrors the real listener: no bound port until it is actually running.
	if !f.running {
		return 0
	}
	return 3011
}
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
		LAN:                lan,
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return []string{"100.72.46.7"} },
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
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
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

// The phone races every advertised endpoint, so Status must list all of them.
// Host/TailscaleHost keep reporting the first of each for the existing
// renderer, but they are now derived from the same lists rather than resolved
// separately, so the two can never disagree.
func TestMobileStatusAdvertisesEveryEndpoint(t *testing.T) {
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42", "10.0.0.5"} },
		PickTailscaleHosts: func() []string { return []string{"100.72.46.7"} },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	got := b.Status()

	want := []mobilebridge.Endpoint{
		{Kind: mobilebridge.KindLAN, Host: "192.168.1.42", Port: 3011},
		{Kind: mobilebridge.KindLAN, Host: "10.0.0.5", Port: 3011},
		{Kind: mobilebridge.KindTailscale, Host: "100.72.46.7", Port: 3011},
	}
	if len(got.Endpoints) != len(want) {
		t.Fatalf("got %d endpoints %+v, want %d", len(got.Endpoints), got.Endpoints, len(want))
	}
	for i := range want {
		if got.Endpoints[i] != want[i] {
			t.Errorf("endpoint %d: got %+v want %+v", i, got.Endpoints[i], want[i])
		}
	}

	// The legacy singular fields stay in step with the lists.
	if got.Host != "192.168.1.42" {
		t.Errorf("Host = %q want the first LAN candidate", got.Host)
	}
	if got.TailscaleHost != "100.72.46.7" {
		t.Errorf("TailscaleHost = %q want the first Tailscale candidate", got.TailscaleHost)
	}
}

// No network at all is a real state. An empty list must not become a list
// containing an empty host, which the phone would try to dial.
func TestMobileStatusEndpointsEmptyWithoutNetwork(t *testing.T) {
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return nil },
		PickTailscaleHosts: func() []string { return nil },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got := b.Status()
	if len(got.Endpoints) != 0 {
		t.Fatalf("got %+v, want no endpoints", got.Endpoints)
	}
	if got.Host != "" {
		t.Errorf("Host = %q want empty", got.Host)
	}
}

type fakeTunnel struct {
	startedOn int
	stops     int
	endpoint  *mobilebridge.TunnelEndpoint
	status    mobilebridge.TunnelStatus
}

func (f *fakeTunnel) Start(localPort int) { f.startedOn = localPort }
func (f *fakeTunnel) Stop()               { f.stops++ }
func (f *fakeTunnel) Endpoint() *mobilebridge.TunnelEndpoint {
	return f.endpoint
}
func (f *fakeTunnel) Status() mobilebridge.TunnelStatus { return f.status }

func TestMobileEnableStartsTheTunnelOnTheBoundPort(t *testing.T) {
	// The connector must target the port the listener actually bound, not the
	// configured default: Start falls back to an ephemeral port when the
	// default is taken, and a connector pointed at the wrong port tunnels
	// nothing.
	tun := &fakeTunnel{}
	b := &BridgeService{
		LAN:                &fakeLAN{},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		Tunnel:             tun,
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if tun.startedOn != 3011 {
		t.Fatalf("tunnel started on %d, want the bound port 3011", tun.startedOn)
	}
}

type stopErrorLAN struct {
	fakeLAN
	err          error
	stillRunning bool
}

func (f *stopErrorLAN) Stop(context.Context) error {
	f.stopCalls++
	f.running = f.stillRunning
	return f.err
}

func TestMobileDisableFinishesCleanupAfterStopErrorWhenListenerStopped(t *testing.T) {
	stopErr := errors.New("shutdown deadline exceeded")
	path := filepath.Join(t.TempDir(), "mobile.json")
	if err := mobilebridge.Save(path, mobilebridge.State{
		Enabled:       true,
		Password:      "paired-password",
		LastPort:      3011,
		SecurePairing: true,
	}); err != nil {
		t.Fatal(err)
	}
	cleared := 0
	lan := &stopErrorLAN{
		fakeLAN: fakeLAN{running: true, hash: mobilebridge.HashPassword("paired-password")},
		err:     stopErr,
	}
	bridge := &BridgeService{
		LAN:         lan,
		ConfigPath:  path,
		ClearServe:  func() error { cleared++; return nil },
		DefaultPort: 3011,
	}

	if err := bridge.Disable(); !errors.Is(err, stopErr) {
		t.Fatalf("Disable error = %v, want %v", err, stopErr)
	}
	state, err := mobilebridge.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Enabled {
		t.Fatal("persisted bridge remained enabled after the listener stopped")
	}
	if cleared != 1 {
		t.Fatalf("owned secure-pairing proxy cleared %d times, want 1", cleared)
	}
}

func TestMobileDisablePreservesEnabledStateWhenStopErrorLeavesListenerRunning(t *testing.T) {
	stopErr := errors.New("listener still running")
	path := filepath.Join(t.TempDir(), "mobile.json")
	if err := mobilebridge.Save(path, mobilebridge.State{
		Enabled:       true,
		Password:      "paired-password",
		LastPort:      3011,
		SecurePairing: true,
	}); err != nil {
		t.Fatal(err)
	}
	cleared := 0
	lan := &stopErrorLAN{
		fakeLAN:      fakeLAN{running: true, hash: mobilebridge.HashPassword("paired-password")},
		err:          stopErr,
		stillRunning: true,
	}
	bridge := &BridgeService{
		LAN:         lan,
		ConfigPath:  path,
		ClearServe:  func() error { cleared++; return nil },
		DefaultPort: 3011,
	}

	if err := bridge.Disable(); !errors.Is(err, stopErr) {
		t.Fatalf("Disable error = %v, want %v", err, stopErr)
	}
	state, err := mobilebridge.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Enabled {
		t.Fatal("persisted bridge was disabled while the listener remained running")
	}
	if cleared != 0 {
		t.Fatalf("secure-pairing proxy cleared %d times while listener remained live", cleared)
	}
}

func TestMobileDisableStopsTheTunnel(t *testing.T) {
	// Leaving a public tunnel up after the user turned Connect Mobile off would
	// keep the machine reachable from the internet with the UI saying it is not.
	tun := &fakeTunnel{}
	b := &BridgeService{
		LAN:                &fakeLAN{},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		Tunnel:             tun,
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := b.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if tun.stops != 1 {
		t.Fatalf("tunnel stopped %d times, want 1", tun.stops)
	}
}

func TestMobileStatusIncludesAReadyTunnelEndpoint(t *testing.T) {
	tun := &fakeTunnel{
		endpoint: &mobilebridge.TunnelEndpoint{Ready: true, Hostname: "abc.trycloudflare.com"},
		status:   mobilebridge.TunnelStatus{Running: true, Ready: true, Hostname: "abc.trycloudflare.com"},
	}
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		Tunnel:             tun,
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	got := b.Status()
	last := got.Endpoints[len(got.Endpoints)-1]
	if last.Kind != mobilebridge.KindTunnel || last.Host != "abc.trycloudflare.com" {
		t.Fatalf("last endpoint = %+v, want the tunnel", last)
	}
	if !got.Tunnel.Ready {
		t.Error("Tunnel status not surfaced; the desktop cannot show progress")
	}
}

func TestMobileStatusOmitsATunnelThatIsNotReady(t *testing.T) {
	// Still settling. The desktop should say "preparing", and the QR must not
	// carry an address that answers 530.
	tun := &fakeTunnel{status: mobilebridge.TunnelStatus{Running: true}}
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		Tunnel:             tun,
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	for _, e := range b.Status().Endpoints {
		if e.Kind == mobilebridge.KindTunnel {
			t.Fatalf("advertised %+v while the tunnel was not ready", e)
		}
	}
}

func TestMobileWorksWithNoTunnelConfigured(t *testing.T) {
	// Remote access unavailable (no cloudflared, or the feature off) must leave
	// the LAN bridge behaving exactly as it did before.
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := b.Status(); len(got.Endpoints) != 1 || got.Endpoints[0].Kind != mobilebridge.KindLAN {
		t.Fatalf("endpoints = %+v, want just the LAN one", got.Endpoints)
	}
	if err := b.Disable(); err != nil {
		t.Fatalf("disable: %v", err)
	}
}

// The desktop builds the pairing code, and the code must carry the machine's
// identity so the phone can verify every endpoint it later races. Without it
// here the renderer would have to make a second call to /api/v1/identity just
// to draw a QR.
func TestMobileStatusCarriesTheHostIdentity(t *testing.T) {
	b := &BridgeService{
		LAN:                &fakeLAN{running: true},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		HostID:             "h_b3e07f31",
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
	}
	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if got := b.Status().HostID; got != "h_b3e07f31" {
		t.Fatalf("HostID = %q want h_b3e07f31", got)
	}
}

// Found by running the daemon: with Connect Mobile off there is no bound port,
// so every endpoint was advertised as host:0. A phone would dutifully race
// addresses that cannot work. Nothing is reachable until the bridge is up, so
// the honest answer is an empty list.
func TestMobileAdvertisesNothingWhileTheBridgeIsOff(t *testing.T) {
	b := &BridgeService{
		LAN:                &fakeLAN{running: false},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return []string{"100.72.46.7"} },
	}

	if got := b.AdvertisedEndpoints(); len(got) != 0 {
		t.Fatalf("advertised %+v while the bridge was disabled", got)
	}
	if got := b.Status().Endpoints; len(got) != 0 {
		t.Fatalf("status advertised %+v while the bridge was disabled", got)
	}
}

// Found by running the desktop app: enabling Connect Mobile started the
// connector, but restarting the daemon restored only the LAN listener. Remote
// access then stayed silently off — the UI showed the bridge enabled while the
// tunnel never came back — until the user toggled it off and on.
//
// A restart does not go through enableWithPasswordLocked (there is no password to
// rotate), so RestoreOnBoot has to mirror its post-Start work.
func TestMobileRestoreOnBootStartsTheTunnel(t *testing.T) {
	tun := &fakeTunnel{}
	b := &BridgeService{
		LAN:                &fakeLAN{},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		Tunnel:             tun,
	}

	if err := b.RestoreOnBoot(mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if tun.startedOn != 3011 {
		t.Fatalf("tunnel started on %d, want the restored port 3011", tun.startedOn)
	}
}

func TestMobileRestoreOnBootWithoutATunnelIsHarmless(t *testing.T) {
	b := &BridgeService{
		LAN:                &fakeLAN{},
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
	}
	if err := b.RestoreOnBoot(mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011}); err != nil {
		t.Fatalf("restore: %v", err)
	}
}

// A cloudflared process outlives the daemon that spawned it, so quitting
// without stopping it leaves a public hostname pointing at a port that no
// longer has the authenticated LAN listener behind it. Reaping on the next
// boot cleans it up eventually, but "eventually" is whenever the user happens
// to reopen the app — until then the process runs and the hostname resolves.
func TestShutdownTunnelStopsTheConnector(t *testing.T) {
	dir := t.TempDir()
	tun := &fakeTunnel{}
	b := &BridgeService{ConfigPath: filepath.Join(dir, "mobile.json"), Tunnel: tun}

	b.ShutdownTunnel()

	if tun.stops != 1 {
		t.Fatalf("connector stops = %d, want 1", tun.stops)
	}
}

// Remote access is optional: without cloudflared installed there is no
// connector, and shutdown must not panic on the way out.
func TestShutdownTunnelWithoutAConnector(t *testing.T) {
	dir := t.TempDir()
	b := &BridgeService{ConfigPath: filepath.Join(dir, "mobile.json")}

	b.ShutdownTunnel()
}

// Remote access is optional: without cloudflared there is no connector at all.
// A zero TunnelStatus made that indistinguishable from "not started yet", so
// the desktop showed a normal QR and the user had no way to learn that
// connecting from cellular was simply unavailable on this machine.
func TestTunnelStatusReportsWhenRemoteAccessIsUnsupported(t *testing.T) {
	dir := t.TempDir()
	b := &BridgeService{ConfigPath: filepath.Join(dir, "mobile.json")}

	st := b.tunnelStatus()

	if st.Supported {
		t.Fatal("no connector configured, so remote access is not supported")
	}
}

func TestTunnelStatusReportsSupportedWhenAConnectorExists(t *testing.T) {
	dir := t.TempDir()
	b := &BridgeService{ConfigPath: filepath.Join(dir, "mobile.json"), Tunnel: &fakeTunnel{}}

	if !b.tunnelStatus().Supported {
		t.Fatal("a configured connector means remote access is supported")
	}
}

// Resolution happens once at daemon start, so a cloudflared installed from
// Connect Mobile was invisible until AO restarted — the user pressed Install,
// watched it succeed, and remote access stayed off with no explanation.
// Enabling is the natural moment to look again.
func TestEnableResolvesAConnectorInstalledSinceBoot(t *testing.T) {
	dir := t.TempDir()
	tun := &fakeTunnel{}
	resolved := 0
	b := &BridgeService{
		ConfigPath:  filepath.Join(dir, "mobile.json"),
		LAN:         &fakeLAN{},
		DefaultPort: 3011,
		ResolveTunnel: func() TunnelController {
			resolved++
			return tun
		},
	}

	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if resolved != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolved)
	}
	if tun.startedOn != 3011 {
		t.Fatalf("connector started on %d, want the bound port 3011", tun.startedOn)
	}
}

// A machine that still has no cloudflared must enable cleanly as a LAN-only
// bridge rather than failing: remote access is optional.
func TestEnableWithoutAConnectorStillWorks(t *testing.T) {
	dir := t.TempDir()
	b := &BridgeService{
		ConfigPath:    filepath.Join(dir, "mobile.json"),
		LAN:           &fakeLAN{},
		DefaultPort:   3011,
		ResolveTunnel: func() TunnelController { return nil },
	}

	if _, err := b.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if b.tunnelStatus().Supported {
		t.Fatal("no connector resolved, so remote access is not supported")
	}
}

// Installing cloudflared used to re-enable the bridge so the daemon would look
// for the new binary. Enable always mints a fresh password, so that silently
// invalidated the phone already paired — the user installed remote access and
// lost the connection they were setting it up for.
func TestStartRemoteAccessKeepsThePairedPhoneWorking(t *testing.T) {
	dir := t.TempDir()
	lan := &fakeLAN{}
	tun := &fakeTunnel{}
	b := &BridgeService{
		ConfigPath: filepath.Join(dir, "mobile.json"), LAN: lan, DefaultPort: 3011,
	}
	before, err := b.Enable()
	if err != nil {
		t.Fatalf("enable: %v", err)
	}

	// cloudflared appears only now, as an install would leave it.
	b.ResolveTunnel = func() TunnelController { return tun }
	after, err := b.StartRemoteAccess()
	if err != nil {
		t.Fatalf("StartRemoteAccess: %v", err)
	}

	if after.Password != before.Password {
		t.Fatal("password rotated: the phone paired a moment ago can no longer authenticate")
	}
	if tun.startedOn != 3011 {
		t.Fatalf("connector started on %d, want the bound port 3011", tun.startedOn)
	}
}

// Nothing to start, and nothing to break: the bridge is off, so this is a
// no-op rather than an error the UI has to special-case.
func TestStartRemoteAccessWhileDisabledDoesNothing(t *testing.T) {
	dir := t.TempDir()
	tun := &fakeTunnel{}
	b := &BridgeService{
		ConfigPath: filepath.Join(dir, "mobile.json"), LAN: &fakeLAN{}, DefaultPort: 3011,
		ResolveTunnel: func() TunnelController { return tun },
	}

	if _, err := b.StartRemoteAccess(); err != nil {
		t.Fatalf("StartRemoteAccess: %v", err)
	}
	if tun.startedOn != 0 {
		t.Fatal("connector started while the bridge is disabled")
	}
}

type startErrorLAN struct {
	fakeLAN
	err error
}

func (f *startErrorLAN) Start(int) (int, error) { return 0, f.err }

func TestMobileRestoreOnBootRollsBackHashWhenStartFails(t *testing.T) {
	startErr := errors.New("bind failed")
	lan := &startErrorLAN{fakeLAN: fakeLAN{hash: "previous-hash"}, err: startErr}
	bridge := &BridgeService{LAN: lan}

	err := bridge.RestoreOnBoot(mobilebridge.State{Enabled: true, Password: "restored-password", LastPort: 3011})
	if !errors.Is(err, startErr) {
		t.Fatalf("RestoreOnBoot error = %v, want %v", err, startErr)
	}
	if got := lan.PasswordHash(); got != "previous-hash" {
		t.Fatalf("password hash after failed restore = %q, want previous-hash", got)
	}
}
