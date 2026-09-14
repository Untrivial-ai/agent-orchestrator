package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAgentListEnsuresDisplayReadinessByDefault(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure" {
			_, _ = io.WriteString(w, readinessAgentsJSON("codex", "not_installed", "unknown"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls")
	if err != nil {
		t.Fatalf("agent ls failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "codex") || !strings.Contains(out, "needs install") {
		t.Fatalf("output missing table labels:\n%s", out)
	}
	want := []string{"POST /api/v1/agents/readiness/ensure"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestAgentListLaunchUsesLaunchReadinessAndShowsReason(t *testing.T) {
	cfg := setConfigEnv(t)
	var gotPurpose string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure" {
			var req ensureAgentReadinessRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode readiness request: %v", err)
			}
			gotPurpose = req.Purpose
			_, _ = io.WriteString(w, `{"agents":[`+
				`{"id":"codex","label":"Codex","installation":{"state":"installed","freshness":"fresh","reasonCode":"installed","reason":"installed"},"authentication":{"state":"unknown","freshness":"fresh","reasonCode":"auth_check_failed","reason":"Codex account setup did not complete."},"effectiveReadiness":"unknown","usageCount":0},`+
				`{"id":"opencode","label":"OpenCode","installation":{"state":"installed","freshness":"fresh","reasonCode":"installed","reason":"installed"},"authentication":{"state":"authorized","freshness":"fresh","reasonCode":"authorized","reason":"authorized"},"effectiveReadiness":"ready","usageCount":0}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls", "--launch")
	if err != nil {
		t.Fatalf("agent ls --launch failed: %v stderr=%s", err, errOut)
	}
	if gotPurpose != "launch" {
		t.Fatalf("readiness purpose = %q, want launch", gotPurpose)
	}
	for _, want := range []string{"LAUNCH", "codex", "unknown", "auth_check_failed", "opencode", "ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestAgentListLaunchShowsFailureReasonWhenAuthStateIsStillAuthorized(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure" {
			_, _ = io.WriteString(w, `{"agents":[{"id":"codex","label":"Codex","installation":{"state":"installed","freshness":"fresh","reasonCode":"installed","reason":"installed"},"authentication":{"state":"authorized","freshness":"stale","reasonCode":"auth_check_failed","reason":"Codex account setup did not complete."},"effectiveReadiness":"unknown","usageCount":0}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls", "--launch")
	if err != nil {
		t.Fatalf("agent ls --launch failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "auth_check_failed") {
		t.Fatalf("launch output hid blocking reason:\n%s", out)
	}
}

func TestAgentListRejectsLaunchWithRefresh(t *testing.T) {
	cfg := setConfigEnv(t)
	var readinessRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agents/readiness/") || r.URL.Path == "/api/v1/agents/refresh" {
			readinessRequests++
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls", "--launch", "--refresh")
	if err == nil {
		t.Fatal("agent ls --launch --refresh unexpectedly succeeded")
	}
	if readinessRequests != 0 {
		t.Fatalf("readiness requests = %d, want 0", readinessRequests)
	}
}

func TestAgentListRefreshAndStatuses(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh" {
			_, _ = io.WriteString(w, `{"supported":[`+
				`{"id":"aider","label":"Aider","authStatus":"unauthorized"},`+
				`{"id":"codex","label":"Codex","authStatus":"authorized"},`+
				`{"id":"goose","label":"Goose","authStatus":"unknown"},`+
				`{"id":"opencode","label":"OpenCode","authStatus":"unknown"}],`+
				`"installed":[`+
				`{"id":"aider","label":"Aider","authStatus":"unauthorized"},`+
				`{"id":"codex","label":"Codex","authStatus":"authorized"},`+
				`{"id":"goose","label":"Goose","authStatus":"unknown"}],`+
				`"authorized":[{"id":"codex","label":"Codex","authStatus":"authorized"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls", "--refresh")
	if err != nil {
		t.Fatalf("agent ls --refresh failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"codex", "authorized", "aider", "needs auth", "goose", "auth unknown", "opencode", "needs install"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	want := []string{"POST /api/v1/agents/refresh"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestAgentListJSONEmitsRawCatalog(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/readiness/ensure" {
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "ls", "--json")
	if err != nil {
		t.Fatalf("agent ls --json failed: %v stderr=%s", err, errOut)
	}
	var inv agentInventory
	if err := json.Unmarshal([]byte(out), &inv); err != nil {
		t.Fatalf("json output did not decode: %v\n%s", err, out)
	}
	if len(inv.Supported) != 1 || len(inv.Installed) != 1 || len(inv.Authorized) != 1 {
		t.Fatalf("inventory = %#v", inv)
	}
}

func TestReadinessInventoryProjectsAuthNotApplicableAsLegacyAuthorized(t *testing.T) {
	inv := readinessInventory(agentReadinessResponse{Agents: []agentReadinessSnapshot{{
		ID: "local", Label: "Local",
		Installation:   agentReadinessObservation{State: "installed"},
		Authentication: agentReadinessObservation{State: "not_applicable"},
	}}})
	if len(inv.Authorized) != 1 || inv.Authorized[0].AuthStatus != "authorized" {
		t.Fatalf("legacy authorized projection = %#v", inv.Authorized)
	}
}
