package controllers

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

func lanOnlyBridge(t *testing.T, tunnel *fakeTunnel) *BridgeService {
	t.Helper()
	return &BridgeService{
		LAN:         &fakeLAN{},
		ConfigPath:  filepath.Join(t.TempDir(), "mobile.json"),
		DefaultPort: 3011,
		Tunnel:      tunnel,
	}
}

func loadLANOnlyState(t *testing.T, path string) mobilebridge.State {
	t.Helper()
	state, err := mobilebridge.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLANOnlyRestoreDoesNotStartPublicTunnel(t *testing.T) {
	bridge := lanOnlyBridge(t, &fakeTunnel{})
	if _, err := bridge.EnableLANOnly(); err != nil {
		t.Fatal(err)
	}
	state := loadLANOnlyState(t, bridge.ConfigPath)
	tunnel := &fakeTunnel{}
	restarted := &BridgeService{LAN: &fakeLAN{}, ConfigPath: bridge.ConfigPath, DefaultPort: 3011, Tunnel: tunnel}
	if err := restarted.RestoreOnBoot(state); err != nil {
		t.Fatal(err)
	}
	if !restarted.LAN.Running() || tunnel.startedOn != 0 {
		t.Fatal("restored LAN-only listener started a public tunnel or failed to bind")
	}
}

func TestLANOnlyTransitionStopsPublicTunnelWithoutRotatingPassword(t *testing.T) {
	tunnel := &fakeTunnel{}
	bridge := lanOnlyBridge(t, tunnel)
	before, err := bridge.Enable()
	if err != nil {
		t.Fatal(err)
	}
	if tunnel.startedOn != 3011 {
		t.Fatal("Connect Mobile setup did not start the tunnel")
	}
	after, err := bridge.EnableLANOnly()
	if err != nil {
		t.Fatal(err)
	}
	if after.Password != before.Password || !bridge.LAN.Running() || tunnel.stops != 1 {
		t.Fatal("LAN-only transition changed the password, stopped the listener, or left the tunnel running")
	}
	if !loadLANOnlyState(t, bridge.ConfigPath).NoPublicTunnel {
		t.Fatal("LAN-only transition was not persisted")
	}
}

func TestLANOnlyEnableStopsPublicTunnelWhenListenerWentDown(t *testing.T) {
	tunnel := &fakeTunnel{}
	bridge := lanOnlyBridge(t, tunnel)
	if _, err := bridge.Enable(); err != nil {
		t.Fatal(err)
	}
	bridge.LAN.(*fakeLAN).running = false
	if _, err := bridge.EnableLANOnly(); err != nil {
		t.Fatal(err)
	}
	if tunnel.stops != 1 || !bridge.LAN.Running() || !loadLANOnlyState(t, bridge.ConfigPath).NoPublicTunnel {
		t.Fatal("private re-enable left the old public tunnel running")
	}
}

func TestLANOnlyRegenerateKeepsPublicTunnelOff(t *testing.T) {
	tunnel := &fakeTunnel{}
	bridge := lanOnlyBridge(t, tunnel)
	if _, err := bridge.EnableLANOnly(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Regenerate(); err != nil {
		t.Fatal(err)
	}
	if tunnel.startedOn != 0 || !loadLANOnlyState(t, bridge.ConfigPath).NoPublicTunnel {
		t.Fatal("password rotation reopened the public tunnel")
	}
}

func TestLANOnlyStartRemoteAccessEnablesPublicTunnel(t *testing.T) {
	tunnel := &fakeTunnel{}
	bridge := lanOnlyBridge(t, tunnel)
	before, err := bridge.EnableLANOnly()
	if err != nil {
		t.Fatal(err)
	}
	after, err := bridge.StartRemoteAccess()
	if err != nil {
		t.Fatal(err)
	}
	if after.Password != before.Password || tunnel.startedOn != 3011 || loadLANOnlyState(t, bridge.ConfigPath).NoPublicTunnel {
		t.Fatal("explicit remote access did not persist tunnel mode and start the connector without rotating the password")
	}
}

func TestTunnelOnlyPersistsLoopbackAndAdvertisesOnlyTunnel(t *testing.T) {
	tunnel := &fakeTunnel{endpoint: &mobilebridge.TunnelEndpoint{Ready: true, Hostname: "test.trycloudflare.com"}}
	bridge := lanOnlyBridge(t, tunnel)
	bridge.PickLANHosts = func() []string { return []string{"192.168.1.42"} }
	bridge.PickTailscaleHosts = func() []string { return []string{"100.72.46.7"} }
	status, err := bridge.EnableTunnelOnly()
	if err != nil {
		t.Fatal(err)
	}
	again, err := bridge.EnableTunnelOnly()
	if err != nil || again.Password != status.Password {
		t.Fatalf("repeat tunnel-only setup rotated password: first=%q again=%q err=%v", status.Password, again.Password, err)
	}
	state := loadLANOnlyState(t, bridge.ConfigPath)
	if !state.LoopbackOnly || bridge.LAN.(*fakeLAN).bindHost != "127.0.0.1" || tunnel.startedOn != 3011 {
		t.Fatalf("tunnel-only setup did not persist loopback mode: state=%+v bind=%q tunnel=%d", state, bridge.LAN.(*fakeLAN).bindHost, tunnel.startedOn)
	}
	if status.Host != "" || status.TailscaleHost != "" || len(status.Endpoints) != 1 || status.Endpoints[0].Kind != "tunnel" || len(bridge.AdvertisedEndpoints()) != 1 {
		t.Fatalf("loopback-only mode advertised unreachable direct endpoints: %+v", status)
	}
	restarted := &BridgeService{LAN: &fakeLAN{}, ConfigPath: bridge.ConfigPath, DefaultPort: 3011, Tunnel: &fakeTunnel{}}
	if err := restarted.RestoreOnBoot(state); err != nil {
		t.Fatal(err)
	}
	if got := restarted.LAN.(*fakeLAN).bindHost; got != "127.0.0.1" {
		t.Fatalf("restored tunnel-only listener bound %q, want loopback", got)
	}
}

func TestTunnelOnlyDoesNotClaimExistingLANListenerIsPrivate(t *testing.T) {
	bridge := lanOnlyBridge(t, &fakeTunnel{})
	before, err := bridge.EnableLANOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnableTunnelOnly(); err == nil {
		t.Fatal("tunnel-only enable accepted a listener already bound to the LAN")
	}
	state := loadLANOnlyState(t, bridge.ConfigPath)
	if state.LoopbackOnly || !bridge.LAN.Running() || bridge.LAN.(*fakeLAN).bindHost != "0.0.0.0" || bridge.Status().Password != before.Password {
		t.Fatal("failed tunnel-only transition changed the existing LAN listener or password")
	}
}

func TestTunnelOnlyAfterDisabledSecurePairingPreservesUserServe(t *testing.T) {
	bridge := lanOnlyBridge(t, &fakeTunnel{})
	cleared := 0
	userServe := false
	bridge.ApplyServe = func(int) error { return nil }
	bridge.ClearServe = func() error {
		if userServe {
			return errors.New("would remove a user-owned Serve route")
		}
		cleared++
		return nil
	}
	if _, err := bridge.Enable(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Disable(); err != nil {
		t.Fatal(err)
	}
	userServe = true // a separate route takes over after AO successfully cleared its own
	if _, err := bridge.EnableTunnelOnly(); err != nil {
		t.Fatalf("disabled secure-pairing host could not switch to tunnel-only: %v", err)
	}
	state := loadLANOnlyState(t, bridge.ConfigPath)
	if cleared != 1 || !state.LoopbackOnly || state.SecurePairing || bridge.LAN.(*fakeLAN).bindHost != "127.0.0.1" {
		t.Fatalf("tunnel-only transition touched a user-owned Serve route: clears=%d state=%+v bind=%q", cleared, state, bridge.LAN.(*fakeLAN).bindHost)
	}
}

func TestTunnelOnlyRequiresExplicitCleanupAfterFailedServeTeardown(t *testing.T) {
	bridge := lanOnlyBridge(t, &fakeTunnel{})
	bridge.ApplyServe = func(int) error { return nil }
	bridge.ClearServe = func() error { return errors.New("clear failed") }
	if _, err := bridge.Enable(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetSecurePairing(false); err != nil {
		t.Fatal(err)
	}
	if err := bridge.Disable(); err != nil {
		t.Fatal(err)
	}
	if state := loadLANOnlyState(t, bridge.ConfigPath); !state.ServeCleanupPending || state.SecurePairing {
		t.Fatalf("failed cleanup was not durably recorded: %+v", state)
	}
	if _, err := bridge.EnableLANOnly(); err != nil {
		t.Fatal(err)
	}
	if state := loadLANOnlyState(t, bridge.ConfigPath); !state.ServeCleanupPending {
		t.Fatal("LAN re-enable discarded pending Serve cleanup")
	}
	if err := bridge.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnableTunnelOnly(); err == nil || bridge.LAN.Running() {
		t.Fatalf("tunnel-only accepted stale Tailscale Serve: err=%v running=%v", err, bridge.LAN.Running())
	}
	clears := 0
	bridge.ClearServe = func() error { clears++; return nil }
	if _, err := bridge.EnableTunnelOnly(); err == nil || clears != 0 {
		t.Fatalf("tunnel-only silently cleared node-global Serve: err=%v clears=%d", err, clears)
	}
	if err := bridge.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.EnableTunnelOnly(); err != nil {
		t.Fatal(err)
	}
	if state := loadLANOnlyState(t, bridge.ConfigPath); clears != 1 || state.ServeCleanupPending || !state.LoopbackOnly {
		t.Fatalf("explicit cleanup did not persist tunnel-only mode: clears=%d state=%+v", clears, state)
	}
}

func TestTunnelOnlyRefusesInterruptedSecurePairingBridge(t *testing.T) {
	bridge := lanOnlyBridge(t, &fakeTunnel{})
	bridge.ApplyServe = func(int) error { return nil }
	bridge.ClearServe = func() error { t.Fatal("tunnel-only cleared Serve without explicit disable"); return nil }
	if _, err := bridge.Enable(); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.SetSecurePairing(true); err != nil {
		t.Fatal(err)
	}
	bridge.LAN.(*fakeLAN).running = false // daemon/listener died before disabling Serve
	if _, err := bridge.EnableTunnelOnly(); err == nil || bridge.LAN.Running() {
		t.Fatalf("tunnel-only accepted interrupted secure-pairing bridge: err=%v running=%v", err, bridge.LAN.Running())
	}
}
