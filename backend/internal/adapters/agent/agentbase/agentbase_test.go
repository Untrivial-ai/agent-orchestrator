package agentbase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestModelConfigSpecHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ModelConfigSpec(ctx, "model"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModelConfigSpec error = %v, want context canceled", err)
	}
}

func TestModelConfigSpecAndFlag(t *testing.T) {
	spec, err := ModelConfigSpec(context.Background(), "Model override.")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Fields) != 1 || spec.Fields[0].Key != "model" || spec.Fields[0].Description != "Model override." {
		t.Fatalf("spec = %#v", spec)
	}

	cmd := []string{"agent"}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  provider/model  "}, "--model")
	if want := []string{"agent", "--model", "provider/model"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %q, want %q", cmd, want)
	}
	AppendModelFlag(&cmd, ports.AgentConfig{Model: "  "}, "--model")
	if len(cmd) != 3 {
		t.Fatalf("blank model changed cmd: %q", cmd)
	}
}

func TestModelEffortConfigSpecHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ModelEffortConfigSpec(ctx, "model", "effort"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ModelEffortConfigSpec error = %v, want context canceled", err)
	}
}

func TestModelEffortConfigSpecAndFlag(t *testing.T) {
	spec, err := ModelEffortConfigSpec(context.Background(), "Model override.", "Effort override.")
	if err != nil {
		t.Fatal(err)
	}
	want := []ports.ConfigField{
		{Key: "model", Type: ports.ConfigFieldString, Description: "Model override."},
		{Key: "effort", Type: ports.ConfigFieldString, Description: "Effort override."},
	}
	if !reflect.DeepEqual(spec.Fields, want) {
		t.Fatalf("spec fields = %#v, want %#v", spec.Fields, want)
	}

	cmd := []string{"agent"}
	AppendEffortFlag(&cmd, ports.AgentConfig{Effort: "  high  "}, "--effort")
	if want := []string{"agent", "--effort", "high"}; !reflect.DeepEqual(cmd, want) {
		t.Fatalf("cmd = %q, want %q", cmd, want)
	}
	AppendEffortFlag(&cmd, ports.AgentConfig{}, "--effort")
	if len(cmd) != 3 {
		t.Fatalf("blank effort changed cmd: %q", cmd)
	}
}
