package droidacp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestConfigureUsesUserDroidACPDaemonAndStandingInstructions(t *testing.T) {
	args, env, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "Follow AO orchestrator rules."})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{
		"exec", "--output-format", "acp-daemon",
		"--append-system-prompt", "Follow AO orchestrator rules.",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if env != nil {
		t.Fatalf("env overrides = %#v, want nil", env)
	}
}

func TestConfigureOmitsEmptySystemPrompt(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{SystemPrompt: "  "})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{"exec", "--output-format", "acp-daemon"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureMapsFullBypassToDroidExecFlag(t *testing.T) {
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{
		Permissions: ports.PermissionModeBypassPermissions,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	want := []string{
		"exec", "--output-format", "acp-daemon", "--skip-permissions-unsafe",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestConfigureReusesDroidRuntimeModelAndAutonomySettings(t *testing.T) {
	dataDir := t.TempDir()
	args, _, err := configure(context.Background(), acpdriver.LaunchConfig{
		SessionID: "worker-1", DataDir: dataDir, Model: "claude-sonnet-5",
		Permissions: ports.PermissionModeAuto,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	settingsPath := filepath.Join(
		dataDir, "agent-runtime", "droid", "ao-droid-worker-1-settings.json")
	want := []string{
		"--settings", settingsPath,
		"exec", "--output-format", "acp-daemon",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings struct {
		Model                  string `json:"model"`
		SessionDefaultSettings struct {
			AutonomyLevel string `json:"autonomyLevel"`
		} `json:"sessionDefaultSettings"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings.Model != "claude-sonnet-5" || settings.SessionDefaultSettings.AutonomyLevel != "medium" {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestConfigureRequiresAODataDirForDroidRuntimeSettings(t *testing.T) {
	_, _, err := configure(context.Background(), acpdriver.LaunchConfig{Model: "claude-sonnet-5"})
	if err == nil {
		t.Fatal("configure succeeded without AO data directory")
	}
}
