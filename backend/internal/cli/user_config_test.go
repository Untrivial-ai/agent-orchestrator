package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// userConfigServer is a stub daemon that records the last request and replies
// with a fixed body for any /api/v1/user-config path.
func userConfigServer(t *testing.T, status int, respBody string) (*httptest.Server, *projectCapture) {
	t.Helper()
	capture := &projectCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.method = r.Method
		capture.path = r.URL.Path
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capture.body = data
		if !strings.HasPrefix(r.URL.Path, "/api/v1/user-config") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func TestUserConfigSet_ModelAndPermissionFlags(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := userConfigServer(t, http.StatusOK, `{"agentConfig":{"model":"claude-opus-4-8","permissions":"auto"}}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "user-config", "set", "--model", "claude-opus-4-8", "--permission", "auto")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPut || capture.path != "/api/v1/user-config" {
		t.Fatalf("request = %s %s, want PUT /api/v1/user-config", capture.method, capture.path)
	}
	var got setUserConfigRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	if got.AgentConfig.Model != "claude-opus-4-8" || got.AgentConfig.Permissions != "auto" {
		t.Fatalf("request agentConfig = %#v", got.AgentConfig)
	}
}

func TestUserConfigSet_Clear(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := userConfigServer(t, http.StatusOK, `{"agentConfig":{}}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "user-config", "set", "--clear")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", capture.method)
	}
	// --clear sends an empty agentConfig object.
	var got setUserConfigRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	if got.AgentConfig.Model != "" || got.AgentConfig.Permissions != "" {
		t.Fatalf("clear request agentConfig = %#v, want empty", got.AgentConfig)
	}
}

func TestUserConfigSet_ClearPrecedenceOverFieldFlags(t *testing.T) {
	// --clear wins over field flags: --clear --model X sends an empty config,
	// ignoring --model. Matches the project set-config precedence.
	cfg := setConfigEnv(t)
	srv, capture := userConfigServer(t, http.StatusOK, `{"agentConfig":{}}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "user-config", "set", "--clear", "--model", "claude-opus-4-8", "--permission", "auto")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var got setUserConfigRequest
	if err := json.Unmarshal(capture.body, &got); err != nil {
		t.Fatalf("decode request: %v\nbody=%s", err, capture.body)
	}
	if got.AgentConfig.Model != "" || got.AgentConfig.Permissions != "" {
		t.Fatalf("--clear should win; agentConfig = %#v, want empty", got.AgentConfig)
	}
}

func TestUserConfigSet_RequiresAFlag(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := userConfigServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "user-config", "set")
	if err == nil {
		t.Fatal("set with no flags: want usage error, got nil")
	}
}

func TestUserConfigGet_PrintsUnset(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := userConfigServer(t, http.StatusOK, `{"agentConfig":{}}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "user-config", "get")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodGet || capture.path != "/api/v1/user-config" {
		t.Fatalf("request = %s %s, want GET /api/v1/user-config", capture.method, capture.path)
	}
	if !strings.Contains(out, "(unset)") {
		t.Fatalf("output = %q, want it to contain (unset)", out)
	}
}
