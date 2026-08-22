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

func TestAgentListUsesCachedCatalogByDefault(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
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
	want := []string{"GET /api/v1/agents"}
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
			_, _ = io.WriteString(w, `{"supported":[{"id":"aider","label":"Aider"},{"id":"codex","label":"Codex"},{"id":"goose","label":"Goose"},{"id":"opencode","label":"OpenCode"}],"installed":[{"id":"aider","label":"Aider","authStatus":"unauthorized"},{"id":"codex","label":"Codex","authStatus":"authorized"},{"id":"goose","label":"Goose","authStatus":"unknown"}],"authorized":[{"id":"codex","label":"Codex","authStatus":"authorized"}]}`)
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
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
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

func TestAgentProbe_Success(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe" {
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex","authStatus":"authorized"},"supported":true,"installed":true}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "probe", "codex")
	if err != nil {
		t.Fatalf("agent probe failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"agent: codex", "label: Codex", "supported: supported", "install: installed", "auth: authorized"} {
		if !strings.Contains(out, want) {
			t.Fatalf("probe output missing %q:\n%s", want, out)
		}
	}
	want := []string{"POST /api/v1/agents/codex/probe"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestAgentProbe_JSON(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe" {
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex","authStatus":"unauthorized"},"supported":true,"installed":true}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "probe", "codex", "--json")
	if err != nil {
		t.Fatalf("agent probe --json failed: %v stderr=%s", err, errOut)
	}
	var got agentProbeResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("probe json decode failed: %v\n%s", err, out)
	}
	if !got.Supported || !got.Installed || got.Agent.ID != "codex" || got.Agent.AuthStatus != "unauthorized" {
		t.Fatalf("unexpected probe JSON: %#v", got)
	}
}

func TestAgentProbe_MissingAgentIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "agent", "probe")
	if err == nil {
		t.Fatal("expected missing agent id to fail")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (err=%v)", got, err)
	}
}
