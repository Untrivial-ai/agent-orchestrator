//go:build !windows

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Scenarios for "the session survives the things that actually happen".
//
// Daemons get restarted, laptops sleep, agents crash. The promise chat mode makes
// is that a restart is not a data-loss event: the conversation is the provider's
// and AO reattaches to it, rather than replaying a transcript at a fresh agent and
// hoping it looks the same.

func TestChatSurvivesADaemonRestartWithNativeContext(t *testing.T) {
	requireE2E(t)
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "restart")

	session := chatSession(t, d, project,
		"Remember this number for later: 4271. Reply with exactly: STORED")

	before := d.conversation(session)
	if before.ConversationID == "" {
		t.Fatal("session has no conversation to restore")
	}
	itemsBefore := len(before.Messages) + len(before.Activities)

	// A clean shutdown, the way quitting the app behaves.
	d.stop()
	restarted := startDaemon(t, dataDir)

	// No explicit restore call: bringing a live session back is the boot pass's
	// job, and a user who reopens the app does not press anything. Waiting on the
	// controller is exactly what the renderer does.
	after := restarted.awaitLiveController(session, 90*time.Second)

	// Same conversation, not a new one: a fresh conversation id would mean the
	// history was abandoned and the agent restarted blind.
	if after.ConversationID != before.ConversationID {
		t.Fatalf("conversation id changed across the restart: %s -> %s",
			before.ConversationID, after.ConversationID)
	}
	if got := len(after.Messages) + len(after.Activities); got < itemsBefore {
		t.Errorf("timeline lost items across the restart: %d -> %d", itemsBefore, got)
	}

	// The real claim: the provider still holds the model context, so the agent can
	// answer from the earlier turn without AO replaying anything. If AO were
	// reconstructing context from its own rows, this is where it would show.
	send(t, restarted, session, "What number did I ask you to remember? Reply with just the number.", "recall")
	recalled := restarted.awaitConversation(session, 3*time.Minute, "the recall answer",
		func(s snapshot) bool { return terminal(s.Turns[len(s.Turns)-1].State) })

	if !contains(recalled.assistantText(), "4271") {
		t.Fatalf("the agent lost its context across the restart:\n%s", describe(recalled))
	}
}

// The detached Codex host is outside the daemon's process group. Even SIGKILL of
// the daemon must leave the provider turn running for the replacement to adopt.
func TestChatRestartMidTurnKeepsCodexRunning(t *testing.T) {
	requireE2E(t)
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "midturn")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "Run the shell command `sleep 20`, then reply with exactly: SURVIVED-SIGKILL", "long")
	d.awaitConversation(session, 90*time.Second, "the long turn to start running", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})
	hostBefore := persistentHostPID(t, dataDir, session)

	d.kill()
	if !processAlive(hostBefore) {
		t.Fatalf("detached host %d died with the killed daemon", hostBefore)
	}
	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 90*time.Second)
	snap := restarted.awaitConversation(session, 3*time.Minute, "the surviving turn to finish",
		func(s snapshot) bool { return terminal(s.Turns[len(s.Turns)-1].State) })

	last := snap.Turns[len(snap.Turns)-1]
	hostAfter := persistentHostPID(t, dataDir, session)
	t.Logf("abrupt daemon simulation: host_pid=%d->%d turn_state=%s", hostBefore, hostAfter, last.State)
	if hostAfter != hostBefore || last.State != "completed" || !contains(snap.assistantText(), "SURVIVED-SIGKILL") {
		t.Fatalf("real Codex turn did not survive abrupt daemon replacement:\n%s", describe(snap))
	}
}

// A message the user typed and AO accepted must not vanish because the daemon
// died before sending it. Whatever the resolution — delivered later or settled as
// undelivered — it has to be visible and accounted for, never silently dropped.
func TestChatQueuedMessageIsAccountedForAcrossARestart(t *testing.T) {
	requireE2E(t)
	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "queuecrash")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "Run the shell command `sleep 20`, then reply with exactly: FIRST-SURVIVED", "qc-slow")
	d.awaitConversation(session, 90*time.Second, "the slow turn to start running", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})
	_, state := send(t, d, session, "Reply with exactly: QUEUED-ACROSS-RESTART", "qc-queued")
	if state != "queued" {
		t.Fatalf("mid-turn send reported %q, want queued", state)
	}

	d.kill()
	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 90*time.Second)

	snap := restarted.awaitConversation(session, 3*time.Minute, "the queued message to be delivered",
		func(s snapshot) bool {
			return terminal(s.userTurnStates()["Reply with exactly: QUEUED-ACROSS-RESTART"]) &&
				contains(s.assistantText(), "QUEUED-ACROSS-RESTART")
		})

	// The message must still be in the timeline with a state that explains itself.
	// It must not be sitting at "queued" behind a controller that no longer exists.
	state = snap.userTurnStates()["Reply with exactly: QUEUED-ACROSS-RESTART"]
	if state == "" {
		t.Fatalf("the queued message disappeared across the restart:\n%s", describe(snap))
	}
	if state != "completed" || !contains(snap.assistantText(), "QUEUED-ACROSS-RESTART") {
		t.Errorf("queued message was not completed after the surviving turn: state=%q\n%s", state, describe(snap))
	}
	t.Logf("queued-across-restart resolved as %q", state)
}

// Killing a chat session tears down its controller and its worktree, and the
// session stops claiming to be live. A chat session has no runtime handle, so this
// is the path that would silently skip teardown if mode were not accounted for.
func TestChatKillTerminatesAndReleasesTheController(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "kill")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	d.mustCall("POST", "/sessions/"+session+"/kill", http.StatusOK, nil, nil)

	deadline := time.Now().Add(60 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		var read struct {
			Session struct {
				Status       string `json:"status"`
				IsTerminated bool   `json:"isTerminated"`
			} `json:"session"`
		}
		d.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
		status = read.Session.Status
		if read.Session.IsTerminated {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if status != "terminated" {
		t.Fatalf("killed chat session status = %q, want terminated", status)
	}

	// A terminated session cannot receive anything. Accepting the send would record
	// a message that nothing will ever deliver.
	code, body := d.callExpectingError("POST", "/sessions/"+session+"/conversation/messages",
		map[string]any{"text": "are you there?", "clientMessageId": "after-kill"})
	if code < 400 {
		t.Errorf("a terminated chat session accepted a message (status %d)", code)
	} else if body.Code == "" {
		t.Errorf("refusal carried no error code: %+v", body)
	}
}
