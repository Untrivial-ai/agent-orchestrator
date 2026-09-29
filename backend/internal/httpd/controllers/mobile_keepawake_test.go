package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakeKeepAwake struct {
	active  bool
	battery bool
	starts  int
}

func (f *fakeKeepAwake) Start() error     { f.active = true; f.starts++; return nil }
func (f *fakeKeepAwake) Stop()            { f.active = false }
func (f *fakeKeepAwake) Active() bool     { return f.active }
func (f *fakeKeepAwake) HasBattery() bool { return f.battery }

func newKeepAwakeBridge(t *testing.T, ka KeepAwakeController) (*BridgeService, *fakeLAN) {
	t.Helper()
	lan := &fakeLAN{}
	return &BridgeService{
		LAN:                lan,
		ConfigPath:         filepath.Join(t.TempDir(), "mobile", "config.json"),
		DefaultPort:        3011,
		PickLANHosts:       func() []string { return []string{"192.168.1.42"} },
		PickTailscaleHosts: func() []string { return nil },
		KeepAwake:          ka,
	}, lan
}

// Unsupported platforms report nothing to toggle and refuse the switch.
func TestKeepAwakeUnsupportedWithoutController(t *testing.T) {
	b, _ := newKeepAwakeBridge(t, nil)
	if got := b.Status().KeepAwake; got != (KeepAwakeStatus{}) {
		t.Fatalf("KeepAwake = %+v, want zero value", got)
	}
	if _, err := b.SetKeepAwake(true); err == nil {
		t.Fatal("expected SetKeepAwake to fail without a controller")
	}
}

// Turning the option on while the bridge is off only records the choice; the
// assertion is taken when the bridge comes up and released when it goes down.
func TestKeepAwakeFollowsBridge(t *testing.T) {
	ka := &fakeKeepAwake{battery: true}
	b, _ := newKeepAwakeBridge(t, ka)

	res, err := b.SetKeepAwake(true)
	if err != nil {
		t.Fatalf("SetKeepAwake: %v", err)
	}
	if !res.KeepAwake.Supported || !res.KeepAwake.Enabled || res.KeepAwake.Active || !res.KeepAwake.HasBattery {
		t.Fatalf("with bridge off: %+v", res.KeepAwake)
	}

	if _, err := b.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !ka.active {
		t.Fatal("enable should take the assertion when the option is on")
	}
	// Enable rewrites persisted state; the choice must survive it.
	if st, _ := mobilebridge.Load(b.ConfigPath); !st.KeepAwake {
		t.Fatal("Enable dropped the persisted KeepAwake choice")
	}

	if err := b.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ka.active {
		t.Fatal("disable should release the assertion")
	}
	if !b.Status().KeepAwake.Enabled {
		t.Fatal("disable should keep the preference")
	}
}

func TestKeepAwakeToggleWhileBridgeRuns(t *testing.T) {
	ka := &fakeKeepAwake{}
	b, _ := newKeepAwakeBridge(t, ka)
	if _, err := b.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if ka.active {
		t.Fatal("option is off by default; enable must not take the assertion")
	}
	if res, _ := b.SetKeepAwake(true); !res.KeepAwake.Active {
		t.Fatal("turning the option on with the bridge up should take the assertion")
	}
	if res, _ := b.SetKeepAwake(false); res.KeepAwake.Active || res.KeepAwake.Enabled {
		t.Fatalf("turning the option off: %+v", res.KeepAwake)
	}
}

func TestKeepAwakeRestoredOnBoot(t *testing.T) {
	ka := &fakeKeepAwake{}
	b, _ := newKeepAwakeBridge(t, ka)
	if err := b.RestoreOnBoot(mobilebridge.State{Enabled: true, Password: "pw", LastPort: 3011, KeepAwake: true}); err != nil {
		t.Fatalf("RestoreOnBoot: %v", err)
	}
	if !ka.active {
		t.Fatal("boot restore should take the assertion when the option was on")
	}
	b.ShutdownKeepAwake()
	if ka.active {
		t.Fatal("shutdown should release the assertion")
	}
}

func TestMobileKeepAwakeRoute(t *testing.T) {
	c := &MobileController{Bridge: &fakeBridge{}}

	w := httptest.NewRecorder()
	c.KeepAwake(w, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/keep-awake", bytes.NewBufferString(`{"enabled":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var got MobileStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.KeepAwake.Enabled {
		t.Fatalf("bad response: %+v", got.KeepAwake)
	}

	w = httptest.NewRecorder()
	c.KeepAwake(w, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/keep-awake", bytes.NewBufferString(`not json`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid body: got %d", w.Code)
	}
}
