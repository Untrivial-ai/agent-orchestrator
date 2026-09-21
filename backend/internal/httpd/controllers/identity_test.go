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
)

func identityServer(t *testing.T, hostID string) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		HostID: hostID,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetIdentityReturnsTheHostID(t *testing.T) {
	srv := identityServer(t, "h_b3e07f31-4803-46ac-bced-ded38a0fff71")

	res, err := http.Get(srv.URL + "/api/v1/identity")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", res.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["hostId"] != "h_b3e07f31-4803-46ac-bced-ded38a0fff71" {
		t.Fatalf("hostId = %v", got["hostId"])
	}
	if got["apiVersion"] == nil {
		t.Fatal("apiVersion missing — the phone uses it to negotiate behaviour")
	}
}

// The probe is reachable without a credential, so it must not become a place
// where anything else about the machine leaks.
func TestGetIdentityExposesNothingBeyondIDAndVersion(t *testing.T) {
	srv := identityServer(t, "h_abc")

	res, err := http.Get(srv.URL + "/api/v1/identity")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range got {
		if k != "hostId" && k != "apiVersion" {
			t.Errorf("unexpected field %q in the unauthenticated identity probe", k)
		}
	}
}
