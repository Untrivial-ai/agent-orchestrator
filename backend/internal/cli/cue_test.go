package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCueCreateUsesProjectContextAndPostsOneDefinition(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_PROJECT_ID", "demo")
	t.Setenv("AO_SESSION_ID", "")
	var posts int
	var body cueCreateDTO
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/internal/telemetry/cli-invoked":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"project":{"id":"demo","kind":"single_repo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/demo/cues":
			posts++
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"cue":{"id":"cue-1","projectId":"demo","name":"Review PR","type":"agent","prompt":"Review the current PR"}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)

	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cue", "create", "--name", "Review PR", "--prompt", "Review the current PR")
	if err != nil {
		t.Fatalf("create: %v stderr=%s", err, stderr)
	}
	if posts != 1 || body.Type != "agent" || body.Prompt != "Review the current PR" || body.Command != "" || !strings.Contains(out, "cue-1") {
		t.Fatalf("posts=%d body=%+v output=%q", posts, body, out)
	}
}

func TestCueCreateCommandKeepsDaemonErrorEnvelope(t *testing.T) {
	t.Setenv("AO_PROJECT_ID", "")
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/telemetry/cli-invoked":
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"project":{"id":"demo","kind":"single_repo"}}`)
		case "/api/v1/projects/demo/cues":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"conflict","code":"CUE_NAME_EXISTS","message":"A cue with this name already exists in the project","requestId":"request-1"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cue", "create", "--project", "demo", "--name", "Tests", "--command", "go test ./...")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "CUE_NAME_EXISTS") || !strings.Contains(err.Error(), "request-1") {
		t.Fatalf("err=%v exit=%d", err, ExitCode(err))
	}
}

func TestCueCreateCommandPostsExactShellCommand(t *testing.T) {
	cfg := setConfigEnv(t)
	var body cueCreateDTO
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/telemetry/cli-invoked":
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"project":{"id":"demo","kind":"single_repo"}}`)
		case "/api/v1/projects/demo/cues":
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"cue":{"id":"cue-2","projectId":"demo","name":"Tests","type":"command","command":"go test ./..."}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)

	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cue", "create", "--project", "demo", "--name", "Tests", "--command", "go test ./...", "--json")
	if err != nil || body.Type != "command" || body.Command != "go test ./..." || body.Prompt != "" || !strings.Contains(out, `"id": "cue-2"`) {
		t.Fatalf("create: err=%v stderr=%s body=%+v output=%q", err, stderr, body, out)
	}
}

func TestCueCreateRejectsIncompleteOrAmbiguousDefinition(t *testing.T) {
	setConfigEnv(t)
	for _, args := range [][]string{
		{"cue", "create", "--command", "go test ./..."},
		{"cue", "create", "--name", "Tests"},
		{"cue", "create", "--name", "Tests", "--command", "go test ./...", "--prompt", "Run tests"},
	} {
		_, _, err := executeCLI(t, Deps{}, args...)
		if err == nil || ExitCode(err) != 2 {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestCueCommandExposesCreateAndReadOnlyList(t *testing.T) {
	cmd := newCueCommand(&commandContext{})
	if len(cmd.Commands()) != 2 || cmd.Commands()[0].Name() != "create" || cmd.Commands()[1].Name() != "list" {
		t.Fatalf("unexpected cue commands: %v", cmd.Commands())
	}
}

func TestCueListReadsProjectDefinitions(t *testing.T) {
	cfg := setConfigEnv(t)
	var listed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/telemetry/cli-invoked":
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"project":{"id":"demo","kind":"single_repo"}}`)
		case "/api/v1/projects/demo/cues":
			listed = true
			_, _ = io.WriteString(w, `{"cues":[{"id":"cue-1","projectId":"demo","name":"Review PR","type":"agent","prompt":"Review the current PR"}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)

	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cue", "list", "--project", "demo", "--json")
	if err != nil || !listed || !strings.Contains(out, `"prompt": "Review the current PR"`) {
		t.Fatalf("list: err=%v stderr=%s listed=%t output=%q", err, stderr, listed, out)
	}
}
