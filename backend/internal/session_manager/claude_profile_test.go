package sessionmanager

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestClaudeProfileSelectionAndSessionPin(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/daemon")
	base, role, spawn, empty := "/base", "/role", "/spawn", ""
	config := domain.ProjectConfig{Env: map[string]string{"CLAUDE_CONFIG_DIR": "/legacy", "KEEP": "yes"}, AgentConfig: domain.AgentConfig{ClaudeConfigDir: &base}, Worker: domain.RoleOverride{AgentConfig: domain.AgentConfig{ClaudeConfigDir: &role}}}
	for _, tc := range []struct {
		name     string
		override *string
		want     string
	}{{"role", nil, role}, {"spawn", &spawn, spawn}, {"explicit default", &empty, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := applySpawnAgentConfig(effectiveAgentConfig(domain.KindWorker, config), ports.AgentConfig{ClaudeConfigDir: tc.override})
			rec := domain.SessionRecord{Kind: domain.KindWorker, Metadata: domain.SessionMetadata{ClaudeConfigDir: resolvedClaudeProfile(resolved, config.Env)}}
			changed := config
			changed.AgentConfig = domain.AgentConfig{}
			changed.Worker = domain.RoleOverride{}
			changed.Env = map[string]string{"CLAUDE_CONFIG_DIR": "/changed", "KEEP": "yes"}
			env := sessionProfileEnv(rec, changed)
			if env["CLAUDE_CONFIG_DIR"] != tc.want || env["KEEP"] != "yes" {
				t.Fatalf("restored env = %v, want pinned %q", env, tc.want)
			}
			if changed.Env["CLAUDE_CONFIG_DIR"] != "/changed" {
				t.Fatal("mutated project environment")
			}
		})
	}
	if got := *resolvedClaudeProfile(domain.AgentConfig{}, config.Env); got != "/legacy" {
		t.Fatalf("legacy = %q", got)
	}
	if got := *resolvedClaudeProfile(domain.AgentConfig{}, nil); got != "/daemon" {
		t.Fatalf("daemon = %q", got)
	}
}

func TestSpawnPinsClaudeProfileAndLaunchesSelection(t *testing.T) {
	st := newFakeStore()
	profile := "/profiles/client-named"
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: domain.ProjectConfig{Worker: domain.RoleOverride{Harness: domain.HarnessClaudeCode}, Env: map[string]string{"CLAUDE_CONFIG_DIR": "/legacy"}}}
	rt := &fakeRuntime{}
	agent := &recordingAgent{}
	m := New(Deps{Runtime: rt, Agents: singleAgent{agent: agent}, Workspace: &fakeWorkspace{}, Store: st, Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st}, LookPath: func(string) (string, error) { return "/bin/true", nil }})
	rec, _, _, err := m.Spawn(ctx, ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker, AgentConfig: ports.AgentConfig{ClaudeConfigDir: &profile}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Metadata.ClaudeConfigDir == nil || *rec.Metadata.ClaudeConfigDir != profile || rt.lastCfg.Env["CLAUDE_CONFIG_DIR"] != profile {
		t.Fatalf("profile not pinned/launched: metadata=%+v env=%v", rec.Metadata, rt.lastCfg.Env)
	}
}
