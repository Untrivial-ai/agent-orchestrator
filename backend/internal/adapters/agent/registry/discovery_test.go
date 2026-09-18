package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	chatregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/agentbinary"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A newly registered harness must participate in the same executable selection
// as readiness and Chat, including its internal launch helper.
func TestEveryHarnessUsesSharedBinaryDiscovery(t *testing.T) {
	for _, item := range Harnessed() {
		t.Run(string(item.Harness), func(t *testing.T) {
			provider, ok := item.Agent.(ports.AgentBinaryDiscoveryProvider)
			if !ok {
				t.Fatal("missing binary discovery metadata and injection")
			}
			spec := provider.BinaryDiscoverySpec()
			if spec.Lookup == nil || spec.Presence == nil || spec.Normalize == nil {
				t.Fatal("incomplete raw lookup metadata")
			}
			resolver := &selectedBinary{harness: item.Harness}
			provider.SetBinaryDiscovery(resolver)
			cmd, err := item.Agent.GetLaunchCommand(context.Background(), ports.LaunchConfig{DataDir: t.TempDir(), WorkspacePath: t.TempDir(), SessionID: "discovery-test"})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(cmd, "/selected/agent") {
				t.Fatalf("launch ignored shared selection: %v", cmd)
			}
			if resolver.calls == 0 {
				t.Fatal("launch bypassed shared resolver")
			}
		})
	}
}

type selectedBinary struct {
	harness domain.AgentHarness
	calls   int
}

func (s *selectedBinary) Resolve(_ context.Context, h domain.AgentHarness, _ ports.BinaryResolvePurpose) (ports.AgentBinaryResolution, error) {
	if h != s.harness {
		panic("wrong harness")
	}
	s.calls++
	return ports.AgentBinaryResolution{Executable: "/selected/agent"}, nil
}
func (*selectedBinary) Invalidate(domain.AgentHarness) {}

func TestShellSelectionSharedByTUIRestoreAndChat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fake CLI; native Windows probe has separate coverage")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	marker := filepath.Join(dir, "invoked")
	script := "#!/bin/sh\nprintf invoked >> '" + marker + "'\nprintf 'codex-cli 0.150.0\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	before := os.Getenv("PATH")
	missing := func(context.Context) (string, error) { return "", ports.ErrAgentBinaryNotFound }
	c := agentbinary.New(agentbinary.Config{Specs: map[domain.AgentHarness]ports.AgentBinarySpec{domain.HarnessCodex: {Names: []string{"codex"}, Lookup: missing, Presence: missing}}, Probe: fixtureProbe{binary: binary}})
	defer c.Close()
	worker := codex.New()
	worker.SetBinaryDiscovery(c)
	argv, err := worker.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil || len(argv) == 0 || argv[0] != binary {
		t.Fatalf("TUI %v %v", argv, err)
	}
	restored, ok, err := worker.GetRestoreCommand(context.Background(), ports.RestoreConfig{Session: ports.SessionRef{Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "fixture-native-id"}}})
	if err != nil || !ok || len(restored) == 0 || restored[0] != binary {
		t.Fatalf("restore %v %v %v", restored, ok, err)
	}
	driver, err := chatregistry.Build(nil, c).Driver(domain.HarnessCodex)
	if err != nil {
		t.Fatal(err)
	}
	// The fake executable deliberately speaks no app-server protocol. A
	// protocol failure confirms selection without pretending it is compatible.
	if _, err := driver.Probe(context.Background()); !errors.Is(err, ports.ErrChatDriverIncompatible) {
		t.Fatalf("fake protocol result: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Chat did not execute shared binary", err)
	}
	if os.Getenv("PATH") != before {
		t.Fatal("daemon PATH changed")
	}
}

type fixtureProbe struct{ binary string }

func (p fixtureProbe) ProbeAgentShell(context.Context, []string) (ports.AgentShellSnapshot, error) {
	return ports.AgentShellSnapshot{Paths: map[string]string{"codex": p.binary}}, nil
}
