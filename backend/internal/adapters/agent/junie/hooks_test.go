package junie

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestGetAgentHooksPreparesRuntimeFilesFromWorkspaceConfig(t *testing.T) {
	builder := &recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}}
	plugin := newTestPlugin(builder)
	cfg := ports.WorkspaceHookConfig{
		DataDir: "/ao-data", SessionID: "ao-session", SystemPrompt: "instructions", SystemPromptFile: "/ao/system.md",
	}
	if err := plugin.GetAgentHooks(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	want := []RuntimeFileRequest{{
		DataDir: "/ao-data", SessionID: "ao-session", SystemPrompt: "instructions", SystemPromptFile: "/ao/system.md",
	}}
	if !reflect.DeepEqual(builder.requests, want) {
		t.Fatalf("Prepare requests = %#v, want %#v", builder.requests, want)
	}
}

func TestGetAgentHooksPropagatesPreparationError(t *testing.T) {
	wantErr := errors.New("prepare failed")
	plugin := newTestPlugin(&recordingRuntimeFileBuilder{err: wantErr})
	err := plugin.GetAgentHooks(context.Background(), ports.WorkspaceHookConfig{DataDir: "/ao-data", SessionID: "ao-session"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetAgentHooks() error = %v, want %v", err, wantErr)
	}
}

func TestGetAgentHooksHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plugin := newTestPlugin(&recordingRuntimeFileBuilder{files: RuntimeFiles{ConfigPath: "/runtime/config.json"}})
	if err := plugin.GetAgentHooks(ctx, ports.WorkspaceHookConfig{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAgentHooks() error = %v, want context canceled", err)
	}
}

func TestGetAgentHooksRejectsMissingRuntimeIdentity(t *testing.T) {
	for _, cfg := range []ports.WorkspaceHookConfig{
		{DataDir: t.TempDir()},
		{SessionID: "ao-session"},
	} {
		plugin := &Plugin{resolvedBinary: "junie", runtimeFiles: NewRuntimeFileBuilder()}
		if err := plugin.GetAgentHooks(context.Background(), cfg); err == nil {
			t.Fatalf("GetAgentHooks(%#v) succeeded", cfg)
		}
	}
}
