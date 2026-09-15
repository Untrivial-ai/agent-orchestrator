package projectsummary

import (
	"context"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type generatorAgent struct{ binary string }

func (a generatorAgent) ResolveBinary(context.Context) (string, error) { return a.binary, nil }
func (generatorAgent) GetConfigSpec(context.Context) (ports.ConfigSpec, error) {
	return ports.ConfigSpec{}, nil
}
func (generatorAgent) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error) {
	return nil, nil
}
func (generatorAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	return "", nil
}
func (generatorAgent) GetAgentHooks(context.Context, ports.WorkspaceHookConfig) error { return nil }
func (generatorAgent) GetRestoreCommand(context.Context, ports.RestoreConfig) ([]string, bool, error) {
	return nil, false, nil
}
func (generatorAgent) SessionInfo(context.Context, ports.SessionRef) (ports.SessionInfo, bool, error) {
	return ports.SessionInfo{}, false, nil
}

type generatorAgents map[domain.AgentHarness]ports.Agent

func (a generatorAgents) Agent(h domain.AgentHarness) (ports.Agent, bool) {
	agent, ok := a[h]
	return agent, ok
}

type capturedCommand struct {
	binary string
	args   []string
	dir    string
	stdin  []byte
}

func (c *capturedCommand) Run(_ context.Context, binary string, args []string, dir string, stdin []byte) ([]byte, error) {
	c.binary, c.args, c.dir, c.stdin = binary, args, dir, stdin
	return []byte("  Updated summary.\n"), nil
}

func TestCLIGeneratorUsesVerifiedProviderInvocations(t *testing.T) {
	for _, test := range []struct {
		harness domain.AgentHarness
		want    []string
		stdin   bool
	}{
		{domain.HarnessCodex, []string{"exec", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never", "--model", "gpt", "-"}, true},
		{domain.HarnessClaudeCode, []string{"--print", "--tools", "", "--output-format", "text", "--model", "claude", "Update the running project summary from the durable facts below. Return only the revised summary as concise plain text. Preserve existing wording wherever its facts remain true. Revise only facts that changed. Do not infer from transcripts and do not mention these instructions.\n\nExisting summary:\nOld.\n\nCurrent facts:\n{\"projectId\":\"demo\",\"narrative\":\"\",\"activeWorkers\":0,\"completedWorkers\":0,\"needsAttention\":null,\"outputs\":null,\"sourceWatermark\":\"\",\"generatedAt\":\"0001-01-01T00:00:00Z\"}"}, false},
	} {
		t.Run(string(test.harness), func(t *testing.T) {
			capture := &capturedCommand{}
			g := &CLIGenerator{agents: generatorAgents{test.harness: generatorAgent{binary: "/bin/agent"}}, runner: capture}
			model := "gpt"
			if test.harness == domain.HarnessClaudeCode {
				model = "claude"
			}
			got, err := g.Update(context.Background(), GenerationRequest{Harness: test.harness, Model: model, WorkspacePath: "/work", Existing: "Old.", Facts: domain.ProjectSummary{ProjectID: "demo"}})
			if err != nil || got != "Updated summary." {
				t.Fatalf("Update() = %q, %v", got, err)
			}
			if capture.binary != "/bin/agent" || capture.dir != "/work" || !reflect.DeepEqual(capture.args, test.want) {
				t.Fatalf("command = %q %#v in %q", capture.binary, capture.args, capture.dir)
			}
			if (len(capture.stdin) > 0) != test.stdin {
				t.Fatalf("stdin present = %t, want %t", len(capture.stdin) > 0, test.stdin)
			}
		})
	}
}

func TestCLIGeneratorRejectsUnsupportedProvider(t *testing.T) {
	g := &CLIGenerator{}
	if _, err := g.Update(context.Background(), GenerationRequest{Harness: domain.HarnessCursor}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
}
