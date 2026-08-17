package sessionmanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudeagent "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestInterfaceTransitionClaudeStaleIdleEmptyComposerCompletes(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)

	inputAt := time.Now()
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 48) + "\x1b[39m"
	configureClaudeTransitionRegression(t, manager, store, runtime,
		rule+"\n\x1b[39m❯\u00a0\x1b[7m \x1b[0m\n"+rule+"\n⏵⏵ bypass permissions on",
		inputAt, inputAt.Add(-time.Second))

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionCompleted {
		t.Fatalf("ordinary idle Claude switch = phase %s, code %s, detail %s; want completed",
			settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
}

func TestInterfaceTransitionClaudeNavigationMenuReportsPendingDecision(t *testing.T) {
	manager, store, runtime, _, _ := newTransitionManager(t, domain.SessionModeTUI)
	useFastInterfaceTransitionTimings(manager)
	inputAt := time.Now()
	rule := strings.Repeat("─", 48)
	output := "❯ /permissions\n" + rule + "\n" +
		"  Permissions  Recently denied   Allow   Ask   Deny   Workspace\n\n" +
		"  Claude Code won't ask before using allowed tools.\n" +
		"  ╭───────────────────────────────────────────────╮\n" +
		"  │ ⌕ Search…                                     │\n" +
		"  ╰───────────────────────────────────────────────╯\n\n" +
		"    1. Add a new rule…\n\n" +
		"  ←/→ to switch · ↓ to select · Esc to cancel"
	configureClaudeTransitionRegression(t, manager, store, runtime, output, inputAt, inputAt)

	transition, err := manager.StartInterfaceTransition(context.Background(), "session-1",
		domain.SessionModeChat, domain.SessionInterfaceTransitionDrain)
	if err != nil {
		t.Fatal(err)
	}
	settled := awaitTransition(t, store, transition.ID)
	if settled.Phase != domain.SessionInterfaceTransitionFailed || settled.ErrorCode != "DRAIN_DECISION_PENDING" {
		t.Fatalf("Claude navigation menu switch = phase %s, code %s, detail %s; want failed DRAIN_DECISION_PENDING",
			settled.Phase, settled.ErrorCode, settled.ErrorDetail)
	}
	if runtime.destroyed != 0 {
		t.Fatalf("source runtime destroyed %d times with a pending Claude decision", runtime.destroyed)
	}
}

func configureClaudeTransitionRegression(
	t *testing.T,
	manager *Manager,
	store *transitionStore,
	runtime *transitionRuntime,
	output string,
	inputAt time.Time,
	idleAt time.Time,
) {
	t.Helper()
	manager.agents = singleAgent{agent: claudeagent.New()}
	const nativeID = "019fc430-1234-7abc-8def-0123456789ab"

	configDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	transcriptDir := filepath.Join(configDir, "projects", "-tmp-worktree")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, nativeID+".jsonl"), []byte("{\"type\":\"user\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := store.sessions["session-1"]
	rec.Metadata.AgentSessionID = nativeID
	rec.Activity = domain.Activity{State: domain.ActivityIdle, LastActivityAt: idleAt}
	store.sessions["session-1"] = rec
	runtime.aliveByHandle = map[string]bool{"runtime-1": true}
	runtime.outputs = []string{output}
	manager.SetTerminalInputGate(&transitionInputGate{
		acquired: make(chan string, 1), released: make(chan string, 1), lastInputAt: inputAt,
	})
}
