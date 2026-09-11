//go:build !windows

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// These are real daemon -> detached host -> ACP provider -> model boundaries.
// They are separately gated because they use the developer's installed binaries,
// provider configuration, credentials, and model quota.
func TestOpenCodeACPTurnSurvivesDaemonSIGKILL(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENCODE_ACP") != "1" {
		t.Skip("set AO_LIVE_OPENCODE_ACP=1 to run the real OpenCode restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "opencode", "opencode", "OPENCODE-DAEMON-SURVIVED", false)
}

func TestClaudeACPTurnSurvivesDaemonSIGKILL(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_ACP") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_ACP=1 to run the real Claude restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "claude-code", "claude", "CLAUDE-DAEMON-SURVIVED", false)
}

func TestClaudeACPTurnSurvivesGracefulDaemonRestart(t *testing.T) {
	if os.Getenv("AO_LIVE_CLAUDE_ACP") != "1" {
		t.Skip("set AO_LIVE_CLAUDE_ACP=1 to run the real Claude restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "claude-code", "claude", "CLAUDE-GRACEFUL-DAEMON-SURVIVED", true)
}

func TestOpenCodeACPTurnSurvivesGracefulDaemonRestart(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENCODE_ACP") != "1" {
		t.Skip("set AO_LIVE_OPENCODE_ACP=1 to run the real OpenCode restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "opencode", "opencode", "OPENCODE-GRACEFUL-DAEMON-SURVIVED", true)
}

func TestCursorACPTurnSurvivesDaemonSIGKILL(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_ACP") != "1" {
		t.Skip("set AO_LIVE_CURSOR_ACP=1 to run the real Cursor restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "cursor", "cursor-agent", "CURSOR-DAEMON-SURVIVED", false)
}

func TestCursorACPTurnSurvivesGracefulDaemonRestart(t *testing.T) {
	if os.Getenv("AO_LIVE_CURSOR_ACP") != "1" {
		t.Skip("set AO_LIVE_CURSOR_ACP=1 to run the real Cursor restart E2E")
	}
	runACPTurnSurvivesDaemonRestart(t, "cursor", "cursor-agent", "CURSOR-GRACEFUL-DAEMON-SURVIVED", true)
}

func runACPTurnSurvivesDaemonRestart(t *testing.T, harness, binary, token string, graceful bool) {
	t.Helper()
	if _, err := exec.LookPath(binary); err != nil {
		t.Skipf("%s is not on PATH: %v", binary, err)
	}
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, harness+"-acp-restart")
	setPermissions(t, d, project, "bypass-permissions")
	session := spawn(t, d, map[string]any{
		"projectId": project, "kind": "worker", "harness": harness, "mode": "chat",
		"prompt": "Reply with exactly: " + token + "-READY",
	}).Session.ID
	initial := d.awaitConversation(session, 4*time.Minute, "the initial "+harness+" turn", func(s snapshot) bool {
		return len(s.Turns) >= 1 && terminal(s.Turns[0].State)
	})
	if initial.Turns[0].State != "completed" || !contains(initial.assistantText(), token+"-READY") {
		t.Fatalf("%s is installed but its initial credentialed turn failed:\n%s", harness, describe(initial))
	}

	startedMarker := filepath.Join(dataDir, "provider-tool-started")
	command := fmt.Sprintf("touch %q && sleep 12", startedMarker)
	turnID, _ := send(t, d, session,
		"Run the shell command `"+command+"`, wait for it, then reply with exactly: "+token,
		harness+"-long")
	waitForProviderMarker(t, d, session, startedMarker, harness)
	before := d.conversation(session)
	active := before.Turns[len(before.Turns)-1]
	if active.ID != turnID || active.ProviderTurnID == "" || terminal(active.State) {
		t.Fatalf("restart barrier is not inside the accepted turn: %s", describe(before))
	}
	hostBefore := persistentHostPID(t, dataDir, session)
	if graceful {
		d.stop()
	} else {
		d.kill()
	}
	if !processAlive(hostBefore) {
		t.Fatalf("detached ACP host %d died with the daemon", hostBefore)
	}

	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 2*time.Minute)
	finished := restarted.awaitConversation(session, 4*time.Minute, "the adopted "+harness+" prompt", func(s snapshot) bool {
		return len(s.Turns) >= 2 && terminal(s.Turns[len(s.Turns)-1].State)
	})
	last := finished.Turns[len(finished.Turns)-1]
	hostAfter := persistentHostPID(t, dataDir, session)
	t.Logf("%s ACP restart: host_pid=%d->%d turn=%s->%s provider_turn=%s->%s state=%s",
		harness, hostBefore, hostAfter, active.ID, last.ID, active.ProviderTurnID, last.ProviderTurnID, last.State)
	if hostAfter != hostBefore || last.ID != active.ID || last.ProviderTurnID != active.ProviderTurnID || last.State != "completed" ||
		!turnHasAnswer(finished, turnID, token) {
		t.Fatalf("%s ACP turn did not survive daemon replacement:\n%s", harness, describe(finished))
	}
	// Prove this is a usable connection, not just a replayed terminal snapshot.
	followup, _ := send(t, restarted, session, "Reply with exactly: "+token+"-FOLLOWUP", harness+"-followup")
	continued := restarted.awaitConversation(session, 4*time.Minute, "a fresh post-restart turn", func(s snapshot) bool {
		return len(s.Turns) == 3 && terminal(s.Turns[2].State)
	})
	if continued.Turns[2].State != "completed" || !turnHasAnswer(continued, followup, token+"-FOLLOWUP") {
		t.Fatalf("post-restart follow-up failed:\n%s", describe(continued))
	}
	restarted.mustCall("POST", "/sessions/"+session+"/kill", http.StatusOK, nil, nil)
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(hostAfter) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(hostAfter) {
		t.Fatal("explicit session kill left persistent host alive")
	}
	t.Logf("%s post-restart follow-up completed; explicit kill released host %d", harness, hostAfter)
}

func turnHasAnswer(s snapshot, turnID, token string) bool {
	for _, msg := range s.Messages {
		if msg.TurnID == turnID && msg.Role == "assistant" && contains(msg.Text, token) {
			return true
		}
	}
	return false
}

func waitForProviderMarker(t *testing.T, d *daemon, session, marker, harness string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s's shell marker; conversation:\n%s",
		harness, describe(d.conversation(session)))
}
