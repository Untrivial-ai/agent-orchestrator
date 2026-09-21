package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCanonicalConfigPreservesExistingSettings(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := projectServer(t, http.StatusOK, `{"status":"ok","project":{"id":"demo","config":{"containerReap":{"disabled":true},"reviewers":[{"harness":"codex","agentConfig":{"model":"gpt-5","mode":"high"}}],"env":{"KEEP":"value"}}}}`)
	writeRunFileFor(t, cfg, srv)
	deps := Deps{ProcessAlive: func(int) bool { return true }}
	out, _, err := executeCLI(t, deps, "project", "get", "demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got projectGetResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Project.Config == nil {
		t.Fatal("project get lost config")
	}
	got.Project.Config.CanonicalRepoURL = "https://gitlab.com/group/repo"
	edited, err := json.Marshal(got.Project.Config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeCLI(t, deps, "project", "set-config", "demo", "--config-json", string(edited)); err != nil {
		t.Fatal(err)
	}
	var body setConfigRequest
	if err := json.Unmarshal(capture.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Config.ContainerReap == nil || !body.Config.ContainerReap.Disabled {
		t.Fatalf("container opt-out lost: %s", capture.body)
	}
	if len(body.Config.Reviewers) != 1 || body.Config.Reviewers[0].AgentConfig == nil || body.Config.Reviewers[0].AgentConfig.Model != "gpt-5" || body.Config.Reviewers[0].AgentConfig.Mode != "high" {
		t.Fatalf("reviewer config lost: %s", capture.body)
	}
	if body.Config.Env["KEEP"] != "value" || body.Config.CanonicalRepoURL != got.Project.Config.CanonicalRepoURL {
		t.Fatalf("config lost: %s", capture.body)
	}
}
