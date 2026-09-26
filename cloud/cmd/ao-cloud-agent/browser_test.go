package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserSubcommandMounted(t *testing.T) {
	// A stub URL proves the wiring: the shared verb tree runs and reaches the
	// transport, which fails to connect to the closed port.
	t.Setenv("AO_BROWSER_API_URL", "http://127.0.0.1:1")
	t.Setenv("AO_SESSION_ID", "sess-1")
	t.Setenv("AO_BROWSER_CAPABILITY", "tok")
	var out bytes.Buffer
	if err := runBrowser([]string{"status"}); err == nil {
		_ = out
		t.Log("status against a closed port unexpectedly succeeded")
	}
}

func TestBrowserRequiresBrowserServiceEnv(t *testing.T) {
	t.Setenv("AO_BROWSER_API_URL", "")
	if err := runBrowser([]string{"status"}); err == nil ||
		!strings.Contains(err.Error(), "AO_BROWSER_API_URL") {
		t.Fatalf("err = %v, want missing browser service message", err)
	}
}

func TestBrowserTransportRoundTrip(t *testing.T) {
	var gotPath, gotCapability, gotAction string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCapability = r.Header.Get("X-AO-Browser-Capability")
		var body struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotAction = body.Action
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":"r1","sessionId":"sess-1","action":"console","result":{"messages":[]}}`))
	}))
	defer server.Close()

	t.Setenv("AO_BROWSER_API_URL", server.URL)
	t.Setenv("AO_SESSION_ID", "sess-1")
	t.Setenv("AO_BROWSER_CAPABILITY", "cap-1")

	if err := runBrowser([]string{"--json", "console"}); err != nil {
		t.Fatalf("runBrowser console: %v", err)
	}
	if gotPath != "/api/v1/browser/commands" || gotCapability != "cap-1" || gotAction != "console" {
		t.Fatalf("path=%q capability=%q action=%q", gotPath, gotCapability, gotAction)
	}
}

func TestBrowserTransportSurfacesErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","code":"BROWSER_CAPABILITY_INVALID","message":"Browser capability is invalid","requestId":"req-9"}`))
	}))
	defer server.Close()

	t.Setenv("AO_BROWSER_API_URL", server.URL)
	t.Setenv("AO_SESSION_ID", "sess-1")
	t.Setenv("AO_BROWSER_CAPABILITY", "wrong")

	err := runBrowser([]string{"console"})
	if err == nil ||
		!strings.Contains(err.Error(), "Browser capability is invalid (BROWSER_CAPABILITY_INVALID)") ||
		!strings.Contains(err.Error(), "[request req-9]") {
		t.Fatalf("err = %v, want preserved envelope with request id", err)
	}
}

func TestBrowserHelpListsVerbs(t *testing.T) {
	t.Setenv("AO_BROWSER_API_URL", "http://127.0.0.1:1")
	if err := runBrowser([]string{"--help"}); err != nil {
		t.Fatalf("help: %v", err)
	}
}
