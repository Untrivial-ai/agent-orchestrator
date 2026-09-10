package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Projection of the provider signal AO used to drop: reasoning streams, plans,
// terminal input, auto-approval reviews, model reroutes, account state, thread
// state and MCP server health.
//
// Against the real store, like the rest of this package: the claim is that these
// land as durable rows a snapshot can serve, which a mock store cannot show.

/* ---- reasoning --------------------------------------------------------- */

// Reasoning streams into the activity's own text and is then replaced by the
// provider's settled summary. Replacing rather than appending is what makes a
// dropped delta cosmetic.
func TestReasoningStreamsThenSettles(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderTurnID: "pt-1", ProviderItemID: "rs_1",
			ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "Reasoning",
		},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", Delta: "**Checking"},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", Delta: " the plan**"},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return activityByItem(s, "rs_1").StreamedText == "**Checking the plan**"
	})
	activity := activityByItem(snapshot, "rs_1")
	if activity.Kind != domain.ActivityKindReasoning {
		t.Fatalf("kind = %q", activity.Kind)
	}
	// Reasoning is not command output. A reader must be able to tell "the agent
	// thought this" from "the program printed this".
	if activity.CommandOutput != "" {
		t.Errorf("reasoning landed in command output: %q", activity.CommandOutput)
	}

	h.conv.emit(ports.ChatEvent{
		Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "pt-1", ProviderItemID: "rs_1",
		ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusCompleted,
		Summary: "Reasoning", Text: "**Checked the plan**",
	})

	settled := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return activityByItem(s, "rs_1").StreamedText == "**Checked the plan**"
	})
	if got := activityByItem(settled, "rs_1").Status; got != domain.ActivityStatusCompleted {
		t.Fatalf("status = %q", got)
	}
	// One row, not one per delta.
	if n := countActivities(settled, domain.ActivityKindReasoning); n != 1 {
		t.Fatalf("reasoning activities = %d, want 1", n)
	}
}

// A completion carrying no settled text must not wipe what streamed. A reasoning
// item with an empty summary array is the normal shape on a provider that streams
// no summaries, and erasing the accumulation on it would delete reasoning the user
// watched arrive.
func TestEmptySettleKeepsStreamedReasoning(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderTurnID: "pt-1", ProviderItemID: "rs_1",
			ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusRunning,
		},
		ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, ProviderItemID: "rs_1", Delta: "thought"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "pt-1", ProviderItemID: "rs_1",
			ActivityKind: domain.ActivityKindReasoning, ActivityStatus: domain.ActivityStatusCompleted,
			Text: "",
		},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return activityByItem(s, "rs_1").Status == domain.ActivityStatusCompleted
	})
	if got := activityByItem(snapshot, "rs_1").StreamedText; got != "thought" {
		t.Fatalf("streamed text = %q, want it kept", got)
	}
}

/* ---- terminal input ---------------------------------------------------- */

// The keystrokes the agent sent into a PTY are kept apart from the program's
// output. The PTY echoes what is typed, so one shared stream would show every
// typed line twice.
func TestTerminalInputStaysOutOfCommandOutput(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderTurnID: "pt-1", ProviderItemID: "exec-1",
			ActivityKind: domain.ActivityKindCommand, ActivityStatus: domain.ActivityStatusRunning,
			Summary: "python3",
		},
		ports.ChatEvent{Kind: ports.ChatEventCommandOutputDelta, ProviderItemID: "exec-1", Delta: "Python 3.13.0\n"},
		ports.ChatEvent{Kind: ports.ChatEventCommandInput, ProviderItemID: "exec-1", Delta: "print(6*7)\n"},
		ports.ChatEvent{Kind: ports.ChatEventCommandOutputDelta, ProviderItemID: "exec-1", Delta: "42\n"},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		activity := activityByItem(s, "exec-1")
		return activity.StreamedText == "print(6*7)\n" &&
			activity.CommandOutput == "Python 3.13.0\n42\n"
	})
	activity := activityByItem(snapshot, "exec-1")
	if activity.CommandOutput != "Python 3.13.0\n42\n" {
		t.Fatalf("command output = %q", activity.CommandOutput)
	}
}

/* ---- plan -------------------------------------------------------------- */

// The plan lands as turn state AND as one timeline row that mutates in place. The
// column answers "what is the plan now" without walking the timeline; the row
// answers "where in the conversation did the agent plan", which the column cannot.
func TestPlanUpdatesOverwriteTurnStateAndOneRow(t *testing.T) {
	h := newHarness(t)

	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "do three things", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	first := domain.ConversationPlan{Steps: []domain.ConversationPlanStep{
		{Text: "one", Status: domain.PlanStepInProgress},
		{Text: "two", Status: domain.PlanStepPending},
	}}
	second := domain.ConversationPlan{Steps: []domain.ConversationPlanStep{
		{Text: "one", Status: domain.PlanStepCompleted},
		{Text: "two", Status: domain.PlanStepCompleted},
	}}

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: "provider-turn-1",
			Plan: &first, Summary: "Plan 0/2: one"},
		ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: "provider-turn-1",
			Plan: &second, Summary: "Plan 2/2 steps done"},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		for _, turn := range s.Turns {
			if turn.Plan != nil && len(turn.Plan.Steps) == 2 &&
				turn.Plan.Steps[1].Status == domain.PlanStepCompleted {
				return true
			}
		}
		return false
	})

	// Two updates, one row: the provider re-sends the whole plan, so a row per
	// update would read as though the agent had planned twice.
	if n := countActivities(snapshot, domain.ActivityKindPlan); n != 1 {
		t.Fatalf("plan activities = %d, want 1", n)
	}
	row := firstActivityOfKind(snapshot, domain.ActivityKindPlan)
	if row.Summary != "Plan 2/2 steps done" {
		t.Errorf("summary = %q, want the latest", row.Summary)
	}
	if row.Status != domain.ActivityStatusCompleted {
		t.Errorf("status = %q: every step is done", row.Status)
	}

	var detail struct {
		Event string `json:"event"`
		Steps []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(row.Detail, &detail); err != nil {
		t.Fatalf("plan detail is not an object: %v (%s)", err, row.Detail)
	}
	if detail.Event != "plan" {
		t.Errorf("detail event = %q", detail.Event)
	}
	// Structure, not prose: a client that wants checkboxes needs the per-step status,
	// and joining steps into text is not reversible.
	if len(detail.Steps) != 2 || detail.Steps[0].Status != string(domain.PlanStepCompleted) {
		t.Fatalf("detail steps = %+v", detail.Steps)
	}
}

// A plan with work left is not a completed thing. Reporting it as one would tick
// off steps the agent is still on.
func TestPlanInProgressStaysRunning(t *testing.T) {
	h := newHarness(t)
	plan := domain.ConversationPlan{Steps: []domain.ConversationPlanStep{
		{Text: "one", Status: domain.PlanStepCompleted},
		{Text: "two", Status: domain.PlanStepInProgress},
	}}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: "pt-1", Plan: &plan},
	)
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return countActivities(s, domain.ActivityKindPlan) == 1
	})
	if got := firstActivityOfKind(snapshot, domain.ActivityKindPlan).Status; got != domain.ActivityStatusRunning {
		t.Fatalf("status = %q, want running", got)
	}
}

// A provider is allowed to omit a final plan notification. Successful turn
// completion is AO's durable proof that the remaining plan work finished, so
// both copies of the plan must settle with the turn instead of leaving a
// completed answer beside a permanent "0 / N" plan.
func TestSuccessfulCompletionFinalizesPlanWhenProviderOmitsTerminalPlan(t *testing.T) {
	h := newHarness(t)

	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "do four things", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	plan := domain.ConversationPlan{Steps: []domain.ConversationPlanStep{
		{Text: "one", Status: domain.PlanStepInProgress},
		{Text: "two", Status: domain.PlanStepPending},
		{Text: "three", Status: domain.PlanStepPending},
		{Text: "four", Status: domain.PlanStepPending},
	}}
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventPlanUpdated, ProviderTurnID: "provider-turn-1",
			Plan: &plan, Summary: "Plan 0/4: one"},
		ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: "provider-turn-1",
			ProviderItemID: "message-1", Text: "Done."},
		ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1",
			TurnState: domain.TurnStateCompleted},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateCompleted
	})
	turn := snapshot.Turns[0]
	if turn.Plan == nil || len(turn.Plan.Steps) != 4 {
		t.Fatalf("turn plan = %#v, want four steps", turn.Plan)
	}
	for i, step := range turn.Plan.Steps {
		if step.Status != domain.PlanStepCompleted {
			t.Errorf("turn plan step %d status = %q, want completed", i, step.Status)
		}
	}

	activity := firstActivityOfKind(snapshot, domain.ActivityKindPlan)
	if activity.Status != domain.ActivityStatusCompleted {
		t.Errorf("plan activity status = %q, want completed", activity.Status)
	}
	if activity.Summary != "Plan 4/4 steps done" {
		t.Errorf("plan activity summary = %q, want completed count", activity.Summary)
	}
	var detail struct {
		Steps []domain.ConversationPlanStep `json:"steps"`
	}
	if err := json.Unmarshal(activity.Detail, &detail); err != nil {
		t.Fatalf("decode plan activity detail: %v", err)
	}
	for i, step := range detail.Steps {
		if step.Status != domain.PlanStepCompleted {
			t.Errorf("plan activity step %d status = %q, want completed", i, step.Status)
		}
	}
}

/* ---- model reroute ----------------------------------------------------- */

// A reroute becomes conversation state AND a timeline row. The state says what is
// answering now; the row says where the line falls, which state alone cannot.
func TestModelRerouteIsRecordedAndPlacedInTheTimeline(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventModelRerouted, ProviderTurnID: "pt-1",
			Reroute: &ports.ChatModelReroute{
				FromModel: "gpt-5.6-sol",
				ToModel:   "gpt-5.6-safety",
				Reason:    "highRiskCyberActivity",
			},
		},
	)

	// Both halves, because the state and the timeline row are two writes: waiting only
	// on the state would race the row this test then reads.
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ModelReroute != nil &&
			countActivities(s, domain.ActivityKindSystem) == 1
	})
	reroute := snapshot.Conversation.ModelReroute
	if reroute.FromModel != "gpt-5.6-sol" || reroute.ToModel != "gpt-5.6-safety" {
		t.Fatalf("reroute = %+v", reroute)
	}
	if reroute.Reason != "highRiskCyberActivity" {
		t.Errorf("reason = %q", reroute.Reason)
	}
	if reroute.ProviderTurnID != "pt-1" {
		t.Errorf("turn = %q", reroute.ProviderTurnID)
	}
	if reroute.At.IsZero() {
		t.Error("reroute has no timestamp")
	}

	row := firstActivityOfKind(snapshot, domain.ActivityKindSystem)
	if row.Summary != "Provider answered with gpt-5.6-safety instead of gpt-5.6-sol" {
		t.Fatalf("summary = %q", row.Summary)
	}
}

/* ---- account ----------------------------------------------------------- */

// Account reports arrive in pieces, so they merge. Writing each straight through
// would mean a session whose tokens expired lost its plan label, and one that
// changed plan looked like its credentials were fine again.
func TestAccountReportsMergeRatherThanReplace(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(ports.ChatEvent{
		Kind:    ports.ChatEventAccountChanged,
		Account: &ports.ChatAccount{AuthMode: "chatgpt", PlanLabel: "pro"},
	})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.Account != nil && s.Conversation.Account.PlanLabel == "pro"
	})

	h.conv.emit(ports.ChatEvent{
		Kind:    ports.ChatEventAccountChanged,
		Account: &ports.ChatAccount{ReauthRequired: true, ReauthReason: "unauthorized"},
	})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.Account != nil && s.Conversation.Account.ReauthRequiredAt != nil &&
			countActivities(s, domain.ActivityKindSystem) == 1
	})
	account := snapshot.Conversation.Account
	if account.AuthMode != "chatgpt" || account.PlanLabel != "pro" {
		t.Fatalf("a credential demand blanked what AO already knew: %+v", account)
	}
	if account.ReauthReason != "unauthorized" {
		t.Errorf("reason = %q", account.ReauthReason)
	}

	// The one account fact the user has to act on gets a row: a turn that failed
	// because the provider wanted a login it could not get is otherwise
	// indistinguishable from any other failed turn.
	row := firstActivityOfKind(snapshot, domain.ActivityKindSystem)
	if row.Summary != "The provider needs you to sign in again" {
		t.Fatalf("summary = %q", row.Summary)
	}
	if row.Status != domain.ActivityStatusFailed {
		t.Errorf("status = %q", row.Status)
	}

	// A repeated demand updates the notice rather than filling the timeline with it.
	h.conv.emit(ports.ChatEvent{
		Kind:    ports.ChatEventAccountChanged,
		Account: &ports.ChatAccount{ReauthRequired: true, ReauthReason: "unauthorized"},
	})
	time.Sleep(100 * time.Millisecond)
	again, err := h.st.LoadConversationSnapshot(context.Background(), h.ctrl.ConversationID())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := countActivities(again, domain.ActivityKindSystem); n != 1 {
		t.Fatalf("system rows = %d, want the notice to be updated in place", n)
	}
}

/* ---- thread state ----------------------------------------------------- */

// Each report updates only what it spoke about. An ordinary idle report must not
// un-archive a thread, and an archive report must not blank the status.
func TestThreadStateReportsAreTriState(t *testing.T) {
	h := newHarness(t)

	archived := true
	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventThreadState, ThreadState: &ports.ChatThreadState{
			Status:    domain.ThreadStatusActive,
			WaitingOn: []string{"waiting_on_approval"},
		}},
		ports.ChatEvent{Kind: ports.ChatEventThreadState, ThreadState: &ports.ChatThreadState{
			Archived: &archived,
		}},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ThreadState != nil && s.Conversation.ThreadState.ArchivedAt != nil
	})
	state := snapshot.Conversation.ThreadState
	if state.Status != domain.ThreadStatusActive {
		t.Fatalf("an archive report blanked the status: %+v", state)
	}
	if len(state.WaitingOn) != 1 {
		t.Errorf("waiting on = %+v", state.WaitingOn)
	}

	// A later active report with no flags clears them: a thread no longer blocked
	// must not keep looking blocked.
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadState,
		ThreadState: &ports.ChatThreadState{Status: domain.ThreadStatusIdle}})

	settled := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ThreadState.Status == domain.ThreadStatusIdle
	})
	if len(settled.Conversation.ThreadState.WaitingOn) != 0 {
		t.Errorf("waiting on = %+v, want cleared", settled.Conversation.ThreadState.WaitingOn)
	}
	if settled.Conversation.ThreadState.ArchivedAt == nil {
		t.Error("a status report un-archived the thread")
	}
}

// A closed thread is recorded, not acted on. Tearing a controller down on a
// notification no probe has ever seen would turn a provider quirk into a lost
// session.
func TestClosedThreadDoesNotStopTheController(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventThreadState,
		ThreadState: &ports.ChatThreadState{Closed: true, Status: domain.ThreadStatusClosed}})

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return s.Conversation.ThreadState != nil && s.Conversation.ThreadState.ClosedAt != nil
	})
	if snapshot.Conversation.ThreadState.Status != domain.ThreadStatusClosed {
		t.Fatalf("status = %q", snapshot.Conversation.ThreadState.Status)
	}
	if got := h.ctrl.State(); got == ports.ChatControllerStopped {
		t.Fatal("a closed thread stopped the controller")
	}
}

/* ---- MCP servers ------------------------------------------------------- */

// Servers are announced one at a time and re-announced every turn, so reports
// merge by name and keep first-seen order. A list that reshuffles between polls is
// unreadable.
func TestMCPServerReportsMergeByName(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventMCPServers,
			MCPServers: []ports.ChatMCPServer{{Name: "probe", Status: "starting"}}},
		ports.ChatEvent{Kind: ports.ChatEventMCPServers,
			MCPServers: []ports.ChatMCPServer{{Name: "github", Status: "failed",
				Error: "connection refused", FailureReason: "reauthenticationRequired"}}},
		ports.ChatEvent{Kind: ports.ChatEventMCPServers,
			MCPServers: []ports.ChatMCPServer{{Name: "probe", Status: "ready"}}},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		servers := s.Conversation.MCPServers
		return len(servers) == 2 && servers[0].Status == "ready"
	})
	servers := snapshot.Conversation.MCPServers
	if servers[0].Name != "probe" || servers[1].Name != "github" {
		t.Fatalf("order = %+v, want first-seen", servers)
	}
	// The classification is what makes a failure actionable.
	if servers[1].FailureReason != "reauthenticationRequired" {
		t.Errorf("failure reason = %q", servers[1].FailureReason)
	}
	if servers[1].Error != "connection refused" {
		t.Errorf("error = %q", servers[1].Error)
	}
}

// A driver whose provider cannot reload gets a permanent answer, so a client stops
// offering the control rather than retrying something that will never work.
func TestReloadMCPServersUnsupportedIsPermanent(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.ReloadMCPServers(context.Background(), testSession)
	if !errors.Is(err, chatsvc.ErrMCPReloadUnsupported) {
		t.Fatalf("err = %v, want ErrMCPReloadUnsupported", err)
	}
}

// A reload tears down and re-establishes every tool the agent has, so it is
// refused mid-turn rather than pulling tools out from under work in flight.
func TestReloadMCPServersRefusedWhileBusy(t *testing.T) {
	reloader := &mcpReloadRecorder{fakeConversation: newFakeConversation()}
	h := newHarnessWithConversation(t, reloader)

	if _, err := h.svc.Send(context.Background(), testSession, ports.ChatUserMessage{
		Text: "work", Origin: domain.MessageOriginHuman,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if _, err := h.svc.ReloadMCPServers(context.Background(), testSession); !errors.Is(err, chatsvc.ErrTurnRunning) {
		t.Fatalf("err = %v, want ErrTurnRunning", err)
	}
	if reloader.calls() != 0 {
		t.Fatal("the reload reached the provider while a turn was running")
	}
}

// The servers a reload reports are merged into conversation state, so the outcome
// is durable rather than only being returned to whoever asked.
func TestReloadMCPServersRecordsWhatCameBack(t *testing.T) {
	reloader := &mcpReloadRecorder{
		fakeConversation: newFakeConversation(),
		servers:          []ports.ChatMCPServer{{Name: "probe", Status: "ready"}},
	}
	h := newHarnessWithConversation(t, reloader)

	servers, err := h.svc.ReloadMCPServers(context.Background(), testSession)
	if err != nil {
		t.Fatalf("ReloadMCPServers: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "probe" {
		t.Fatalf("servers = %+v", servers)
	}

	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Conversation.MCPServers) == 1 &&
			s.Conversation.MCPServers[0].Status == "ready"
	})
}

/* ---- auto-approval review ---------------------------------------------- */

// An auto-review is its own kind, not an approval. An approval is a question
// waiting on a person; this is a decision already made for them, and the two must
// not render as the same thing.
func TestAutoReviewIsItsOwnActivityKind(t *testing.T) {
	h := newHarness(t)

	h.conv.emit(
		ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "pt-1"},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityStarted, ProviderTurnID: "pt-1",
			ProviderItemID: "ao-review-r1",
			ActivityKind:   domain.ActivityKindAutoReview,
			ActivityStatus: domain.ActivityStatusRunning,
			Summary:        "Reviewing curl -s https://example.com",
		},
		ports.ChatEvent{
			Kind: ports.ChatEventActivityCompleted, ProviderTurnID: "pt-1",
			ProviderItemID: "ao-review-r1",
			ActivityKind:   domain.ActivityKindAutoReview,
			ActivityStatus: domain.ActivityStatusCompleted,
			Summary:        "Auto-approved curl -s https://example.com (low risk)",
			Detail:         []byte(`{"riskLevel":"low","rationale":"low-risk allow"}`),
		},
	)

	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return activityByItem(s, "ao-review-r1").Status == domain.ActivityStatusCompleted
	})
	row := activityByItem(snapshot, "ao-review-r1")
	if row.Kind != domain.ActivityKindAutoReview {
		t.Fatalf("kind = %q, want %q", row.Kind, domain.ActivityKindAutoReview)
	}
	// One row: started and completed name the same synthetic key, so the review
	// mutates rather than appearing twice.
	if n := countActivities(snapshot, domain.ActivityKindAutoReview); n != 1 {
		t.Fatalf("auto review rows = %d, want 1", n)
	}
	if row.Summary != "Auto-approved curl -s https://example.com (low risk)" {
		t.Errorf("summary = %q", row.Summary)
	}
}

/* ---- helpers ----------------------------------------------------------- */

// mcpReloadRecorder is a provider double that can reload its tool servers.
type mcpReloadRecorder struct {
	*fakeConversation
	servers []ports.ChatMCPServer

	mu    sync.Mutex
	count int
}

func (r *mcpReloadRecorder) ReloadMCPServers(context.Context) ([]ports.ChatMCPServer, error) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return r.servers, nil
}

func (r *mcpReloadRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func activityByItem(s store.ConversationSnapshot, itemID string) domain.ConversationActivity {
	for _, activity := range s.Activities {
		if activity.ProviderItemID == itemID {
			return activity
		}
	}
	return domain.ConversationActivity{}
}

func countActivities(s store.ConversationSnapshot, kind domain.ActivityKind) int {
	count := 0
	for _, activity := range s.Activities {
		if activity.Kind == kind {
			count++
		}
	}
	return count
}

func firstActivityOfKind(s store.ConversationSnapshot, kind domain.ActivityKind) domain.ConversationActivity {
	for _, activity := range s.Activities {
		if activity.Kind == kind {
			return activity
		}
	}
	return domain.ConversationActivity{}
}
