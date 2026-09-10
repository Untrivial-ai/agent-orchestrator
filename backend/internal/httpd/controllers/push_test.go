package controllers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakePushRegistry struct {
	unpaired []string
	upserts  []mobilebridge.PushDevice
	deletes  []string
	err      error
}

func (f *fakePushRegistry) Upsert(dev mobilebridge.PushDevice) error {
	if f.err != nil {
		return f.err
	}
	f.upserts = append(f.upserts, dev)
	return nil
}

func (f *fakePushRegistry) Unpair(id string) error {
	if f.err != nil {
		return f.err
	}
	f.unpaired = append(f.unpaired, id)
	return nil
}

func (f *fakePushRegistry) UnregisterToken(token string) error {
	if f.err != nil {
		return f.err
	}
	f.deletes = append(f.deletes, token)
	return nil
}

func newPushTestServer(t *testing.T, reg *fakePushRegistry) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Push: reg}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRegisterPushDevice(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	body := `{"installId":"inst-1","token":"ExponentPushToken[abc]","platform":"android","deviceName":"Pixel"}`
	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(reg.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(reg.upserts))
	}
	got := reg.upserts[0]
	if got.InstallID != "inst-1" || got.Token != "ExponentPushToken[abc]" || got.Platform != "android" || got.DeviceName != "Pixel" {
		t.Fatalf("upserted device = %+v", got)
	}
	if got.CreatedAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", got)
	}

	var env struct {
		Device struct {
			Token string `json:"token"`
		} `json:"device"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Device.Token != "ExponentPushToken[abc]" {
		t.Fatalf("response token = %q", env.Device.Token)
	}
}

// TestRegisterPushDeviceAcceptsMissingToken pins the identity-announce path: a
// phone that connects before notification permission is granted (or on a
// build that can't mint a token at all) must still be able to register its
// identity and appear in the roster, rather than being rejected outright.
func TestRegisterPushDeviceAcceptsMissingToken(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	body := `{"installId":"inst-announce","platform":"ios","deviceName":"iPhone"}`
	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(reg.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(reg.upserts))
	}
	got := reg.upserts[0]
	if got.InstallID != "inst-announce" || got.Token != "" {
		t.Fatalf("upserted device = %+v, want tokenless inst-announce", got)
	}
}

func TestRegisterPushDeviceRejectsBadToken(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(`{"token":"garbage"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	if len(reg.upserts) != 0 {
		t.Fatalf("bad token reached the registry: %+v", reg.upserts)
	}
}

func TestUnregisterPushDevice(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	// The Expo token's [ ] must be URL-encoded by the client; the daemon must
	// receive the decoded token.
	token := "ExponentPushToken[abc]"
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/push/devices/"+url.PathEscape(token), nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(reg.deletes) != 1 || reg.deletes[0] != token {
		t.Fatalf("deletes = %+v, want [%q]", reg.deletes, token)
	}
}

func TestRegisterPushDeviceStoresInstallID(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	body := `{"installId":"inst-9","token":"ExponentPushToken[abc]","platform":"android","deviceName":"Pixel"}`
	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(reg.upserts) != 1 || reg.upserts[0].InstallID != "inst-9" {
		t.Fatalf("upserts = %+v, want InstallID inst-9", reg.upserts)
	}
}

func TestRegisterPushDeviceSynthesizesMissingInstallID(t *testing.T) {
	reg := &fakePushRegistry{}
	srv := newPushTestServer(t, reg)

	body := `{"token":"ExponentPushToken[abc]"}`
	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if len(reg.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(reg.upserts))
	}
	got := reg.upserts[0].InstallID
	if got == "" || !strings.HasPrefix(got, "legacy-") {
		t.Fatalf("InstallID = %q, want non-empty legacy- prefixed id", got)
	}
}

func TestPushRoutesNotImplementedWithoutRegistry(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(`{"token":"ExponentPushToken[abc]"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", res.StatusCode)
	}
}

// A registration must identify a phone. Accepting an identity-less body would
// mint a permanent row representing no device, and repeating the call would mint
// another — the roster fills with phantoms nobody can act on.
func TestRegisterPushDeviceRequiresAnIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantCode int
	}{
		{"neither installId nor token", `{}`, http.StatusBadRequest},
		{"empty strings for both", `{"installId":"","token":""}`, http.StatusBadRequest},
		{"platform only, still no identity", `{"platform":"android","deviceName":"Pixel"}`, http.StatusBadRequest},
		{"installId alone is enough", `{"installId":"inst-1"}`, http.StatusOK},
		{"token alone is enough (older client)", `{"token":"ExponentPushToken[abc]"}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &fakePushRegistry{}
			srv := newPushTestServer(t, reg)
			res, err := http.Post(srv.URL+"/api/v1/push/devices", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantCode)
			}
			if tc.wantCode == http.StatusBadRequest {
				if len(reg.upserts) != 0 {
					t.Fatalf("a rejected registration still wrote a row: %+v", reg.upserts)
				}
				var env struct {
					Code string `json:"code"`
				}
				_ = json.NewDecoder(res.Body).Decode(&env)
				if env.Code != "MISSING_DEVICE_IDENTITY" {
					t.Fatalf("code = %q, want MISSING_DEVICE_IDENTITY", env.Code)
				}
			}
		})
	}
}
