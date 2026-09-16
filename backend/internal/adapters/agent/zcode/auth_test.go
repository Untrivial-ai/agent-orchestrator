package zcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestAuthStatusAuthorizedWithConfiguredProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeConfig(t, filepath.Join(home, ".zcode", "cli", "config.json"), `{
		"provider": {
			"builtin:zai-coding-plan": {
				"kind": "anthropic",
				"options": {"apiKey": "redacted-not-read"}
			}
		}
	}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", status)
	}
}

func TestAuthStatusUnknownWithoutConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown", status)
	}
}

func TestAuthStatusUnknownWithEmptyProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeZcodeConfig(t, filepath.Join(home, ".zcode", "cli", "config.json"), `{
		"provider": {
			"builtin:zai-coding-plan": {
				"kind": "anthropic",
				"options": {}
			}
		},
		"model": {"main": "builtin:zai-coding-plan/GLM-5.3-Flash"}
	}`)
	plugin := &Plugin{resolvedBinary: "zcode"}
	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want unknown (model config is not auth)", status)
	}
}

func writeZcodeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)), 0o600); err != nil {
		t.Fatal(err)
	}
}
