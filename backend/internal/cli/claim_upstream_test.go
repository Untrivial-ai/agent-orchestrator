package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClaimCommandsCanonicalRepository(t *testing.T) {
	for _, provider := range []struct{ name, origin, canonical, pr string }{
		{"github", "https://github.com/alice/repo", "https://github.com/acme/repo", "https://github.com/acme/repo/pull/7"},
		{"gitlab custom port", "https://gitlab.example.com:8443/alice/repo", "https://gitlab.example.com:8443/group/sub/repo", "https://gitlab.example.com:8443/group/sub/repo/-/merge_requests/7"},
		{"gitlab", "https://gitlab.com/alice/team/repo", "https://gitlab.com/group/subgroup/repo", "https://gitlab.com/group/subgroup/repo/-/merge_requests/7"},
	} {
		for _, spawn := range []bool{false, true} {
			for _, ref := range []string{"7", provider.pr} {
				t.Run(fmt.Sprintf("%s/spawn=%t/%s", provider.name, spawn, ref), func(t *testing.T) {
					cfg := setConfigEnv(t)
					var captured claimPRRequest
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch {
						case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
							_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "project": projectDetails{ID: "demo", Path: "/repo/demo", Repo: provider.origin, Config: &projectConfig{CanonicalRepoURL: provider.canonical}}})
						case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
							_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
						case r.URL.Path == "/api/v1/agents/readiness/ensure":
							_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
						case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
							_, _ = io.WriteString(w, `{"session":{"id":"demo-1","status":"idle"}}`)
						case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
							_ = json.NewDecoder(r.Body).Decode(&captured)
							_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[{"url":"`+provider.pr+`","number":7}],"takenOverFrom":[]}`)
						default:
							http.NotFound(w, r)
						}
					}))
					t.Cleanup(srv.Close)
					writeRunFileFor(t, cfg, srv)
					args := []string{"session", "claim-pr", "demo-1", ref}
					if spawn {
						args = []string{"spawn", "--project", "demo", "--agent", "codex", "--name", "worker", "--claim-pr", ref}
					}
					_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
					if err != nil {
						t.Fatal(err)
					}
					if captured.PR != provider.pr {
						t.Fatalf("claim target = %q, want %q", captured.PR, provider.pr)
					}
				})
			}
		}
	}
}

func TestProjectSetConfigCanonicalRepo(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := projectServer(t, http.StatusOK, `{"project":{"id":"demo"}}`)
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "project", "set-config", "demo", "--canonical-repo-url", "https://github.com/acme/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(capture.body), `"canonicalRepoURL":"https://github.com/acme/repo"`) {
		t.Fatalf("body=%s", capture.body)
	}
}
