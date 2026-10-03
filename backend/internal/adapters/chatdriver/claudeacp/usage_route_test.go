package claudeacp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

// Break caught: Chat launches install no AO hooks, so the launch itself is the
// only place the billing route is observable. It must see what the launched
// Claude Code will: daemon env, then settings files, then the session env.
func TestClaudeLaunchUsageRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, key := range []string{
		"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	} {
		t.Setenv(key, "")
	}
	ctx := context.Background()
	workspace := t.TempDir()

	if got := claudeLaunchUsageRoute(ctx, workspace, nil); got != "anthropic" {
		t.Fatalf("default route = %q, want anthropic", got)
	}
	if got := claudeLaunchUsageRoute(ctx, workspace, map[string]string{
		"ANTHROPIC_BASE_URL": "https://gateway.example",
	}); got != pricing.UnidentifiedBillingRoute {
		t.Fatalf("session gateway route = %q, want unidentified", got)
	}

	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := claudeLaunchUsageRoute(ctx, workspace, nil); got != "bedrock" {
		t.Fatalf("settings route = %q, want bedrock", got)
	}
}
