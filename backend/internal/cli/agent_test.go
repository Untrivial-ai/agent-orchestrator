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
