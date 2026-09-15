package controllers

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// concurrencyLAN is a channel-controlled, race-safe LANController. The
// production LAN manager owns its own synchronization; this fake does the same
// so race failures in these tests identify BridgeService rather than the fake.
type concurrencyLAN struct {
	mu sync.Mutex

	running bool
	bound   int
	hash    string

	startEntered chan struct{}
	startRelease chan struct{}
	stopEntered  chan struct{}
	startOnce    sync.Once
	stopOnce     sync.Once
}

func (f *concurrencyLAN) Start(port int) (int, error) {
	if f.startEntered != nil {
		f.startOnce.Do(func() { close(f.startEntered) })
	}
	if f.startRelease != nil {
		<-f.startRelease
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = true
	f.bound = port
	return port, nil
}

func (f *concurrencyLAN) Stop(context.Context) error {
	if f.stopEntered != nil {
		f.stopOnce.Do(func() { close(f.stopEntered) })
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	f.bound = 0
	return nil
}

func (f *concurrencyLAN) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *concurrencyLAN) BoundPort() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bound
}

func (f *concurrencyLAN) SetPasswordHash(hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hash = hash
}

func (f *concurrencyLAN) PasswordHash() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hash
}

// Removing synchronization around the serve error snapshot makes this test
// fail under the race detector: Status reads it while the secure-pairing
// transition replaces it after each failed clear.
func TestBridgeStatusConcurrentSecurePairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.json")
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011}); err != nil {
		t.Fatal(err)
	}
	bridge := &BridgeService{
		LAN:                &concurrencyLAN{running: true, bound: 3011},
		ConfigPath:         path,
		PickLANHosts:       func() []string { return nil },
		PickTailscaleHosts: func() []string { return nil },
		ClearServe:         func() error { return errors.New("clear failed") },
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 200 {
			if _, err := bridge.SetSecurePairing(false); err != nil {
				t.Errorf("SetSecurePairing: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 2_000 {
			_ = bridge.Status()
		}
	}()
	close(start)
	wg.Wait()
}

// Removing the transition mutex lets Disable reach LAN.Stop while Enable is
// still inside LAN.Start. Besides overlapping listener operations, the later
// Enable save can overwrite the disabled state with a password/hash for a
// listener the caller expected Disable to stop.
func TestBridgeSerializesConcurrentEnableAndDisable(t *testing.T) {
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	stopEntered := make(chan struct{})
	lan := &concurrencyLAN{
		startEntered: startEntered,
		startRelease: startRelease,
		stopEntered:  stopEntered,
	}
	bridge := &BridgeService{
		LAN:                lan,
		ConfigPath:         filepath.Join(t.TempDir(), "mobile.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return nil },
		PickTailscaleHosts: func() []string { return nil },
	}

	enableDone := make(chan error, 1)
	go func() {
		_, err := bridge.Enable()
		enableDone <- err
	}()
	awaitSignal(t, startEntered, "Enable to enter LAN.Start")

	disableCalling := make(chan struct{})
	disableDone := make(chan error, 1)
	go func() {
		close(disableCalling)
		disableDone <- bridge.Disable()
	}()
	awaitSignal(t, disableCalling, "Disable goroutine to start")

	overlapped := false
	select {
	case <-stopEntered:
		overlapped = true
	case <-time.After(100 * time.Millisecond):
		// Disable is waiting at the transition boundary, as required.
	}
	close(startRelease)
	if err := awaitResult(t, enableDone, "Enable"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := awaitResult(t, disableDone, "Disable"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if overlapped {
		t.Fatal("Disable reached LAN.Stop before Enable completed LAN.Start")
	}

	state, err := mobilebridge.Load(bridge.ConfigPath)
	if err != nil {
		t.Fatalf("load final state: %v", err)
	}
	if state.Enabled || lan.Running() {
		t.Fatalf("final bridge state disagrees: persisted enabled=%t, listener running=%t", state.Enabled, lan.Running())
	}
	if state.Password == "" || !mobilebridge.PasswordMatches(lan.PasswordHash(), state.Password) {
		t.Fatal("persisted password and armed listener hash disagree after serialized transitions")
	}
}

// Status must not hold the short error-snapshot lock while a connector call is
// blocked. Otherwise ordinary UI polling freezes behind a slow Tailscale CLI.
func TestBridgeStatusResponsiveWhileSecurePairingConnectorIsSlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.json")
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011}); err != nil {
		t.Fatal(err)
	}
	clearEntered := make(chan struct{})
	clearRelease := make(chan struct{})
	bridge := &BridgeService{
		LAN:                &concurrencyLAN{running: true, bound: 3011},
		ConfigPath:         path,
		PickLANHosts:       func() []string { return nil },
		PickTailscaleHosts: func() []string { return nil },
		ClearServe: func() error {
			close(clearEntered)
			<-clearRelease
			return errors.New("clear failed")
		},
	}

	transitionDone := make(chan error, 1)
	go func() {
		_, err := bridge.SetSecurePairing(false)
		transitionDone <- err
	}()
	awaitSignal(t, clearEntered, "secure-pairing connector call")

	statusDone := make(chan MobileStatusResponse, 1)
	go func() { statusDone <- bridge.Status() }()
	var status MobileStatusResponse
	select {
	case status = <-statusDone:
	case <-time.After(time.Second):
		close(clearRelease)
		t.Fatal("Status blocked behind the slow secure-pairing connector")
	}
	close(clearRelease)
	if err := awaitResult(t, transitionDone, "SetSecurePairing"); err != nil {
		t.Fatalf("SetSecurePairing: %v", err)
	}
	if !status.Enabled || status.SecurePairing.Enabled {
		t.Fatalf("Status returned an unexpected persisted snapshot: %+v", status)
	}
}

// Resolving a connector may inspect the filesystem and launch a version probe.
// Holding tunnelMu across that work makes Status wait before it can report the
// last known connector state.
func TestBridgeStatusResponsiveWhileTunnelResolutionIsSlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mobile.json")
	if err := mobilebridge.Save(path, mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011}); err != nil {
		t.Fatal(err)
	}
	resolveEntered := make(chan struct{})
	resolveRelease := make(chan struct{})
	bridge := &BridgeService{
		LAN:                &concurrencyLAN{running: true, bound: 3011},
		ConfigPath:         path,
		PickLANHosts:       func() []string { return nil },
		PickTailscaleHosts: func() []string { return nil },
		ResolveTunnel: func() TunnelController {
			close(resolveEntered)
			<-resolveRelease
			return nil
		},
	}

	transitionDone := make(chan error, 1)
	go func() {
		_, err := bridge.StartRemoteAccess()
		transitionDone <- err
	}()
	awaitSignal(t, resolveEntered, "tunnel resolution")

	statusDone := make(chan struct{})
	go func() {
		_ = bridge.Status()
		close(statusDone)
	}()
	responsive := true
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		responsive = false
	}
	close(resolveRelease)
	if err := awaitResult(t, transitionDone, "StartRemoteAccess"); err != nil {
		t.Fatalf("StartRemoteAccess: %v", err)
	}
	if !responsive {
		awaitSignal(t, statusDone, "Status after tunnel resolution")
		t.Fatal("Status blocked behind slow tunnel resolution")
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func awaitResult(t *testing.T, ch <-chan error, operation string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		return nil
	}
}
