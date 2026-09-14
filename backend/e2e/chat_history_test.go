//go:build !windows

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Scenarios for the conversation history operations against a real provider.
//
// Rollback is the one worth running for real. Every unit test can prove AO sends
// the right frame and hides the right rows; only a live provider can prove the
// agent actually FORGOT. That is the whole claim, and the way it fails in
// production is subtle: the timeline looks right and the agent answers as though
// the discarded turns are still there.
//
// Titles are here for the other half: `thread/name/set` is a provider call whose
// only visible effect arrives back on a notification, so the round trip through
// the daemon into sessions.display_name cannot be checked any other way.

// rollback discards a turn and everything after it, and reports the count.
func rollback(t *testing.T, d *daemon, session, turnID string) int {
	t.Helper()
	var out struct {
		TurnsDiscarded int `json:"turnsDiscarded"`
	}
	d.mustCall("POST", "/sessions/"+session+"/conversation/turns/"+turnID+"/rollback",
		http.StatusOK, nil, &out)
	return out.TurnsDiscarded
}

// The claim in full: after an undo the agent cannot recall what was discarded, and
// AO's timeline does not show it either.
//
// The secret is the instrument. A word the agent only ever learned inside the
// discarded turn is the one thing that distinguishes "the provider dropped the
// history" from "the provider still has it and AO is merely hiding rows".
func TestChatRollbackMakesTheAgentForget(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "rollback")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session,
		"Remember this secret word: ZEPHYRINE. Reply with exactly: STORED", "rb-secret")
	stored := d.awaitConversation(session, 3*time.Minute, "the secret to be stored",
		func(s snapshot) bool {
			return contains(s.assistantText(), "STORED") && terminal(s.Turns[len(s.Turns)-1].State)
		})
	secretTurn := stored.Turns[len(stored.Turns)-1]

	// Confirm the agent really has it, so a later failure to recall cannot be
	// explained by it never having landed.
	send(t, d, session, "What was the secret word? Reply with just the word.", "rb-check")
	recalled := d.awaitConversation(session, 3*time.Minute, "the agent to recall the secret",
		func(s snapshot) bool { return terminal(s.Turns[len(s.Turns)-1].State) })
	if !contains(strings.ToUpper(recalled.assistantText()), "ZEPHYRINE") {
		t.Fatalf("the agent never learned the secret, so forgetting it proves nothing:\n%s",
			describe(recalled))
	}

	discarded := rollback(t, d, session, secretTurn.ID)
	if discarded < 2 {
		t.Errorf("rollback discarded %d turns, want at least 2 (the secret turn and the recall)",
			discarded)
	}

	after := d.conversation(session)

	// AO's side: the turn rows survive, marked; their prose is gone.
	marked := 0
	for _, tn := range after.Turns {
		if tn.RolledBack {
			marked++
		}
	}
	if marked != discarded {
		t.Errorf("%d turns reported discarded but %d are marked rolled back", discarded, marked)
	}
	if contains(after.assistantText(), "STORED") {
		t.Errorf("a discarded turn's prose is still in the timeline:\n%s", describe(after))
	}
	for _, m := range after.Messages {
		if strings.Contains(m.Text, "ZEPHYRINE") {
			t.Errorf("the discarded user message is still in the timeline: %q", m.Text)
		}
	}

	// The provider's side, which is the part only a live run can show.
	send(t, d, session,
		"What was the secret word I told you? If you do not know it, reply with exactly: NO-SECRET",
		"rb-after")
	asked := d.awaitConversation(session, 3*time.Minute, "the post-rollback answer",
		func(s snapshot) bool { return terminal(s.Turns[len(s.Turns)-1].State) })
	answer := strings.ToUpper(asked.assistantText())
	if strings.Contains(answer, "ZEPHYRINE") {
		t.Errorf("the agent still remembers a turn AO says was rolled back:\n%s", describe(asked))
	}
	if !strings.Contains(answer, "NO-SECRET") {
		t.Errorf("the agent gave no clear answer after the rollback:\n%s", describe(asked))
	}

	// Sequence is immutable, so the surviving rows keep their original positions and
	// the timeline still sorts by one key.
	var previous int64 = -1
	for _, m := range asked.Messages {
		if m.Sequence <= previous {
			t.Fatalf("messages are not in increasing sequence order after a rollback: %d after %d",
				m.Sequence, previous)
		}
		previous = m.Sequence
	}
}

// Rolling back mid-turn must be refused, not raced. Discarding history the agent is
// still writing into would leave AO's rows and the agent's memory describing
// different conversations, with nothing to reconcile them.
func TestChatRollbackIsRefusedWhileATurnIsRunning(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "rollbackbusy")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	first := d.conversation(session).Turns[0]

	send(t, d, session,
		"Sleep for 45 seconds using a shell command, then reply with exactly: SLEPT", "rb-slow")
	d.awaitConversation(session, 90*time.Second, "the slow turn to start running",
		func(s snapshot) bool { return s.Turns[len(s.Turns)-1].State == "running" })

	status, body := d.callExpectingError("POST",
		"/sessions/"+session+"/conversation/turns/"+first.ID+"/rollback", nil)
	if status >= 500 {
		t.Fatalf("a rollback during a turn returned %d (%+v); being mid-turn is not a server fault",
			status, body)
	}
	if status != http.StatusConflict {
		t.Errorf("status = %d, want 409", status)
	}
	if body.Code != "CHAT_TURN_RUNNING" {
		t.Errorf("code = %q, want CHAT_TURN_RUNNING", body.Code)
	}

	// And nothing was discarded on the strength of a refused request.
	for _, tn := range d.conversation(session).Turns {
		if tn.RolledBack {
			t.Errorf("turn %s was marked rolled back by a refused rollback", tn.ID)
		}
	}

	d.mustCall("POST", "/sessions/"+session+"/conversation/interrupt", http.StatusNoContent,
		map[string]any{"queuedTurnIds": []string{}}, nil)
}

// A turn id from nowhere is a 404, and a turn the provider never accepted is a
// conflict. Both are client mistakes, and neither is a server failure.
func TestChatRollbackRefusesTurnsItCannotUndo(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "rollbackbad")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	status, body := d.callExpectingError("POST",
		"/sessions/"+session+"/conversation/turns/turn-that-never-existed/rollback", nil)
	if status != http.StatusNotFound {
		t.Errorf("unknown turn returned %d, want 404 (%+v)", status, body)
	}
	if body.Code != "CHAT_TURN_NOT_FOUND" {
		t.Errorf("code = %q, want CHAT_TURN_NOT_FOUND", body.Code)
	}
}

// The title round trip. AO asks the provider to name the thread; the provider
// confirms on its own notification; the daemon adopts it as the session's label.
// Nothing is written optimistically, so a successful call is not the assertion —
// the label moving is.
func TestChatThreadTitleBecomesTheSessionName(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "title")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	var accepted struct {
		Title string `json:"title"`
	}
	// Deliberately dirty input: a model asked for a title reaches for heading
	// markers, quotes and a full stop, and the daemon normalizes rather than trusts.
	d.mustCall("PUT", "/sessions/"+session+"/conversation/title", http.StatusAccepted,
		map[string]any{"title": `## "Fix OAuth Return URL Loss."`}, &accepted)
	if accepted.Title != "Fix OAuth Return URL Loss" {
		t.Fatalf("accepted title = %q, want the normalized form", accepted.Title)
	}

	// The provider's own notification is what lands it, so this has to be awaited.
	named := d.awaitConversation(session, 60*time.Second, "the provider to report the new name",
		func(s snapshot) bool { return s.Title == "Fix OAuth Return URL Loss" })
	if named.Title != "Fix OAuth Return URL Loss" {
		t.Fatalf("conversation title = %q", named.Title)
	}

	// The point of routing it through display_name: every surface that already shows
	// a session name gets it, with no parallel field to keep in sync.
	var read struct {
		Session struct {
			DisplayName string `json:"displayName"`
		} `json:"session"`
	}
	d.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
	if read.Session.DisplayName != "Fix OAuth Return URL Loss" {
		t.Errorf("session display name = %q, want the provider title",
			read.Session.DisplayName)
	}

	// A name the user chose is never taken away by a later provider title.
	d.mustCall("PATCH", "/sessions/"+session, http.StatusOK,
		map[string]any{"displayName": "Mine"}, nil)
	d.mustCall("PUT", "/sessions/"+session+"/conversation/title", http.StatusAccepted,
		map[string]any{"title": "Something Else Entirely"}, nil)

	// The provider title is recorded even so; only the label is left alone.
	d.awaitConversation(session, 60*time.Second, "the second provider title to be recorded",
		func(s snapshot) bool { return s.Title == "Something Else Entirely" })
	d.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
	if read.Session.DisplayName != "Mine" {
		t.Errorf("session display name = %q, want the user's own name to have survived",
			read.Session.DisplayName)
	}

	// A blank title is refused rather than sent: the provider rejects one outright.
	status, body := d.callExpectingError("PUT", "/sessions/"+session+"/conversation/title",
		map[string]any{"title": "   "})
	if status != http.StatusBadRequest {
		t.Errorf("blank title returned %d, want 400 (%+v)", status, body)
	}
}
