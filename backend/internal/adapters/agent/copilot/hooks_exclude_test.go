package copilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	testCopilotExcludeHeader  = "# agent-orchestrator Copilot session files"
	testCopilotExcludePattern = "/.github/agents/ao-*.agent.md"
)

func TestInstallAgentProfileUsesOneExcludeForMultipleSessions(t *testing.T) {
	workspace := copilotWorkspaceWithExclude(t, "")
	plugin := &Plugin{resolvedBinary: "copilot"}

	for _, sessionID := range []string{"work-1", "work-2", "work-3"} {
		if err := plugin.InstallAgentProfile(context.Background(), ports.WorkspaceHookConfig{
			SessionID:     sessionID,
			SystemPrompt:  "follow the repository instructions",
			WorkspacePath: workspace,
		}); err != nil {
			t.Fatalf("InstallAgentProfile(%q): %v", sessionID, err)
		}
	}

	want := testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n"
	if got := readCopilotExclude(t, workspace); got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestInstallAgentProfileNormalizesLegacyExcludeBlocks(t *testing.T) {
	existing := "# user's own rules\n/build\n\n" +
		testCopilotExcludeHeader + "\n/.github/agents/ao-work-1.agent.md\n" +
		testCopilotExcludeHeader + "\n/.github/agents/ao-work-2.agent.md\n\n" +
		"# keep this spacing\n/cache/\n"
	workspace := copilotWorkspaceWithExclude(t, existing)

	installCopilotTestProfile(t, workspace, "work-3")

	want := "# user's own rules\n/build\n\n" +
		testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n\n" +
		"# keep this spacing\n/cache/\n"
	if got := readCopilotExclude(t, workspace); got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestInstallAgentProfileCleansLegacyBlocksWhenGlobExists(t *testing.T) {
	existing := testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n" +
		testCopilotExcludeHeader + "\n/.github/agents/ao-stale.agent.md\n"
	workspace := copilotWorkspaceWithExclude(t, existing)

	installCopilotTestProfile(t, workspace, "fresh")

	want := testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n"
	if got := readCopilotExclude(t, workspace); got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestInstallAgentProfilePreservesUnrelatedExcludeRules(t *testing.T) {
	existing := "# user's similarly named rules\n" +
		"/.github/agents/ao-user.agent.md\n" +
		"/.github/agents/ao-user.agent.md.backup\n" +
		"/.github/agents/team-*.agent.md\n\n"
	workspace := copilotWorkspaceWithExclude(t, existing)

	installCopilotTestProfile(t, workspace, "fresh")

	want := existing + testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n"
	if got := readCopilotExclude(t, workspace); got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
}

func TestInstallAgentProfileDoesNotRewriteNormalizedExclude(t *testing.T) {
	existing := "/build\n" + testCopilotExcludeHeader + "\n" + testCopilotExcludePattern + "\n"
	workspace := copilotWorkspaceWithExclude(t, existing)
	excludePath := filepath.Join(workspace, ".git", "info", "exclude")
	wantModTime := time.Unix(123, 0)
	if err := os.Chtimes(excludePath, wantModTime, wantModTime); err != nil {
		t.Fatal(err)
	}

	installCopilotTestProfile(t, workspace, "fresh")

	info, err := os.Stat(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(wantModTime) {
		t.Fatalf("normalized exclude was rewritten: modtime = %s, want %s", info.ModTime(), wantModTime)
	}
	if got := readCopilotExclude(t, workspace); got != existing {
		t.Fatalf("exclude = %q, want %q", got, existing)
	}
}

func copilotWorkspaceWithExclude(t *testing.T, existing string) string {
	t.Helper()
	workspace := t.TempDir()
	infoDir := filepath.Join(workspace, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if existing != "" {
		if err := os.WriteFile(filepath.Join(infoDir, "exclude"), []byte(existing), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return workspace
}

func installCopilotTestProfile(t *testing.T, workspace, sessionID string) {
	t.Helper()
	plugin := &Plugin{resolvedBinary: "copilot"}
	if err := plugin.InstallAgentProfile(context.Background(), ports.WorkspaceHookConfig{
		SessionID:     sessionID,
		SystemPrompt:  "follow the repository instructions",
		WorkspacePath: workspace,
	}); err != nil {
		t.Fatalf("InstallAgentProfile: %v", err)
	}
}

func readCopilotExclude(t *testing.T, workspace string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
