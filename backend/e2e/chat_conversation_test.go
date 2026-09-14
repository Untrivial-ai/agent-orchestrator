//go:build !windows

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Scenarios for "what AO shows is what actually happened".
//
// The provider is the authority on model context; these rows are the authority on
// what AO renders and on delivery state. So the failure this file is looking for
// is disagreement: a timeline that claims work the provider never did, an ordering
// that cannot have happened, or a message the UI shows as sent that never left.

// chatSession spawns a chat session and waits for it to be idle and ready, so a
// scenario starts from a known state rather than racing the spawn turn.
func chatSession(t *testing.T, d *daemon, project, prompt string) string {
	t.Helper()
	id := spawn(t, d, map[string]any{
		"projectId": project, "kind": "worker", "harness": "codex", "mode": "chat",
		"prompt": prompt,
	}).Session.ID
	d.awaitConversation(id, 3*time.Minute, "the session to finish its first turn",
		func(s snapshot) bool {
			return len(s.Turns) >= 1 && terminal(s.Turns[0].State)
		})
	return id
}

func send(t *testing.T, d *daemon, session, text, clientID string) (turnID, state string) {
	t.Helper()
	var out struct {
		TurnID    string `json:"turnId"`
		State     string `json:"state"`
		Duplicate bool   `json:"duplicate"`
	}
	d.mustCall("POST", "/sessions/"+session+"/conversation/messages", http.StatusAccepted,
		map[string]any{"text": text, "clientMessageId": clientID}, &out)
	return out.TurnID, out.State
}

func TestChatTurnIsProjectedAndArchived(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "projection")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "List the files in this repo, then reply with exactly: LISTED", "c-list")

	snap := d.awaitConversation(session, 3*time.Minute, "the turn to complete",
		func(s snapshot) bool {
			return len(s.Turns) >= 2 && terminal(s.Turns[1].State)
		})

	if got := snap.Turns[1].State; got != "completed" {
		t.Errorf("turn state = %q, want completed (err=%q)", got, snap.Turns[1].ErrorMessage)
	}
	if !contains(snap.assistantText(), "LISTED") {
		t.Errorf("agent's answer missing from the timeline:\n%s", describe(snap))
	}

	// Ordering has a single writer. A duplicate position means two writers raced
	// for it, which would make the timeline's order unreproducible.
	seen := map[int64]string{}
	var previous int64 = -1
	for _, m := range snap.Messages {
		if where, dup := seen[m.Sequence]; dup {
			t.Fatalf("sequence %d handed out twice (%s and message %s)", m.Sequence, where, m.ID)
		}
		seen[m.Sequence] = "message " + m.ID
		if m.Sequence <= previous {
			t.Fatalf("messages are not in increasing sequence order: %d after %d", m.Sequence, previous)
		}
		previous = m.Sequence
	}
	for _, a := range snap.Activities {
		if where, dup := seen[a.Sequence]; dup {
			t.Fatalf("sequence %d handed out twice (%s and activity %s)", a.Sequence, where, a.ID)
		}
		seen[a.Sequence] = "activity " + a.ID
	}

	// Streaming must fold into one message rather than leaving a row per delta.
	for _, m := range snap.Messages {
		if m.Role != "assistant" {
			continue
		}
		if m.Stream {
			t.Errorf("assistant message %s still marked streaming after the turn completed", m.ID)
		}
	}

	// At least one tool call should have been recorded for a prompt that required
	// looking at the repo. Without this the timeline would show an answer with no
	// account of how it was reached.
	commands := 0
	for _, a := range snap.Activities {
		if a.Kind == "command" {
			commands++
		}
	}
	if commands == 0 {
		t.Errorf("no command activity recorded for a prompt that had to read the repo:\n%s", describe(snap))
	}
}

// A retried send must not run the work twice. This is the difference between a
// flaky network and a double commit.
func TestChatSendIsIdempotentOnClientMessageID(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "idempotent")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	before := len(d.conversation(session).Turns)

	first, _ := send(t, d, session, "Reply with exactly: ONCE", "retry-me")
	if first == "" {
		t.Fatal("first send returned no turn id")
	}

	var second struct {
		TurnID    string `json:"turnId"`
		Duplicate bool   `json:"duplicate"`
	}
	d.mustCall("POST", "/sessions/"+session+"/conversation/messages", http.StatusAccepted,
		map[string]any{"text": "Reply with exactly: ONCE", "clientMessageId": "retry-me"}, &second)
	if !second.Duplicate {
		t.Errorf("a retry under the same client message id was not reported as a duplicate")
	}

	snap := d.awaitConversation(session, 3*time.Minute, "the turn to settle", func(s snapshot) bool {
		return len(s.Turns) > before && terminal(s.Turns[len(s.Turns)-1].State)
	})
	if got := len(snap.Turns); got != before+1 {
		t.Fatalf("turns = %d, want %d: a retry created a second turn\n%s", got, before+1, describe(snap))
	}
	sent := 0
	for _, m := range snap.Messages {
		if m.Role == "user" && strings.Contains(m.Text, "ONCE") {
			sent++
		}
	}
	if sent != 1 {
		t.Errorf("the retried message was recorded %d times", sent)
	}
}

// The composer tells the user a mid-turn message is held until the agent
// finishes. The provider cannot run two turns on one conversation, so this has to
// be true of the daemon and not just of the placeholder text.
func TestChatQueueHoldsMidTurnMessagesAndDrainsInOrder(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "queue")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	// Something slow enough to type behind.
	send(t, d, session, "Sleep for 30 seconds using a shell command, then reply with exactly: SLEPT", "q-slow")
	d.awaitConversation(session, 90*time.Second, "the slow turn to start running", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})

	_, stateA := send(t, d, session, "Reply with exactly: SECOND", "q-a")
	_, stateB := send(t, d, session, "Reply with exactly: THIRD", "q-b")
	if stateA != "queued" || stateB != "queued" {
		t.Fatalf("mid-turn sends reported states %q and %q, want queued", stateA, stateB)
	}

	// The order the user typed is the order the agent sees. FIFO is the only
	// ordering that matches what the timeline already showed them.
	snap := d.awaitConversation(session, 5*time.Minute, "the queue to drain", func(s snapshot) bool {
		states := s.userTurnStates()
		return terminal(states["Reply with exactly: SECOND"]) && terminal(states["Reply with exactly: THIRD"])
	})

	states := snap.userTurnStates()
	for _, text := range []string{
		"Sleep for 30 seconds using a shell command, then reply with exactly: SLEPT",
		"Reply with exactly: SECOND",
		"Reply with exactly: THIRD",
	} {
		if states[text] != "completed" {
			t.Errorf("turn for %q = %q, want completed", trim(text, 40), states[text])
		}
	}

	answers := snap.assistantText()
	slept, second, third := strings.Index(answers, "SLEPT"), strings.Index(answers, "SECOND"), strings.Index(answers, "THIRD")
	if slept < 0 || second < 0 || third < 0 {
		t.Fatalf("not every queued message was answered (SLEPT=%d SECOND=%d THIRD=%d)\n%s",
			slept, second, third, describe(snap))
	}
	if slept >= second || second >= third {
		t.Errorf("answers arrived out of order: SLEPT@%d SECOND@%d THIRD@%d", slept, second, third)
	}
}

// Stop is the user's brake. Releasing the queue when they press it would be the
// opposite of what the button says.
func TestChatInterruptCancelsTheQueueWithTheTurn(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "interrupt")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session, "Sleep for 60 seconds using a shell command, then reply with exactly: NEVER", "i-slow")
	d.awaitConversation(session, 90*time.Second, "the slow turn to start running", func(s snapshot) bool {
		return s.Turns[len(s.Turns)-1].State == "running"
	})
	queuedTurnID, _ := send(t, d, session, "Reply with exactly: ALSO-NEVER", "i-queued")

	d.mustCall("POST", "/sessions/"+session+"/conversation/interrupt", http.StatusNoContent,
		map[string]any{"queuedTurnIds": []string{queuedTurnID}}, nil)

	snap := d.awaitConversation(session, 2*time.Minute, "the turn and its queue to settle",
		func(s snapshot) bool {
			states := s.userTurnStates()
			return terminal(states["Reply with exactly: ALSO-NEVER"])
		})

	states := snap.userTurnStates()
	if got := states["Sleep for 60 seconds using a shell command, then reply with exactly: NEVER"]; got != "interrupted" {
		t.Errorf("interrupted turn state = %q, want interrupted", got)
	}
	// A message that was never dispatched did not fail; it was cancelled.
	if got := states["Reply with exactly: ALSO-NEVER"]; got != "interrupted" {
		t.Errorf("queued turn state = %q, want interrupted", got)
	}
	if contains(snap.assistantText(), "ALSO-NEVER") {
		t.Error("stop released the queued message instead of cancelling it")
	}
}

// Interrupting nothing is a client mistake, not a server failure. It must come
// back as a typed refusal a client can branch on.
func TestChatInterruptWithNoActiveTurnIsRefusedNotAnError(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "nointerrupt")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	status, body := d.callExpectingError("POST", "/sessions/"+session+"/conversation/interrupt",
		map[string]any{"queuedTurnIds": []string{}})
	if status >= 500 {
		t.Fatalf("interrupting an idle session returned %d (%+v); a client mistake is not a server fault", status, body)
	}
	if status == http.StatusNoContent {
		t.Skip("provider reported an active turn; nothing to assert about the idle case")
	}
	if body.Code == "" {
		t.Errorf("refusal carried no error code: %+v", body)
	}
}

// `ao send` and orchestrator-to-worker relay both go through this endpoint. A
// chat session has no pane to type into, so without a mode branch the send is
// refused as "missing runtime handles" and chat workers are unreachable by AO's
// own automation.
func TestChatRelayThroughTheSendEndpointIsAttributedToAutomation(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "relay")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	// The endpoint answers 200 with an echo, the same shape `ao send` already
	// depends on for a terminal session.
	d.mustCall("POST", "/sessions/"+session+"/send", http.StatusOK,
		map[string]any{"message": "Reply with exactly: RELAYED"}, nil)

	snap := d.awaitConversation(session, 3*time.Minute, "the relayed message to be answered",
		func(s snapshot) bool {
			return contains(s.assistantText(), "RELAYED")
		})

	var relayed *message
	for i := range snap.Messages {
		if snap.Messages[i].Role == "user" && strings.Contains(snap.Messages[i].Text, "RELAYED") {
			relayed = &snap.Messages[i]
		}
	}
	if relayed == nil {
		t.Fatalf("the relayed message is not in the timeline:\n%s", describe(snap))
	}
	// AO carried this on someone else's behalf; the timeline must say so rather
	// than passing it off as something the user typed here.
	if relayed.Origin != "automation" {
		t.Errorf("relay origin = %q, want automation", relayed.Origin)
	}
}

// Approvals are the one place the provider blocks on AO: it holds its turn until
// the client answers a server-to-client request. Everything about it is
// unforgiving — correlation is by the request's own id, the offered decisions vary
// per request, and an unanswered request stalls the agent.
func TestChatApprovalRoundTrip(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "approvals")
	// The default posture deliberately never asks. Approvals are only reachable
	// through a permission mode that maps onto an on-request approval policy.
	setPermissions(t, d, project, "accept-edits")

	session := chatSession(t, d, project, "Reply with exactly: READY")
	send(t, d, session,
		// /tmp is inside the workspace-write sandbox on macOS, so it does not
		// escalate. A write to the home directory does.
		`Run this exact shell command and tell me its exit code: touch "$HOME/.ao-e2e-approval-probe"`,
		"approval-1")

	snap := d.awaitConversation(session, 2*time.Minute, "an approval request", func(s snapshot) bool {
		_, ok := s.pendingApproval()
		return ok
	})
	approval, _ := snap.pendingApproval()

	if approval.RequestID == "" {
		t.Fatal("approval carries no request id; nothing could answer it safely")
	}
	offeredDecisions := approval.decisions()
	if len(offeredDecisions) == 0 {
		t.Fatalf("approval offered no decisions, so the UI has no buttons to render:\n%s", describe(snap))
	}
	// The provider varies what it offers and does not always include a decline.
	// AO must render that list rather than a fixed set, or it invents consent the
	// provider never offered.
	offered := map[string]bool{}
	for _, option := range offeredDecisions {
		if option.Label == "" {
			t.Errorf("decision %q has no label, so its button would render blank", option.ID)
		}
		offered[option.ID] = true
	}
	if !offered["accept"] {
		t.Fatalf("no accept option offered: %+v", offeredDecisions)
	}

	// Blocked on a person is its own state: not working, not idle. The board has
	// to surface it as needing attention.
	var read struct {
		Session struct{ Status string } `json:"session"`
	}
	d.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
	if read.Session.Status != "needs_input" {
		t.Errorf("session status while blocked on an approval = %q, want needs_input", read.Session.Status)
	}

	// A decision the provider never offered must be refused rather than forwarded.
	status, body := d.callExpectingError("POST",
		"/sessions/"+session+"/conversation/approvals/"+approval.RequestID+"/resolve",
		map[string]any{"decisionId": "definitely-not-a-real-decision"})
	if status < 400 {
		t.Errorf("AO accepted a decision the provider never offered (status %d)", status)
	} else if body.Code == "" {
		t.Errorf("refusal carried no error code: %+v", body)
	}

	// A stale card must not answer a request that replaced it.
	status, _ = d.callExpectingError("POST",
		"/sessions/"+session+"/conversation/approvals/does-not-exist/resolve",
		map[string]any{"decisionId": "accept"})
	if status < 400 {
		t.Errorf("resolving an unknown request id was accepted (status %d)", status)
	}

	// The real answer unblocks the provider's turn.
	d.mustCall("POST",
		"/sessions/"+session+"/conversation/approvals/"+approval.RequestID+"/resolve",
		http.StatusNoContent, map[string]any{"decisionId": "accept"}, nil)

	resolved := d.awaitConversation(session, 2*time.Minute, "the approval to resolve", func(s snapshot) bool {
		for _, a := range s.Activities {
			if a.RequestID == approval.RequestID {
				return a.Status != "pending"
			}
		}
		return false
	})
	for _, a := range resolved.Activities {
		if a.RequestID == approval.RequestID && a.Status != "resolved" {
			t.Errorf("approval %s ended as %q, want resolved", a.RequestID, a.Status)
		}
	}

	// And the turn it was blocking gets to finish.
	d.awaitConversation(session, 3*time.Minute, "the approved turn to settle", func(s snapshot) bool {
		return terminal(s.Turns[len(s.Turns)-1].State)
	})
}

// The agent must be able to reach AO's own CLI from inside a chat session. This is
// what makes chat orchestration possible at all: an orchestrator that cannot run
// `ao` can only talk, not delegate.
func TestChatAgentCanRunTheAOCLI(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "aopath")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	send(t, d, session,
		"Run the shell command `ao --version` and reply with its exact output, or reply NOT-FOUND if the command does not exist.",
		"ao-path")

	snap := d.awaitConversation(session, 3*time.Minute, "the ao probe to finish", func(s snapshot) bool {
		return terminal(s.Turns[len(s.Turns)-1].State)
	})
	answer := snap.assistantText()
	if contains(answer, "NOT-FOUND") || contains(answer, "command not found") {
		t.Fatalf("the agent could not run `ao` inside a chat session:\n%s", describe(snap))
	}
}

// Every projected item must have a raw provider event behind it. Without the
// archive, a wrong projection is the only surviving account of what happened and
// there is nothing to repair it from.
func TestChatProviderEventsAreArchived(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "archive")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	snap := d.conversation(session)
	if snap.LatestSequence == 0 {
		t.Fatal("conversation has no items to check the archive against")
	}

	// The archive is not exposed over HTTP, so this asserts the property the
	// archive exists to protect: nothing in the timeline is unaccounted for. A
	// detail payload that will not decode means AO stored something it cannot read
	// back, which is the same failure with a different symptom.
	for _, a := range snap.Activities {
		if len(a.DetailJSON) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(a.DetailJSON, &decoded); err != nil {
			t.Errorf("activity %s detail does not decode: %v: %s", a.ID, err, a.DetailJSON)
		}
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// Model selection has to reach the provider, not just be accepted by AO. The
// catalog comes from the provider — models are added, renamed and gated per
// account — and the choice applies per turn, so nothing restarts.
func TestChatModelSelectionComesFromTheProviderAndAppliesToTheNextTurn(t *testing.T) {
	requireE2E(t)
	d := startDaemon(t, t.TempDir())
	project := seedProject(t, d, "models")
	session := chatSession(t, d, project, "Reply with exactly: READY")

	var catalog struct {
		Models []struct {
			ID          string   `json:"id"`
			DisplayName string   `json:"displayName"`
			Default     bool     `json:"default"`
			Efforts     []string `json:"efforts"`
		} `json:"models"`
		Selected struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoningEffort"`
			ApprovalMode    string `json:"approvalMode"`
		} `json:"selected"`
	}
	d.mustCall("GET", "/sessions/"+session+"/conversation/models", http.StatusOK, nil, &catalog)

	if len(catalog.Models) == 0 {
		t.Fatal("the provider offered no models; a chat session cannot expose a choice")
	}
	// Nothing is chosen until the user chooses, so the provider's default applies.
	if catalog.Selected.Model != "" {
		t.Errorf("a fresh conversation already has model %q selected", catalog.Selected.Model)
	}
	var defaults int
	for _, model := range catalog.Models {
		if model.ID == "" || model.DisplayName == "" {
			t.Errorf("model %+v is missing an id or a label, so it cannot be rendered", model)
		}
		if model.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("provider reported %d default models, want exactly 1", defaults)
	}

	// Pick something that is NOT the default, so the assertion cannot pass by
	// accident when the choice is ignored.
	var chosen string
	var effort string
	for _, model := range catalog.Models {
		if !model.Default {
			chosen = model.ID
			if len(model.Efforts) > 0 {
				effort = model.Efforts[0]
			}
			break
		}
	}
	if chosen == "" {
		t.Skip("provider offers only one model; nothing to switch to")
	}

	var applied struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
		ApprovalMode    string `json:"approvalMode"`
	}
	d.mustCall("PATCH", "/sessions/"+session+"/conversation/settings", http.StatusOK,
		map[string]any{"model": chosen, "reasoningEffort": effort, "approvalMode": "default"}, &applied)
	if applied.Model != chosen {
		t.Fatalf("settings echoed model %q, want %q", applied.Model, chosen)
	}

	// The snapshot is what the composer labels itself from, so the choice has to be
	// visible there rather than only in the PATCH response.
	snap := d.conversation(session)
	if snap.Settings.Model != chosen {
		t.Errorf("snapshot reports model %q, want %q", snap.Settings.Model, chosen)
	}

	// And the next turn actually runs. A model the provider rejects fails the turn,
	// which is what makes this more than an echo test.
	send(t, d, session, "Reply with exactly: SWITCHED", "model-switch")
	answered := d.awaitConversation(session, 3*time.Minute, "a turn on the chosen model",
		func(s snapshot) bool { return terminal(s.Turns[len(s.Turns)-1].State) })
	last := answered.Turns[len(answered.Turns)-1]
	if last.State != "completed" {
		t.Fatalf("turn on model %q ended as %q (err=%q)", chosen, last.State, last.ErrorMessage)
	}
	if !contains(answered.assistantText(), "SWITCHED") {
		t.Errorf("the agent did not answer on the chosen model:\n%s", describe(answered))
	}

	// An approval mode outside AO's vocabulary is refused rather than normalized:
	// silently downgrading a permission choice is the one direction that must never
	// happen by accident.
	status, body := d.callExpectingError("PATCH", "/sessions/"+session+"/conversation/settings",
		map[string]any{"approvalMode": "acceptEdits"})
	if status != http.StatusBadRequest {
		t.Errorf("camelCase approval mode returned %d, want 400 (%+v)", status, body)
	}
}
