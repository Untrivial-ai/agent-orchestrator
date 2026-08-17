package omp

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestManifest(t *testing.T) {
	m := (&Plugin{}).Manifest()
	if m.ID != "omp" {
		t.Fatalf("ID = %q, want omp", m.ID)
	}
	if m.Name != "OMP" {
		t.Fatalf("Name = %q, want OMP", m.Name)
	}
	hasAgent := false
	for _, c := range m.Capabilities {
		if c == adapters.CapabilityAgent {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("missing CapabilityAgent")
	}
}

func TestGetConfigSpecReportsModelField(t *testing.T) {
	spec, err := (&Plugin{}).GetConfigSpec(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []ports.ConfigField{{
		Key:         "model",
		Type:        ports.ConfigFieldString,
		Description: "Model override passed to `omp --model`.",
	}}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("config fields\nwant: %#v\n got: %#v", want, spec.Fields)
	}
}

func TestGetPromptDeliveryStrategyIsInCommand(t *testing.T) {
	got, err := (&Plugin{}).GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != ports.PromptDeliveryInCommand {
		t.Fatalf("strategy = %q, want %q", got, ports.PromptDeliveryInCommand)
	}
}

func TestGetLaunchCommandStartsInteractiveTUIWithPrompt(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		Prompt: "add a health check",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "add a health check"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandAppendsSystemPromptAndModel(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPrompt: "follow repo rules",
		Config:       ports.AgentConfig{Model: "  anthropic/claude-sonnet-4  "},
		Prompt:       "implement it",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "--append-system-prompt", "follow repo rules", "--model", "anthropic/claude-sonnet-4", "implement it"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetLaunchCommandReadsSystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "system.md")
	if err := os.WriteFile(file, []byte("file prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &Plugin{resolvedBinary: "omp"}
	cmd, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SystemPromptFile: file,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"omp", "--append-system-prompt", "file prompt"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandUsesNativeSessionID(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{
		SystemPrompt: "restore rules",
		Config:       ports.AgentConfig{Model: "openai/gpt-5-codex"},
		Session: ports.SessionRef{
			Metadata: map[string]string{ports.MetadataKeyAgentSessionID: "019e950e-52e0-7411-961b-d380ca7e610f"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok=false, want true")
	}
	want := []string{"omp", "--append-system-prompt", "restore rules", "--model", "openai/gpt-5-codex", "--resume", "019e950e-52e0-7411-961b-d380ca7e610f"}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %#v, want %#v", cmd, want)
	}
}

func TestGetRestoreCommandWithoutNativeSessionIDReturnsNotOK(t *testing.T) {
	p := &Plugin{resolvedBinary: "omp"}
	cmd, ok, err := p.GetRestoreCommand(context.Background(), ports.RestoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if ok || cmd != nil {
		t.Fatalf("cmd=%#v ok=%v, want nil false", cmd, ok)
	}
}
