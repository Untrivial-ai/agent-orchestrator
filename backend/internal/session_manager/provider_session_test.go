package sessionmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	providersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/provider"
)

type recordingProviderResolver struct {
	config                providersvc.RuntimeConfig
	newErr, resumeErr     error
	newCalls, resumeCalls int
}

func (r *recordingProviderResolver) ResolveRuntimeProvider(context.Context, domain.ProviderID, domain.ProviderModelID) (providersvc.RuntimeConfig, error) {
	r.newCalls++
	return r.config, r.newErr
}

func (r *recordingProviderResolver) ResolveRuntimeProviderForResume(context.Context, domain.ProviderID, domain.ProviderModelID) (providersvc.RuntimeConfig, error) {
	r.resumeCalls++
	return r.config, r.resumeErr
}

func TestSessionManagerDeepSeekProviderNewAndNativeResume(t *testing.T) {
	st := newFakeStore()
	st.projects["mer"] = domain.ProjectRecord{ID: "mer", Config: domain.ProjectConfig{
		Env: map[string]string{
			"ANTHROPIC_AUTH_TOKEN": "PROJECT_CANARY",
			"OPENAI_API_KEY":       "PROJECT_OPENAI_CANARY",
		},
		Worker: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
	}}
	provider := &recordingProviderResolver{config: providersvc.RuntimeConfig{
		ProviderID: "deepseek", ProviderModelID: "deepseek-model", ProviderDisplayName: "DeepSeek",
		ModelDisplayName: "DeepSeek V4", ModelName: "deepseek-v4-pro",
		APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: "https://api.deepseek.com", APIKey: "SESSION_DEEPSEEK_SECRET",
	}}
	rt := &fakeRuntime{}
	ws := &fakeWorkspace{}
	agent := &recordingAgent{}
	m := New(Deps{Runtime: rt, Agents: singleAgent{agent: agent}, Workspace: ws, Store: st, Providers: provider,
		Messenger: &fakeMessenger{}, Lifecycle: &fakeLCM{store: st}, DataDir: t.TempDir(),
		LookPath: func(string) (string, error) { return "/bin/true", nil }})

	rec, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Prompt: "remember alpha", ProviderID: "deepseek", ProviderModelID: "deepseek-model"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.newCalls != 1 || rec.Metadata.ProviderID != "deepseek" || rec.Metadata.ProviderModelID != "deepseek-model" {
		t.Fatalf("new Provider resolution/persistence = calls:%d metadata:%+v", provider.newCalls, rec.Metadata)
	}
	assertDeepSeekOnlyEnvironment(t, rt.lastCfg.Env)

	// Model the Claude SessionStart hook persisting the native UUID, then a
	// normal stopped session. Restore must use the historical resume resolver.
	rec = st.sessions[rec.ID]
	rec.Metadata.AgentSessionID = "019fc430-1234-7abc-8def-0123456789ab"
	rec.Metadata.RuntimeHandleID = ""
	rec.Metadata.RuntimeLaunchID = ""
	rec.IsTerminated = true
	rec.Activity = domain.Activity{State: domain.ActivityExited, LastActivityAt: time.Now()}
	st.sessions[rec.ID] = rec
	provider.newErr = providersvc.ErrDisabled
	if _, err := m.RestoreWithMode(context.Background(), rec.ID); err != nil {
		t.Fatal(err)
	}
	if provider.resumeCalls != 1 || agent.restoreCalls != 1 {
		t.Fatalf("resume calls: provider=%d agent=%d", provider.resumeCalls, agent.restoreCalls)
	}
	assertDeepSeekOnlyEnvironment(t, rt.lastCfg.Env)

	if _, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindWorker,
		Harness: domain.HarnessClaudeCode, Prompt: "new disabled task", ProviderID: "deepseek", ProviderModelID: "deepseek-model"}); !errors.Is(err, providersvc.ErrDisabled) {
		t.Fatalf("new session using disabled Provider error = %v", err)
	}
}

func assertDeepSeekOnlyEnvironment(t *testing.T, env map[string]string) {
	t.Helper()
	if env["ANTHROPIC_AUTH_TOKEN"] != "SESSION_DEEPSEEK_SECRET" || env["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com" || env["ANTHROPIC_MODEL"] != "deepseek-v4-pro" {
		t.Fatalf("DeepSeek mapped environment = %#v", env)
	}
	if env["OPENAI_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] == "PROJECT_CANARY" {
		t.Fatalf("unselected/project Provider environment leaked = %#v", env)
	}
}
