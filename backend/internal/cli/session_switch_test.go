package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type agentSwitchRequestCapture struct {
	mu     sync.Mutex
	method string
	path   string
	body   []byte
	count  int
}

func (c *agentSwitchRequestCapture) record(r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return
	}
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.method = r.Method
	c.path = r.URL.Path
	c.body = body
	c.count++
}

func (c *agentSwitchRequestCapture) snapshot() (string, string, []byte, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.method, c.path, append([]byte(nil), c.body...), c.count
}

func agentSwitchFixture(id, state string) string {
	b, _ := json.Marshal(map[string]any{
		"id":                 id,
		"sessionId":          "demo-1",
		"fromHarness":        "claude-code",
		"targetHarness":      "codex",
		"state":              state,
		"agentHandoffStatus": "not_attempted",
		"requestedAt":        "2026-08-04T10:00:00Z",
		"updatedAt":          "2026-08-04T10:01:00Z",
	})
	return string(b)
}

func agentSwitchFixtureWithPrivateFields(id, state string) string {
	var value map[string]any
	_ = json.Unmarshal([]byte(agentSwitchFixture(id, state)), &value)
	value["idempotencyKey"] = "private-key"
	value["sourceNativeSessionRef"] = "native-source"
	value["agentHandoff"] = map[string]any{"private": true}
	value["agentHandoffPath"] = "/private/ao/handoff.json"
	value["agentHandoffHash"] = "private-hash"
	value["sourceGenerationId"] = "generation-1"
	value["errorDetail"] = "private detail"
	value["reason"] = "manual"
	value["completedAt"] = "2026-08-04T10:01:00Z"
	b, _ := json.Marshal(value)
	return string(b)
}

func TestSessionSwitchAgentPostsRequestAndPrintsSwitch(t *testing.T) {
	cfg := setConfigEnv(t)
	capture := &agentSwitchRequestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/switch-agent" {
			_, _ = io.WriteString(w, `{"switch":`+agentSwitchFixture("switch-1", "preparing_handoff")+`}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"session", "switch-agent", "demo-1", "codex",
		"--note", "preserve the current investigation",
		"--idempotency-key", "switch-key",
	)
	if err != nil {
		t.Fatalf("session switch-agent failed: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/sessions/demo-1/switch-agent" || count != 1 {
		t.Fatalf("request = %s %s (count %d)", method, path, count)
	}
	var got switchAgentRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	want := switchAgentRequest{
		TargetHarness:  "codex",
		Note:           "preserve the current investigation",
		IdempotencyKey: "switch-key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
	for _, needle := range []string{"id: switch-1", "from: claude-code", "target: codex", "state: preparing_handoff"} {
		if !strings.Contains(out, needle) {
			t.Errorf("output missing %q:\n%s", needle, out)
		}
	}
}

func TestSessionSwitchAgentOutlivesSharedHTTPClientTimeout(t *testing.T) {
	if sharedTimeout := DefaultDeps().HTTPClient.Timeout; switchAgentCommandTimeout <= sharedTimeout {
		t.Fatalf("switch timeout = %s, must exceed shared HTTP client timeout %s", switchAgentCommandTimeout, sharedTimeout)
	}

	// Scale the production 2s-versus-7m relationship down so this regression
	// test proves the switch request does not inherit the shared client timeout
	// without adding multiple seconds to every CLI test run.
	const (
		sharedTimeout = 20 * time.Millisecond
		responseDelay = 75 * time.Millisecond
	)
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sessions/demo-1/switch-agent" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(responseDelay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"switch":`+agentSwitchFixture("switch-delayed", "completed")+`}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		HTTPClient:   &http.Client{Timeout: sharedTimeout},
		ProcessAlive: func(int) bool { return true },
	}, "session", "switch-agent", "demo-1", "codex", "--json")
	if err != nil {
		t.Fatalf("delayed switch-agent failed (shared client timeout leaked): %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `"id": "switch-delayed"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestSessionSwitchAgentJSONAndTypedDaemonError(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		cfg := setConfigEnv(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/switch-agent" {
				_, _ = io.WriteString(w, `{"switch":`+agentSwitchFixtureWithPrivateFields("switch-1", "preparing_handoff")+`}`)
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(srv.Close)
		writeRunFileFor(t, cfg, srv)

		out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
			"session", "switch-agent", "demo-1", "codex", "--json")
		if err != nil {
			t.Fatalf("session switch-agent --json failed: %v\nstderr=%s", err, errOut)
		}
		var got agentSwitchResponse
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		if got.Switch.ID != "switch-1" || got.Switch.TargetHarness != "codex" {
			t.Fatalf("unexpected response: %#v", got)
		}
		var envelope map[string]map[string]any
		if err := json.Unmarshal([]byte(out), &envelope); err != nil {
			t.Fatalf("decode JSON envelope: %v", err)
		}
		for _, privateField := range []string{
			"reason",
			"completedAt",
			"idempotencyKey",
			"sourceNativeSessionRef",
			"targetNativeSessionRef",
			"agentHandoff",
			"agentHandoffPath",
			"agentHandoffHash",
			"sourceGenerationId",
			"targetGenerationId",
			"errorDetail",
		} {
			if _, ok := envelope["switch"][privateField]; ok {
				t.Errorf("private field %q leaked in output: %s", privateField, out)
			}
		}
	})

	t.Run("typed daemon error", func(t *testing.T) {
		cfg := setConfigEnv(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"message":"another switch is active","code":"AGENT_SWITCH_IN_PROGRESS","requestId":"req-switch-1"}`)
		}))
		t.Cleanup(srv.Close)
		writeRunFileFor(t, cfg, srv)

		_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
			"session", "switch-agent", "demo-1", "codex")
		if err == nil {
			t.Fatal("expected daemon error")
		}
		if ExitCode(err) != 1 {
			t.Fatalf("ExitCode = %d, want 1", ExitCode(err))
		}
		for _, needle := range []string{"another switch is active", "AGENT_SWITCH_IN_PROGRESS", "req-switch-1"} {
			if !strings.Contains(err.Error(), needle) {
				t.Errorf("error %q missing %q", err, needle)
			}
		}
	})
}

func TestSessionAgentSwitchList(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1/agent-switches":
			_, _ = io.WriteString(w, `{"switches":[`+agentSwitchFixture("switch-1", "completed")+`]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	listOut, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"session", "agent-switch", "ls", "demo-1")
	if err != nil {
		t.Fatalf("agent-switch ls failed: %v\nstderr=%s", err, errOut)
	}
	for _, needle := range []string{"ID", "FROM", "TARGET", "switch-1", "claude-code", "codex", "completed"} {
		if !strings.Contains(listOut, needle) {
			t.Errorf("list output missing %q:\n%s", needle, listOut)
		}
	}
}

func TestSessionHelpHidesInternalHandoffCommand(t *testing.T) {
	out, errOut, err := executeCLI(t, Deps{}, "session", "--help")
	if err != nil {
		t.Fatalf("session --help failed: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "handoff") {
		t.Fatalf("session help exposed internal handoff command:\n%s", out)
	}
}

func TestSessionHandoffSubmitUsesEnvironmentSessionAndEmbedsRawJSON(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "demo-1")
	handoffPath := filepath.Join(t.TempDir(), "handoff.json")
	handoff := []byte(`{"summary":"fixed parser","nextSteps":["run tests"],"facts":{"branch":"feature/switch"}}`)
	if err := os.WriteFile(handoffPath, handoff, 0o600); err != nil {
		t.Fatalf("write handoff fixture: %v", err)
	}
	capture := &agentSwitchRequestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/agent-switches/switch-1/handoff" {
			_, _ = io.WriteString(w, `{"switch":`+agentSwitchFixture("switch-1", "preparing_handoff")+`}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"session", "handoff", "submit",
		"--switch", "switch-1",
		"--source-generation", "generation-1",
		"--file", handoffPath,
	)
	if err != nil {
		t.Fatalf("handoff submit failed: %v\nstderr=%s", err, errOut)
	}
	method, path, body, count := capture.snapshot()
	if method != http.MethodPost || path != "/api/v1/sessions/demo-1/agent-switches/switch-1/handoff" || count != 1 {
		t.Fatalf("request = %s %s (count %d)", method, path, count)
	}
	var got submitAgentHandoffRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode request: %v; body=%s", err, body)
	}
	if got.SourceGenerationID != "generation-1" {
		t.Errorf("sourceGenerationId = %q", got.SourceGenerationID)
	}
	var gotHandoff, wantHandoff any
	if err := json.Unmarshal(got.Handoff, &gotHandoff); err != nil {
		t.Fatalf("decode request handoff: %v", err)
	}
	if err := json.Unmarshal(handoff, &wantHandoff); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotHandoff, wantHandoff) {
		t.Fatalf("handoff = %#v, want %#v", gotHandoff, wantHandoff)
	}
	if !strings.Contains(out, "handoff submitted for switch switch-1 (state: preparing_handoff)") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestSessionHandoffSubmitExplicitSessionOverridesEnvironment(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "wrong-session")
	handoffPath := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(handoffPath, []byte(`{"summary":"ready"}`), 0o600); err != nil {
		t.Fatalf("write handoff fixture: %v", err)
	}
	capture := &agentSwitchRequestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/agent-switches/switch-1/handoff" {
			_, _ = io.WriteString(w, `{"switch":`+agentSwitchFixture("switch-1", "preparing_handoff")+`}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"session", "handoff", "submit",
		"--session", "demo-1",
		"--switch", "switch-1",
		"--source-generation", "generation-1",
		"--file", handoffPath,
	)
	if err != nil {
		t.Fatalf("handoff submit failed: %v\nstderr=%s", err, errOut)
	}
	_, gotPath, _, count := capture.snapshot()
	if gotPath != "/api/v1/sessions/demo-1/agent-switches/switch-1/handoff" || count != 1 {
		t.Fatalf("path = %q (count %d)", gotPath, count)
	}
}

func TestSessionHandoffSubmitValidatesInputsBeforeDaemonCall(t *testing.T) {
	cfg := setConfigEnv(t)
	capture := &agentSwitchRequestCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.record(r)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	invalidJSONPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidJSONPath, []byte(`{"summary":`), 0o600); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}
	nonObjectJSONPath := filepath.Join(t.TempDir(), "array.json")
	if err := os.WriteFile(nonObjectJSONPath, []byte(`["not", "an", "object"]`), 0o600); err != nil {
		t.Fatalf("write non-object fixture: %v", err)
	}

	tests := []struct {
		name    string
		envID   string
		args    []string
		wantErr string
	}{
		{
			name:    "missing session",
			args:    []string{"session", "handoff", "submit", "--switch", "switch-1", "--source-generation", "generation-1", "--file", invalidJSONPath},
			wantErr: "pass --session or set AO_SESSION_ID",
		},
		{
			name:    "missing switch",
			envID:   "demo-1",
			args:    []string{"session", "handoff", "submit", "--source-generation", "generation-1", "--file", invalidJSONPath},
			wantErr: "--switch is required",
		},
		{
			name:    "missing generation",
			envID:   "demo-1",
			args:    []string{"session", "handoff", "submit", "--switch", "switch-1", "--file", invalidJSONPath},
			wantErr: "--source-generation is required",
		},
		{
			name:    "missing file",
			envID:   "demo-1",
			args:    []string{"session", "handoff", "submit", "--switch", "switch-1", "--source-generation", "generation-1"},
			wantErr: "--file is required",
		},
		{
			name:    "invalid json",
			envID:   "demo-1",
			args:    []string{"session", "handoff", "submit", "--switch", "switch-1", "--source-generation", "generation-1", "--file", invalidJSONPath},
			wantErr: "handoff file must contain valid JSON",
		},
		{
			name:    "non-object json",
			envID:   "demo-1",
			args:    []string{"session", "handoff", "submit", "--switch", "switch-1", "--source-generation", "generation-1", "--file", nonObjectJSONPath},
			wantErr: "handoff file must contain a JSON object",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", tc.envID)
			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode = %d, want 2", ExitCode(err))
			}
		})
	}
	_, _, _, count := capture.snapshot()
	if count != 0 {
		t.Fatalf("daemon API contacted %d times for invalid input", count)
	}
}
