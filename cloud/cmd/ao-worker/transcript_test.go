package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

func TestEncodeClaudeProjectDir(t *testing.T) {
	cases := map[string]string{
		"/home/ao/work":               "-home-ao-work",
		"/Users/x/Desktop/agent-orch": "-Users-x-Desktop-agent-orch",
		"/tmp/a.b/c_d":                "-tmp-a-b-c-d",
		"/workspace":                  "-workspace",
	}
	for in, want := range cases {
		if got := encodeClaudeProjectDir(in); got != want {
			t.Errorf("encodeClaudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaudeResolverLocatesDeterministicTranscript(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	const sessionID = "sess-abc"
	claudeID := agentruntime.ClaudeSessionID(sessionID)
	// Claude writes under a cwd-encoded project directory; the resolver globs
	// across project directories, so an unrelated encoding here still matches.
	projectDir := filepath.Join(configDir, "projects", "-workspace-agent")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, claudeID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := transcriptResolver{harness: "claude-code", workspace: "/workspace", aoSessionID: sessionID}
	id, got, ok := r.locate()
	if !ok {
		t.Fatal("expected to locate the deterministic Claude transcript")
	}
	if id != claudeID {
		t.Errorf("agent session id = %q, want %q", id, claudeID)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
}

func TestClaudeResolverPrefersLaunchIdentity(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	const nativeID = "11111111-2222-3333-4444-555555555555"
	projectDir := filepath.Join(configDir, "projects", "-workspace")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, nativeID+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := transcriptResolver{
		harness:              "claude-code",
		workspace:            "/workspace",
		aoSessionID:          "sess-abc",
		launchAgentSessionID: nativeID,
	}
	id, _, ok := r.locate()
	if !ok || id != nativeID {
		t.Fatalf("locate() = (%q, ok=%v), want the launch-provided native id", id, ok)
	}
}

func TestClaudeRehydratePathUsesEncodedProjectDir(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	r := transcriptResolver{harness: "claude-code", workspace: "/home/ao/work"}
	const id = "abc-123"
	got, err := r.rehydratePath(id)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configDir, "projects", "-home-ao-work", id+".jsonl")
	if got != want {
		t.Errorf("rehydratePath = %q, want %q", got, want)
	}
}

func TestCodexRolloutIDParsing(t *testing.T) {
	const uuid = "0198e5b1-1a2b-4c3d-8e4f-0a1b2c3d4e5f"
	if got := codexRolloutID("rollout-2026-09-16T10-30-00-" + uuid + ".jsonl"); got != uuid {
		t.Errorf("codexRolloutID = %q, want %q", got, uuid)
	}
	if got := codexRolloutID("rollout-not-a-uuid.jsonl"); got != "" {
		t.Errorf("codexRolloutID(non-uuid) = %q, want empty", got)
	}
}

func TestCodexResolverRehydratePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	r := transcriptResolver{harness: "codex", workspace: "/workspace"}
	const id = "0198e5b1-1a2b-4c3d-8e4f-0a1b2c3d4e5f"
	got, err := r.rehydratePath(id)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "sessions", "rollout-"+id+".jsonl")
	if got != want {
		t.Errorf("rehydratePath = %q, want %q", got, want)
	}
}

func TestUnsupportedHarnessLocateIsNoop(t *testing.T) {
	r := transcriptResolver{harness: "cursor", workspace: "/workspace", dataDir: t.TempDir()}
	if _, _, ok := r.locate(); ok {
		t.Fatal("cursor locate() should be a no-op until implemented")
	}
	if _, err := r.rehydratePath("id"); err == nil {
		t.Fatal("cursor rehydratePath should report unsupported")
	}
}

func TestGetTranscript404IsNothingCaptured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	_, ok, err := c.getTranscript(context.Background())
	if err != nil {
		t.Fatalf("getTranscript: %v", err)
	}
	if ok {
		t.Fatal("404 must report nothing captured, not a hit")
	}
}

func TestGetTranscript200Decodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != transcriptPath {
			t.Errorf("called %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"agentSessionId":"a1","harness":"claude-code","transcript":"eyJ4IjoxfQ==","preservedGitRef":"refs/ao/preserved/s1"}`))
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	got, ok, err := c.getTranscript(context.Background())
	if err != nil || !ok {
		t.Fatalf("getTranscript ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "a1" || got.Harness != "claude-code" || got.PreservedGitRef != "refs/ao/preserved/s1" {
		t.Fatalf("decoded wrong checkpoint: %+v", got)
	}
}

func TestPutTranscriptSendsPut(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	c := &client{baseURL: server.URL, http: server.Client()}
	if err := c.putTranscript(context.Background(), transcriptCheckpoint{AgentSessionID: "a1"}); err != nil {
		t.Fatalf("putTranscript: %v", err)
	}
	if method != http.MethodPut || path != transcriptPath {
		t.Fatalf("called %s %s, want PUT %s", method, path, transcriptPath)
	}
}
