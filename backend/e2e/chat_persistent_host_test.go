//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func persistentHostPID(t *testing.T, dataDir, sessionID string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataDir, "chat-hosts", sessionID, "host.json"))
	if err != nil {
		t.Fatalf("read persistent host descriptor: %v", err)
	}
	var d struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &d); err != nil || d.PID <= 0 {
		t.Fatalf("decode persistent host descriptor: pid=%d err=%v", d.PID, err)
	}
	return d.PID
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestChatTurnSurvivesGracefulUpdaterStyleRestart(t *testing.T) {
	requireE2E(t)
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "graceful-midturn")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "Run the shell command `sleep 20`, then reply with exactly: SURVIVED-GRACEFUL", "graceful-long")
	d.awaitConversation(session, 90*time.Second, "the graceful-restart turn to run", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})
	hostBefore := persistentHostPID(t, dataDir, session)
	d.stop()
	if !processAlive(hostBefore) {
		t.Fatalf("detached host %d died with the old daemon", hostBefore)
	}

	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 90*time.Second)
	finished := restarted.awaitConversation(session, 3*time.Minute, "the detached real Codex turn to finish", func(s snapshot) bool {
		return terminal(s.Turns[len(s.Turns)-1].State)
	})
	hostAfter := persistentHostPID(t, dataDir, session)
	last := finished.Turns[len(finished.Turns)-1]
	t.Logf("graceful updater simulation: host_pid=%d->%d turn_state=%s", hostBefore, hostAfter, last.State)
	if hostAfter != hostBefore || last.State != "completed" || !contains(finished.assistantText(), "SURVIVED-GRACEFUL") {
		t.Fatalf("real Codex turn did not survive graceful replacement:\n%s", describe(finished))
	}
}

func TestChatSessionRecoversAfterMachineStyleHostLoss(t *testing.T) {
	requireE2E(t)
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "machine-loss")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "Run the shell command `sleep 20`, then reply with exactly: MUST-NOT-COMPLETE", "machine-loss-long")
	d.awaitConversation(session, 90*time.Second, "the machine-loss turn to run", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})
	hostBefore := persistentHostPID(t, dataDir, session)
	// The detached host is a session leader; killing its process group removes
	// both host and provider, matching the process loss caused by a machine reboot.
	if err := syscall.Kill(-hostBefore, syscall.SIGKILL); err != nil {
		t.Fatalf("kill detached host process group %d: %v", hostBefore, err)
	}
	d.kill()

	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 90*time.Second)
	settled := restarted.awaitConversation(session, 2*time.Minute, "the lost in-flight turn to settle", func(s snapshot) bool {
		return terminal(s.Turns[len(s.Turns)-1].State)
	})
	lost := settled.Turns[len(settled.Turns)-1]
	if lost.State == "completed" || contains(settled.assistantText(), "MUST-NOT-COMPLETE") {
		t.Fatalf("provider loss falsely completed the interrupted turn:\n%s", describe(settled))
	}
	send(t, restarted, session, "Reply with exactly: RECOVERED-AFTER-HOST-LOSS", "machine-loss-after")
	recovered := restarted.awaitConversation(session, 3*time.Minute, "a turn after host loss", func(s snapshot) bool {
		return contains(s.assistantText(), "RECOVERED-AFTER-HOST-LOSS")
	})
	hostAfter := persistentHostPID(t, dataDir, session)
	t.Logf("machine-style host loss: old_host_pid=%d new_host_pid=%d interrupted_state=%s", hostBefore, hostAfter, lost.State)
	if hostAfter == hostBefore || !contains(recovered.assistantText(), "RECOVERED-AFTER-HOST-LOSS") {
		t.Fatalf("session did not recover through native resume:\n%s", describe(recovered))
	}
}
