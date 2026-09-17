package junieacp

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/junie"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"reflect"
	"testing"
)

type builder struct{}

func (builder) Prepare(context.Context, junie.RuntimeFileRequest) (junie.RuntimeFiles, error) {
	return junie.RuntimeFiles{ConfigPath: "config", GuidelinesPath: "guidelines"}, nil
}
func TestConfigureUsesIsolatedRuntimeFiles(t *testing.T) {
	args, env, err := configure(builder{})(context.Background(), acpdriver.LaunchConfig{DataDir: "data", SessionID: "session", SystemPrompt: "guidelines"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--acp", "true", "--skip-update-check", "--config-location", "config", "--guidelines-filename", "guidelines"}
	if !reflect.DeepEqual(args, want) || env != nil {
		t.Fatalf("args=%q env=%v", args, env)
	}
}
