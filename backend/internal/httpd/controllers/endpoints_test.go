package controllers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakeEndpointSource struct {
	endpoints []mobilebridge.Endpoint
}

func (f fakeEndpointSource) AdvertisedEndpoints() []mobilebridge.Endpoint { return f.endpoints }

// The phone re-reads this after every successful connect. It is what makes a
// rotated tunnel hostname or a changed LAN address heal without re-pairing, and
// it cannot live under /api/v1/mobile because lanControlBlock 404s that whole
// prefix on the LAN listener — the only listener a phone can reach.
func TestGetEndpointsReturnsTheAdvertisedList(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Endpoints: fakeEndpointSource{endpoints: []mobilebridge.Endpoint{
			{Kind: mobilebridge.KindLAN, Host: "192.168.1.42", Port: 3011},
			{Kind: mobilebridge.KindTunnel, Host: "abc.trycloudflare.com", Port: 443, Secure: true},
		}},
	}, httpd.ControlDeps{}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/v1/endpoints")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d want 200", res.StatusCode)
	}
	var body struct {
		Endpoints []mobilebridge.Endpoint `json:"endpoints"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Endpoints) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(body.Endpoints))
	}
	if body.Endpoints[0].Host != "192.168.1.42" {
		t.Errorf("first endpoint = %+v", body.Endpoints[0])
	}
}

func TestGetEndpointsIsReachableFromTheLANListener(t *testing.T) {
	// The whole point of not putting it under /api/v1/mobile.
	if httpd.IsLANControlBlockedPathForTest("/api/v1/endpoints") {
		t.Fatal("/api/v1/endpoints is blocked on the LAN listener, so the phone can never refresh")
	}
}
