package kiroacp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kiro"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
)

func TestConfigureSelectsWorkspaceCustomAgent(t *testing.T) {
	workspace := t.TempDir()
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{
		WorkspacePath: workspace,
		SystemPrompt:  "Follow AO rules.",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"acp", "--agent", kiro.AgentName}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env = %#v, want nil", env)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".kiro", "agents", "ao.json"))
	if err != nil {
		t.Fatalf("read prepared agent: %v", err)
	}
	var agent struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(data, &agent); err != nil {
		t.Fatalf("decode prepared agent: %v", err)
	}
	if agent.Name != kiro.AgentName || agent.Prompt != "Follow AO rules." {
		t.Fatalf("prepared agent = %#v", agent)
	}
}
