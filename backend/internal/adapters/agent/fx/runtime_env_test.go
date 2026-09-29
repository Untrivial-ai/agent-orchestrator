package fx

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRuntimeLaunchEnvFencesHerdrIdentity(t *testing.T) {
	augmenter, ok := any(New()).(interface {
		AugmentRuntimeLaunchEnv(map[string]string, string, domain.SessionID, string)
	})
	if !ok {
		t.Fatal("fx must augment the environment after AO assigns a runtime generation")
	}
	env := map[string]string{"FX_MODEL": "custom-model"}
	dataDir := t.TempDir()
	augmenter.AugmentRuntimeLaunchEnv(env, dataDir, "mer-1", "launch-1")
	if env["HERDR_SOCKET_PATH"] != filepath.Join(dataDir, "run", "fx-herdr.sock") {
		t.Fatalf("socket path = %q", env["HERDR_SOCKET_PATH"])
	}
	// Versioned, separately encoded components cannot collide on delimiters.
	if env["HERDR_PANE_ID"] != "ao:1:bWVyLTE:bGF1bmNoLTE" {
		t.Fatalf("pane identity = %q", env["HERDR_PANE_ID"])
	}
	augmenter.AugmentRuntimeLaunchEnv(env, dataDir, "mer-1", "launch-2")
	if env["HERDR_PANE_ID"] != "ao:1:bWVyLTE:bGF1bmNoLTI" || env["FX_MODEL"] != "custom-model" {
		t.Fatalf("replacement environment = %#v", env)
	}
}

func TestLaunchPreparationDoesNotRequireHerdrListener(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "fx"}
	env := map[string]string{}
	plugin.AugmentRuntimeLaunchEnv(env, t.TempDir(), "mer-1", "launch-1")
	if _, err := os.Stat(env["HERDR_SOCKET_PATH"]); !os.IsNotExist(err) {
		t.Fatalf("test requires unavailable listener: %v", err)
	}
	argv, err := plugin.GetLaunchCommand(context.Background(), ports.LaunchConfig{})
	if err != nil || len(argv) != 1 || argv[0] != "fx" {
		t.Fatalf("unavailable listener prevented launch: %v %v", argv, err)
	}
	strategy, err := plugin.GetPromptDeliveryStrategy(context.Background(), ports.LaunchConfig{})
	if err != nil || strategy != ports.PromptDeliveryAfterStart {
		t.Fatalf("unavailable listener changed prompt delivery: %v %v", strategy, err)
	}
}
