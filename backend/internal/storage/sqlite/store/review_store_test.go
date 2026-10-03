package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestCreateReviewConversationClaimsChatMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{
		ID: "rev-chat", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}

	conversation, err := s.CreateReviewConversation(ctx, "conv-review", review.ID, rec.ProjectID, rec.ID, now)
	if err != nil {
		t.Fatalf("create reviewer conversation: %v", err)
	}
	if conversation.ReviewID != review.ID || conversation.SessionID != rec.ID {
		t.Fatalf("reviewer conversation = %+v", conversation)
	}

	got, ok, err := s.GetReviewByID(ctx, review.ID)
	if err != nil || !ok {
		t.Fatalf("get review after chat claim: ok=%v err=%v", ok, err)
	}
	if got.InterfaceMode != domain.ReviewerInterfaceChat {
		t.Fatalf("interface mode = %q, want chat", got.InterfaceMode)
	}

	// Engine refreshes omit the mode; they must not demote an established Chat
	// reviewer back to the legacy TUI surface.
	review.UpdatedAt = now.Add(time.Second)
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("refresh review: %v", err)
	}
	got, _, err = s.GetReviewByID(ctx, review.ID)
	if err != nil {
		t.Fatalf("get refreshed review: %v", err)
	}
	if got.InterfaceMode != domain.ReviewerInterfaceChat {
		t.Fatalf("refreshed interface mode = %q, want chat", got.InterfaceMode)
	}
	if claimed, err := s.ClaimReviewChatController(ctx, review.ID, "provider-1", "generation-1", now.Add(2*time.Second)); err != nil || !claimed {
		t.Fatalf("claim reviewer controller: claimed=%v err=%v", claimed, err)
	}
}

func TestRestoreReviewLaunchStateRecoversIdentifiersClearedForModeSwitch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "review-rollback")
	session, err := s.CreateSession(ctx, sampleRecord("review-rollback"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{ID: "review-rollback", SessionID: session.ID, ProjectID: session.ProjectID,
		Harness: domain.ReviewerCodex, InterfaceMode: domain.ReviewerInterfaceChat,
		ReviewerHandleID: "review-chat:review-rollback", AgentSessionID: "provider-1",
		ReviewerLaunchID: "launch-1", ProviderConversationID: "provider-1", ControllerGeneration: "generation-1",
		ReviewerActivityState: domain.ActivityActive, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	if ok, err := s.SetReviewInterfaceMode(ctx, review.ID, domain.ReviewerInterfaceTUI, now.Add(time.Second)); err != nil || !ok {
		t.Fatalf("switch to terminal: ok=%v err=%v", ok, err)
	}
	if ok, err := s.RestoreReviewLaunchState(ctx, review); err != nil || !ok {
		t.Fatalf("restore previous launch: ok=%v err=%v", ok, err)
	}
	got, ok, err := s.GetReviewByID(ctx, review.ID)
	if err != nil || !ok {
		t.Fatalf("get restored review: ok=%v err=%v", ok, err)
	}
	if got.InterfaceMode != review.InterfaceMode || got.ReviewerHandleID != review.ReviewerHandleID || got.AgentSessionID != review.AgentSessionID || got.ProviderConversationID != review.ProviderConversationID || got.ControllerGeneration != review.ControllerGeneration || got.ReviewerLaunchID != review.ReviewerLaunchID {
		t.Fatalf("restored launch = %+v, want %+v", got, review)
	}
	recoverable, err := s.ListRecoverableChatReviews(ctx)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != review.ID {
		t.Fatalf("recoverable chats = %+v, err=%v", recoverable, err)
	}
}

func TestCreateAndActivateReviewConversationBranchClaimsReview(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "review-branch")
	session, err := s.CreateSession(ctx, sampleRecord("review-branch"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{ID: "review-branch", SessionID: session.ID, ProjectID: session.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	conversation, err := s.CreateReviewConversation(ctx, "review-branch-conversation", review.ID, session.ProjectID, session.ID, now)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	branch := domain.ConversationBranch{
		ID: "fresh-review-branch", ConversationID: conversation.ID, SessionID: session.ID,
		ParentBranchID: conversation.ActiveBranchID, ProviderConversationID: "fresh-provider",
		ProviderScopeID: "fresh-review-branch", ProviderIDsScoped: true, CreatedAt: now,
	}
	if err := s.CreateAndActivateReviewConversationBranch(ctx, review.ID, branch, "review-generation", now.Add(time.Second)); err != nil {
		t.Fatalf("activate review branch: %v", err)
	}
	got, ok, err := s.GetReviewByID(ctx, review.ID)
	if err != nil || !ok || got.ProviderConversationID != "fresh-provider" || got.ControllerGeneration != "review-generation" {
		t.Fatalf("review controller = %+v, ok=%v err=%v", got, ok, err)
	}
	active, err := s.ConversationBranch(ctx, conversation.ID, branch.ID)
	if err != nil || active.ProviderConversationID != "fresh-provider" {
		t.Fatalf("active branch = %+v, err=%v", active, err)
	}
}

func TestReviewProviderEventsUseReviewControllerFence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "review-events")
	session, err := s.CreateSession(ctx, sampleRecord("review-events"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{ID: "review-events", SessionID: session.ID, ProjectID: session.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	conversation, err := s.CreateReviewConversation(ctx, "review-events-conversation", review.ID, session.ProjectID, session.ID, now)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if claimed, err := s.ClaimReviewChatController(ctx, review.ID, "provider-1", "review-generation", now); err != nil || !claimed {
		t.Fatalf("claim reviewer controller: claimed=%v err=%v", claimed, err)
	}
	projected := 0
	project := func(context.Context) error { projected++; return nil }
	applied, err := s.ProjectReviewProviderEvent(ctx, conversation.ID, session.ID, review.ID,
		"review-generation", "event-1", "message.delta", `{}`, now, project)
	if err != nil || !applied || projected != 1 {
		t.Fatalf("review event: applied=%v projected=%d err=%v", applied, projected, err)
	}
	applied, err = s.ProjectReviewProviderEvent(ctx, conversation.ID, session.ID, review.ID,
		"stale-generation", "event-2", "message.delta", `{}`, now, project)
	if err != nil || applied || projected != 1 {
		t.Fatalf("stale review event: applied=%v projected=%d err=%v", applied, projected, err)
	}
	events, err := s.ProviderEventsSince(ctx, conversation.ID, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("review event archive = %+v, err=%v", events, err)
	}
}

func TestCleanupOwnedReviewControllerWorkSettlesItsTurn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "review-cleanup")
	session, err := s.CreateSession(ctx, sampleRecord("review-cleanup"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	review := domain.Review{
		ID: "review-cleanup", SessionID: session.ID, ProjectID: session.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.UpsertReview(ctx, review); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	conversation, err := s.CreateReviewConversation(ctx, "review-cleanup-conversation", review.ID, session.ProjectID, session.ID, now)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if claimed, err := s.ClaimReviewChatController(ctx, review.ID, "provider-1", "review-generation", now); err != nil || !claimed {
		t.Fatalf("claim reviewer controller: claimed=%v err=%v", claimed, err)
	}
	created, err := s.AppendReviewUserMessage(ctx, conversation.ID, session.ID, review.ID, "review-generation", domain.ConversationMessage{
		ID: "review-message", Text: "Review the change", Origin: domain.MessageOriginHuman,
	}, "review-turn", now)
	if err != nil || !created {
		t.Fatalf("append review message: created=%v err=%v", created, err)
	}
	if err := s.BindTurnToProvider(ctx, "review-turn", "provider-turn", now); err != nil {
		t.Fatalf("bind turn: %v", err)
	}
	if err := s.UpsertActivity(ctx, conversation.ID, "provider-turn", domain.ConversationActivity{
		ID: "review-approval", Kind: domain.ActivityKindApproval, Status: domain.ActivityStatusPending,
		Summary: "Approve", RequestID: "request-1", ProviderItemID: "item-1",
	}, now); err != nil {
		t.Fatalf("upsert approval: %v", err)
	}

	owned, err := s.CleanupOwnedReviewControllerWork(ctx, review.ID, conversation.ID, "stale-generation", now.Add(time.Minute))
	if err != nil || owned {
		t.Fatalf("stale cleanup: owned=%v err=%v", owned, err)
	}
	owned, err = s.CleanupOwnedReviewControllerWork(ctx, review.ID, conversation.ID, "review-generation", now.Add(2*time.Minute))
	if err != nil || !owned {
		t.Fatalf("owned cleanup: owned=%v err=%v", owned, err)
	}
	snapshot, err := s.LoadConversationSnapshot(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].State != domain.TurnStateFailed {
		t.Fatalf("reviewer turns = %+v, want one failed turn", snapshot.Turns)
	}
	if len(snapshot.Activities) != 1 || snapshot.Activities[0].Status != domain.ActivityStatusFailed {
		t.Fatalf("reviewer activities = %+v, want one failed approval", snapshot.Activities)
	}
}

func TestInsertReviewRunDuplicatePRSHAMapsToSentinel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// A second run for the same (session_id, pr_url, target_sha, harness) hits the
	// partial unique index (migration 0041) and must surface as the sentinel so
	// the engine can fall back to the existing run.
	dup := run
	dup.ID = "run-2"
	if err := s.InsertReviewRun(ctx, dup); !errors.Is(err, domain.ErrDuplicateReviewRun) {
		t.Fatalf("duplicate insert err = %v, want ErrDuplicateReviewRun", err)
	}

	otherPR := run
	otherPR.ID = "run-other-pr"
	otherPR.PRURL = "https://example/pr/2"
	if err := s.InsertReviewRun(ctx, otherPR); err != nil {
		t.Fatalf("same sha on different PR should insert: %v", err)
	}

	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunFailed, domain.VerdictNone, "claude: not found", "", true); err != nil {
		t.Fatalf("mark failed: %v", err)
	} else if !ok {
		t.Fatal("mark failed: got ok=false")
	}
	if err := s.InsertReviewRun(ctx, dup); err != nil {
		t.Fatalf("retry after failed insert: %v", err)
	}

	// An empty target_sha is excluded from the index, so two are allowed.
	for _, id := range []string{"run-empty-1", "run-empty-2"} {
		r := run
		r.ID, r.TargetSHA = id, ""
		if err := s.InsertReviewRun(ctx, r); err != nil {
			t.Fatalf("empty-sha insert %s: %v", id, err)
		}
	}
}

// Harness is part of the idempotency key, so a different reviewer on the same
// commit is a second opinion rather than a duplicate. Without this the reviewer
// picker is inert on an already-reviewed commit.
func TestInsertReviewRunAllowsADifferentHarnessForTheSameCommit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	first := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, first); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	other := first
	other.ID = "run-other-harness"
	other.Harness = domain.ReviewerCodex
	if err := s.InsertReviewRun(ctx, other); err != nil {
		t.Fatalf("a different harness on the same commit should insert: %v", err)
	}

	// ...but the same harness twice is still a duplicate.
	same := first
	same.ID = "run-same-harness"
	if err := s.InsertReviewRun(ctx, same); !errors.Is(err, domain.ErrDuplicateReviewRun) {
		t.Fatalf("same harness duplicate err = %v, want ErrDuplicateReviewRun", err)
	}
}

func TestInsertReviewRunAllowsRerunAfterChangesRequested(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictChangesRequested, "please fix", "rev-1", true); err != nil {
		t.Fatalf("mark changes requested: %v", err)
	} else if !ok {
		t.Fatal("mark changes requested: got ok=false")
	}

	rerun := run
	rerun.ID = "run-2"
	rerun.CreatedAt = now.Add(time.Second)
	if err := s.InsertReviewRun(ctx, rerun); err != nil {
		t.Fatalf("rerun after changes_requested insert: %v", err)
	}
}

func TestInsertReviewRunAllowsRerunAfterTerminalEmptyVerdict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	run := domain.ReviewRun{
		ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerClaudeCode,
		PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunComplete, Verdict: domain.VerdictNone, CreatedAt: now,
	}
	if err := s.InsertReviewRun(ctx, run); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	rerun := run
	rerun.ID = "run-2"
	rerun.Status = domain.ReviewRunRunning
	rerun.CreatedAt = now.Add(time.Second)
	if err := s.InsertReviewRun(ctx, rerun); err != nil {
		t.Fatalf("rerun after terminal empty-verdict insert: %v", err)
	}
}

func TestReviewUpsertReusesRowAndRunRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)

	// First upsert creates the review row.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerClaudeCode, PRURL: "https://example/pr/1",
		ReviewerHandleID: "review-mer-1",
		CreatedAt:        now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	// A different harness gets its own row for the same worker session.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-2", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerHarness("greptile"), PRURL: "https://example/pr/2",
		ReviewerHandleID: "review-mer-1b",
		ReviewerLaunchID: "launch-2",
		CreatedAt:        now, UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("upsert review (second harness): %v", err)
	}
	got, ok, err := s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerClaudeCode)
	if err != nil || !ok {
		t.Fatalf("get claude review: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-1" {
		t.Fatalf("claude review id = %q, want rev-1", got.ID)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-2" {
		t.Fatalf("greptile review id = %q, want rev-2", got.ID)
	}
	if got.Harness != domain.ReviewerHarness("greptile") || got.PRURL != "https://example/pr/2" || got.ReviewerHandleID != "review-mer-1b" {
		t.Fatalf("second harness fields: %+v", got)
	}
	// A same-harness upsert reuses that row and records the native session id.
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-3", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerHarness("greptile"), PRURL: "https://example/pr/2",
		ReviewerHandleID: "review-mer-1b",
		AgentSessionID:   "reviewer-native-1",
		CreatedAt:        now, UpdatedAt: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("upsert review (agent session): %v", err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get review after agent session update: ok=%v err=%v", ok, err)
	}
	if got.ID != "rev-2" {
		t.Fatalf("same-harness upsert changed row id = %q, want rev-2", got.ID)
	}
	if got.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("agent session id = %q, want reviewer-native-1", got.AgentSessionID)
	}
	updated, err := s.UpdateReviewActivity(ctx, "rev-1", domain.ActivityIdle, "claude-native-1", "")
	if err != nil || !updated {
		t.Fatalf("update claude activity/native session: updated=%v err=%v", updated, err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerClaudeCode)
	if err != nil || !ok {
		t.Fatalf("get claude review after native update: ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "claude-native-1" {
		t.Fatalf("claude agent session id = %q, want claude-native-1", got.AgentSessionID)
	}
	if got.ReviewerActivityState != domain.ActivityIdle {
		t.Fatalf("claude reviewer activity state = %q, want idle", got.ReviewerActivityState)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review after claude update: ok=%v err=%v", ok, err)
	}
	if got.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("greptile agent session id = %q, want reviewer-native-1", got.AgentSessionID)
	}
	if got.ReviewerLaunchID != "launch-2" {
		t.Fatalf("greptile reviewer launch id = %q, want launch-2", got.ReviewerLaunchID)
	}
	updated, err = s.UpdateReviewActivity(ctx, "rev-2", domain.ActivityIdle, "", "stale-launch")
	if err != nil {
		t.Fatalf("stale launch update: %v", err)
	}
	if updated {
		t.Fatal("stale launch update unexpectedly succeeded")
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review after stale launch update: ok=%v err=%v", ok, err)
	}
	if got.ReviewerActivityState != "" {
		t.Fatalf("stale launch update changed activity state = %q, want empty", got.ReviewerActivityState)
	}
	updated, err = s.UpdateReviewActivity(ctx, "rev-2", domain.ActivityBlocked, "legacy-native", "")
	if err != nil {
		t.Fatalf("empty launch update against claimed generation: %v", err)
	}
	if updated {
		t.Fatal("empty launch update unexpectedly succeeded against claimed generation")
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review after empty launch update: ok=%v err=%v", ok, err)
	}
	if got.ReviewerActivityState != "" || got.AgentSessionID != "reviewer-native-1" {
		t.Fatalf("empty launch update changed review = %+v", got)
	}
	updated, err = s.UpdateReviewActivity(ctx, "rev-2", domain.ActivityIdle, "", "launch-2")
	if err != nil || !updated {
		t.Fatalf("matching launch update: updated=%v err=%v", updated, err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get greptile review after matching launch update: ok=%v err=%v", ok, err)
	}
	if got.ReviewerActivityState != domain.ActivityIdle {
		t.Fatalf("matching launch update changed activity state = %q, want idle", got.ReviewerActivityState)
	}
	if err := s.ClearReviewerHandle(ctx, rec.ID); err != nil {
		t.Fatalf("clear reviewer handle: %v", err)
	}
	got, ok, err = s.GetReviewBySessionAndHarness(ctx, rec.ID, domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("get review after clear: ok=%v err=%v", ok, err)
	}
	if got.ReviewerHandleID != "" {
		t.Fatalf("reviewer handle after clear = %q, want empty", got.ReviewerHandleID)
	}

	// A run inserts running and updates to complete/changes_requested.
	if err := s.InsertReviewRun(ctx, domain.ReviewRun{
		ID: "run-1", ReviewID: got.ID, SessionID: rec.ID, BatchID: "batch-1", Harness: domain.ReviewerHarness("greptile"),
		PRURL: got.PRURL, TargetSHA: "sha1", Status: domain.ReviewRunRunning, Verdict: domain.VerdictNone,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictChangesRequested, "please fix", "rev-987", false); err != nil {
		t.Fatalf("update run: %v", err)
	} else if !ok {
		t.Fatal("update run: got ok=false")
	}

	gotRun, ok, err := s.GetReviewRun(ctx, "run-1")
	if err != nil || !ok {
		t.Fatalf("get run: ok=%v err=%v", ok, err)
	}
	if gotRun.ID != "run-1" || gotRun.SessionID != rec.ID || gotRun.BatchID != "batch-1" || gotRun.TargetSHA != "sha1" {
		t.Fatalf("get run = %+v", gotRun)
	}

	bySHA, ok, err := s.GetReviewRunBySessionPRAndSHA(ctx, rec.ID, got.PRURL, "sha1")
	if err != nil || !ok {
		t.Fatalf("by sha: ok=%v err=%v", ok, err)
	}
	if bySHA.Status != domain.ReviewRunComplete || bySHA.Verdict != domain.VerdictChangesRequested || bySHA.Body != "please fix" || bySHA.GithubReviewID != "rev-987" {
		t.Fatalf("run result not persisted: %+v", bySHA)
	}
	byHarness, ok, err := s.GetReviewRunBySessionPRSHAAndHarness(ctx, rec.ID, got.PRURL, "sha1", domain.ReviewerHarness("greptile"))
	if err != nil || !ok {
		t.Fatalf("by harness: ok=%v err=%v", ok, err)
	}
	if byHarness.ID != "run-1" {
		t.Fatalf("by harness = %+v, want run-1", byHarness)
	}
	if _, ok, _ := s.GetReviewRunBySessionPRSHAAndHarness(ctx, rec.ID, got.PRURL, "sha1", domain.ReviewerCodex); ok {
		t.Fatal("unexpected run for a different harness")
	}
	if _, ok, _ := s.GetReviewRunBySessionPRAndSHA(ctx, rec.ID, got.PRURL, "other"); ok {
		t.Fatal("unexpected run for a different sha")
	}

	runs, err := s.ListReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run-1" || runs[0].AutoInjectReview {
		t.Fatalf("list runs = %+v", runs)
	}
	batchRuns, err := s.ListReviewRunsByBatch(ctx, rec.ID, "batch-1")
	if err != nil {
		t.Fatalf("list batch runs: %v", err)
	}
	if len(batchRuns) != 1 || batchRuns[0].ID != "run-1" || batchRuns[0].BatchID != "batch-1" {
		t.Fatalf("batch runs = %+v", batchRuns)
	}

	if ok, err := s.UpdateReviewRunResult(ctx, "run-1", domain.ReviewRunComplete, domain.VerdictApproved, "again", "", true); err != nil {
		t.Fatalf("second update: %v", err)
	} else if ok {
		t.Fatal("second update completed an already-complete run")
	}
}

func TestCancelRunningReviewRunsBySession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	rec, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertReview(ctx, domain.Review{
		ID: "rev-1", SessionID: rec.ID, ProjectID: rec.ProjectID,
		Harness: domain.ReviewerCodex, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	for _, run := range []domain.ReviewRun{
		{ID: "run-1", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/1", TargetSHA: "sha1", Status: domain.ReviewRunRunning, CreatedAt: now},
		{ID: "run-2", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/2", TargetSHA: "sha2", Status: domain.ReviewRunRunning, CreatedAt: now.Add(time.Second)},
		{ID: "run-3", ReviewID: "rev-1", SessionID: rec.ID, Harness: domain.ReviewerCodex, PRURL: "https://example/pr/3", TargetSHA: "sha3", Status: domain.ReviewRunComplete, Verdict: domain.VerdictApproved, CreatedAt: now.Add(2 * time.Second)},
	} {
		if err := s.InsertReviewRun(ctx, run); err != nil {
			t.Fatalf("insert %s: %v", run.ID, err)
		}
	}
	running, err := s.ListRunningReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 2 || running[0].ID != "run-2" || running[1].ID != "run-1" {
		t.Fatalf("running = %+v", running)
	}
	n, err := s.CancelRunningReviewRunsBySession(ctx, rec.ID, "cancelled by user")
	if err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if n != 2 {
		t.Fatalf("cancelled rows = %d, want 2", n)
	}
	runs, err := s.ListReviewRunsBySession(ctx, rec.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	byID := map[string]domain.ReviewRun{}
	for _, run := range runs {
		byID[run.ID] = run
	}
	if byID["run-1"].Status != domain.ReviewRunCancelled || byID["run-2"].Status != domain.ReviewRunCancelled {
		t.Fatalf("running runs not cancelled: %+v", byID)
	}
	if byID["run-3"].Status != domain.ReviewRunComplete {
		t.Fatalf("complete run changed: %+v", byID["run-3"])
	}
}

func TestReviewGettersMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, ok, err := s.GetReviewBySession(ctx, "mer-1"); err != nil || ok {
		t.Fatalf("missing review: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetReviewRunBySessionPRAndSHA(ctx, "mer-1", "pr1", "sha1"); err != nil || ok {
		t.Fatalf("missing run: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.GetReviewRun(ctx, "run-missing"); err != nil || ok {
		t.Fatalf("missing run by id: ok=%v err=%v", ok, err)
	}
}
