package unrealagent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAdapterIsBuiltInAndChatOnly(t *testing.T) {
	plugin := &Plugin{executable: func() (string, error) { return "/Applications/AO.app/ao", nil }}
	if got, err := plugin.ResolveBinary(context.Background()); err != nil || got != "/Applications/AO.app/ao" {
		t.Fatalf("ResolveBinary() = (%q, %v)", got, err)
	}
	if _, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{}); err == nil {
		t.Fatal("terminal launch unexpectedly succeeded")
	}
	manifest := plugin.Manifest()
	if manifest.ID != adapterID || manifest.Name != "Unreal Agent" || !reflect.DeepEqual(manifest.Capabilities, []adapters.Capability{adapters.CapabilityAgent}) {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestResolveBinaryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().ResolveBinary(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveBinary error = %v", err)
	}
}
