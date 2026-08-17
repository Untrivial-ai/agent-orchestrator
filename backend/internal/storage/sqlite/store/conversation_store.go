package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Conversation persistence for Chat sessions.
//
// Two invariants live here rather than in the caller:
//
//   - Sequence allocation and the row that uses it commit together. Every write
//     that mints a position runs inside one transaction that bumps
//     conversations.latest_sequence and inserts the row, so two concurrent
//     writers cannot land on the same position.
//   - A projection and the raw provider event that produced it are written in the
//     same transaction. If the process dies between them, neither survives — so
//     the archive can never disagree with the timeline it explains.

// ErrConversationNotFound reports a lookup for a session that has none. It
// aliases the domain sentinel so a service can recognize it without importing the
// storage layer.
var ErrConversationNotFound = domain.ErrNoConversation

// ErrNoQueuedTurn reports an empty send queue. It is an ordinary outcome of
// draining, not a failure.
var ErrNoQueuedTurn = domain.ErrNoQueuedTurn

// ErrQueuedTurnNotAvailable means the selected turn cannot be reserved: it is
// absent from this conversation, no longer queued, or another promotion already
// owns it. The caller must not contact the provider after this outcome.
var ErrQueuedTurnNotAvailable = errors.New("queued turn is not available for promotion")

// CreateConversation opens a worker's session-scoped conversation or rebinds an
// orchestrator's project-scoped narrative to its current session. Returning the
// existing row makes controller restart and clean orchestrator replacement
// idempotent without splitting the project history.
func (s *Store) CreateConversation(
	ctx context.Context,
	id string,
	scope domain.ConversationScope,
	project domain.ProjectID,
	session domain.SessionID,
	now time.Time,
) (domain.ConversationRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if scope == domain.ConversationScopeProject {
		if existing, err := s.qw.SelectProjectConversation(ctx, project); err == nil {
			if err := s.qw.BindProjectConversationSession(ctx, gen.BindProjectConversationSessionParams{
				CurrentSessionID: &session,
				UpdatedAt:        now,
				ID:               existing.ID,
			}); err != nil {
				return domain.ConversationRecord{}, fmt.Errorf("bind project conversation %s to %s: %w", existing.ID, session, err)
			}
			existing.CurrentSessionID = &session
			existing.UpdatedAt = now
			return conversationToDomain(existing), nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return domain.ConversationRecord{}, fmt.Errorf("select project conversation for %s: %w", project, err)
		}
	} else {
		scope = domain.ConversationScopeSession
		if existing, err := s.qw.SelectConversationBySession(ctx, &session); err == nil {
			return conversationToDomain(existing), nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return domain.ConversationRecord{}, fmt.Errorf("select conversation for %s: %w", session, err)
		}
	}

	var ownerSession *domain.SessionID
	if scope == domain.ConversationScopeSession {
		ownerSession = &session
	}
	rootBranchID := id + ":root"
	err := s.inTx(ctx, "insert conversation", func(q *gen.Queries) error {
		owner, getErr := q.GetSession(ctx, session)
		if getErr != nil {
			return fmt.Errorf("select controller session %s: %w", session, getErr)
		}
		if insertErr := q.InsertConversation(ctx, gen.InsertConversationParams{
			ID:               id,
			Scope:            scope,
			ProjectID:        project,
			SessionID:        ownerSession,
			CurrentSessionID: &session,
			ActiveBranchID:   rootBranchID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}); insertErr != nil {
			return insertErr
		}
		return q.InsertConversationBranch(ctx, gen.InsertConversationBranchParams{
			ID:                     rootBranchID,
			ConversationID:         id,
			SessionID:              nullableString(string(session)),
			ProviderConversationID: owner.ProviderConversationID,
			ForkAfterSequence:      0,
			CreatedAt:              now,
		})
	})
	if err != nil {
		return domain.ConversationRecord{}, fmt.Errorf("insert conversation %s: %w", id, err)
	}

	return domain.ConversationRecord{
		ID:             id,
		Scope:          scope,
		ProjectID:      project,
		SessionID:      session,
		ActiveBranchID: rootBranchID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// CreateConversationBranch records a provider-thread child without changing the
// active controller. Activation is a separate transaction after the replacement
// provider controller is ready.
func (s *Store) CreateConversationBranch(
	ctx context.Context,
	branch domain.ConversationBranch,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.inTx(ctx, "insert conversation branch", func(q *gen.Queries) error {
		return insertConversationBranchTx(ctx, q, branch, now)
	})
}

func insertConversationBranchTx(
	ctx context.Context,
	q *gen.Queries,
	branch domain.ConversationBranch,
	now time.Time,
) error {
	conversation, err := q.SelectConversationByID(ctx, branch.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: conversation %s", domain.ErrNoConversationBranch, branch.ConversationID)
	}
	if err != nil {
		return fmt.Errorf("select conversation %s: %w", branch.ConversationID, err)
	}
	if branch.SessionID == "" && conversation.CurrentSessionID != nil {
		branch.SessionID = *conversation.CurrentSessionID
	}
	if branch.ParentBranchID != "" {
		if _, err := q.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{
			ConversationID: branch.ConversationID,
			BranchID:       branch.ParentBranchID,
		}); err != nil {
			return fmt.Errorf("parent branch %s is not in conversation %s: %w",
				branch.ParentBranchID, branch.ConversationID, err)
		}
	}
	for label, turnID := range map[string]string{
		"fork-after":  branch.ForkAfterTurnID,
		"replaced":    branch.ReplacedTurnID,
		"replacement": branch.ReplacementTurnID,
	} {
		if turnID == "" {
			continue
		}
		turn, turnErr := q.SelectConversationTurnByID(ctx, turnID)
		if turnErr != nil || turn.ConversationID != branch.ConversationID {
			if turnErr == nil {
				turnErr = ErrConversationTurnNotFound
			}
			return fmt.Errorf("%s turn %s is not in conversation %s: %w",
				label, turnID, branch.ConversationID, turnErr)
		}
	}
	if now.IsZero() {
		now = branch.CreatedAt
	}
	if err := q.InsertConversationBranch(ctx, gen.InsertConversationBranchParams{
		ID:                     branch.ID,
		ConversationID:         branch.ConversationID,
		SessionID:              nullableString(string(branch.SessionID)),
		ProviderConversationID: branch.ProviderConversationID,
		ParentBranchID:         nullableString(branch.ParentBranchID),
		ForkAfterTurnID:        nullableString(branch.ForkAfterTurnID),
		ReplacedTurnID:         nullableString(branch.ReplacedTurnID),
		ReplacementTurnID:      nullableString(branch.ReplacementTurnID),
		ForkAfterSequence:      branch.ForkAfterSequence,
		CreatedAt:              now,
	}); err != nil {
		return fmt.Errorf("insert conversation branch %s: %w", branch.ID, err)
	}
	return nil
}

// CreateAndActivateConversationBranch publishes a ready provider branch and the
// session controller generation in one transaction. The caller retains the
// fenced source controller until this commit succeeds.
func (s *Store) CreateAndActivateConversationBranch(
	ctx context.Context,
	sessionID domain.SessionID,
	branch domain.ConversationBranch,
	generation string,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.inTx(ctx, "create and activate conversation branch", func(q *gen.Queries) error {
		if err := insertConversationBranchTx(ctx, q, branch, now); err != nil {
			return err
		}
		conversationRows, err := q.ActivateConversationBranch(ctx, gen.ActivateConversationBranchParams{
			ActiveBranchID: branch.ID,
			UpdatedAt:      now,
			ID:             branch.ConversationID,
		})
		if err != nil {
			return fmt.Errorf("move conversation head: %w", err)
		}
		if conversationRows != 1 {
			return fmt.Errorf("move conversation head: conversation %s not found", branch.ConversationID)
		}
		sessionRows, err := q.ActivateConversationBranchSession(ctx, gen.ActivateConversationBranchSessionParams{
			ProviderConversationID: branch.ProviderConversationID,
			ControllerGeneration:   generation,
			UpdatedAt:              now,
			ID:                     sessionID,
		})
		if err != nil {
			return fmt.Errorf("move session controller: %w", err)
		}
		if sessionRows != 1 {
			return fmt.Errorf("move session controller: live chat session %s not found", sessionID)
		}
		return nil
	})
}

// ConversationBranch reads one branch and reports whether it is active.
func (s *Store) ConversationBranch(
	ctx context.Context,
	conversationID, branchID string,
) (domain.ConversationBranch, error) {
	row, err := s.qr.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{
		ConversationID: conversationID,
		BranchID:       branchID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationBranch{}, fmt.Errorf("%w: %s", domain.ErrNoConversationBranch, branchID)
	}
	if err != nil {
		return domain.ConversationBranch{}, fmt.Errorf("select conversation branch %s: %w", branchID, err)
	}
	return conversationBranchToDomain(row), nil
}

// ConversationBranches lists every durable sibling and descendant in creation
// order. Only the current head is marked active.
func (s *Store) ConversationBranches(
	ctx context.Context,
	conversationID string,
) ([]domain.ConversationBranch, error) {
	rows, err := s.qr.SelectConversationBranches(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("select conversation branches for %s: %w", conversationID, err)
	}
	branches := make([]domain.ConversationBranch, 0, len(rows))
	for _, row := range rows {
		branches = append(branches, conversationBranchListToDomain(row))
	}
	return branches, nil
}

// ConversationEditAnchor resolves the active-lineage fork point and preserves
// the original structured delivery content for the replacement send.
func (s *Store) ConversationEditAnchor(
	ctx context.Context,
	conversationID, replacedTurnID string,
) (domain.ConversationEditAnchor, error) {
	row, err := s.qr.SelectConversationEditAnchor(ctx, gen.SelectConversationEditAnchorParams{
		ConversationID: conversationID,
		ReplacedTurnID: nullableString(replacedTurnID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationEditAnchor{}, fmt.Errorf("%w: %s", ErrConversationTurnNotFound, replacedTurnID)
	}
	if err != nil {
		return domain.ConversationEditAnchor{}, fmt.Errorf("select conversation edit anchor %s: %w", replacedTurnID, err)
	}
	return domain.ConversationEditAnchor{
		ConversationID:              row.ConversationID,
		SourceBranchID:              row.SourceBranchID,
		ReplacedTurnID:              row.ReplacedTurnID.String,
		PreviousProviderTurnID:      row.PreviousProviderTurnID,
		ForkAfterSequence:           row.ForkAfterSequence,
		OriginalDeliveryContentJSON: row.OriginalDeliveryContentJson,
		RetryActiveBranch:           row.RetryActiveBranch.Valid && row.RetryActiveBranch.Bool,
	}, nil
}

// UpdateConversationBranchReplacement attaches the first turn created on a
// child branch to the source prompt it replaces.
func (s *Store) UpdateConversationBranchReplacement(
	ctx context.Context,
	branchID, replacementTurnID string,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.UpdateConversationBranchReplacement(ctx,
		gen.UpdateConversationBranchReplacementParams{
			ReplacementTurnID: nullableString(replacementTurnID),
			BranchID:          branchID,
		})
	if err != nil {
		return fmt.Errorf("update conversation branch %s replacement: %w", branchID, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %s", domain.ErrNoConversationBranch, branchID)
	}
	return nil
}

// ActivateConversationBranch moves the durable conversation head and the Chat
// controller's provider handle/generation together. If the session is not a live
// Chat session, the conversation update is rolled back.
func (s *Store) ActivateConversationBranch(
	ctx context.Context,
	sessionID domain.SessionID,
	conversationID, branchID, providerConversationID, generation string,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.inTx(ctx, "activate conversation branch", func(q *gen.Queries) error {
		conversation, err := q.SelectConversationByID(ctx, conversationID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: conversation %s", domain.ErrNoConversationBranch, conversationID)
		}
		if err != nil {
			return fmt.Errorf("select conversation %s: %w", conversationID, err)
		}
		if conversation.CurrentSessionID == nil || *conversation.CurrentSessionID != sessionID {
			return fmt.Errorf("conversation %s is not controlled by session %s", conversationID, sessionID)
		}
		branch, err := q.SelectConversationBranch(ctx, gen.SelectConversationBranchParams{
			ConversationID: conversationID,
			BranchID:       branchID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", domain.ErrNoConversationBranch, branchID)
		}
		if err != nil {
			return fmt.Errorf("select conversation branch %s: %w", branchID, err)
		}
		if branch.ProviderConversationID != providerConversationID {
			return fmt.Errorf("activate conversation branch %s: provider conversation %q does not match stored %q",
				branchID, providerConversationID, branch.ProviderConversationID)
		}
		conversationRows, err := q.ActivateConversationBranch(ctx, gen.ActivateConversationBranchParams{
			ActiveBranchID: branchID,
			UpdatedAt:      now,
			ID:             conversationID,
		})
		if err != nil {
			return fmt.Errorf("move conversation head: %w", err)
		}
		if conversationRows != 1 {
			return fmt.Errorf("move conversation head: conversation %s not found", conversationID)
		}
		sessionRows, err := q.ActivateConversationBranchSession(ctx,
			gen.ActivateConversationBranchSessionParams{
				ProviderConversationID: providerConversationID,
				ControllerGeneration:   generation,
				UpdatedAt:              now,
				ID:                     sessionID,
			})
		if err != nil {
			return fmt.Errorf("move session controller: %w", err)
		}
		if sessionRows != 1 {
			return fmt.Errorf("move session controller: live chat session %s not found", sessionID)
		}
		return nil
	})
}

// ConversationForSession looks up a session's conversation.
func (s *Store) ConversationForSession(
	ctx context.Context,
	session domain.SessionID,
) (domain.ConversationRecord, error) {
	row, err := s.qr.SelectConversationBySession(ctx, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationRecord{}, ErrConversationNotFound
	}
	if err != nil {
		return domain.ConversationRecord{}, fmt.Errorf("select conversation for %s: %w", session, err)
	}
	return conversationToDomain(row), nil
}

// AppendUserMessage records an inbound message and the turn it opens.
//
// Idempotent on clientMessageID: a retried send returns the message and turn that
// already exist instead of opening a second provider turn. The caller must not
// dispatch to the provider when `created` is false.
func (s *Store) AppendUserMessage(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	generation string,
	msg domain.ConversationMessage,
	turnID string,
	now time.Time,
) (created bool, err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if msg.ClientMessageID != "" {
		existing, lookupErr := s.qr.SelectConversationMessageByClientID(ctx,
			gen.SelectConversationMessageByClientIDParams{
				ConversationID:  conversationID,
				ClientMessageID: msg.ClientMessageID,
			})
		if lookupErr == nil {
			_ = existing
			return false, nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return false, fmt.Errorf("lookup client message %s: %w", msg.ClientMessageID, lookupErr)
		}
	}

	err = s.inTx(ctx, "append user message", func(q *gen.Queries) error {
		sequence, err := q.NextConversationSequence(ctx, gen.NextConversationSequenceParams{
			UpdatedAt: now,
			ID:        conversationID,
		})
		if err != nil {
			return fmt.Errorf("allocate sequence: %w", err)
		}

		if err := q.InsertConversationTurn(ctx, gen.InsertConversationTurnParams{
			ID:                   turnID,
			ConversationID:       conversationID,
			HandledBySessionID:   session,
			ControllerGeneration: generation,
			State:                domain.TurnStateQueued,
			RequestedAt:          now,
		}); err != nil {
			return fmt.Errorf("insert turn: %w", err)
		}

		return q.InsertConversationMessage(ctx, gen.InsertConversationMessageParams{
			ID:                  msg.ID,
			ConversationID:      conversationID,
			TurnID:              sql.NullString{String: turnID, Valid: true},
			Sequence:            sequence,
			Role:                domain.MessageRoleUser,
			Origin:              msg.Origin,
			Text:                msg.Text,
			ProviderItemID:      "",
			ClientMessageID:     msg.ClientMessageID,
			DeliveryContentJson: msg.DeliveryContentJSON,
			CreatedAt:           now,
			UpdatedAt:           now,
		})
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// AdoptProviderTurn records a turn the provider started that AO never dispatched.
//
// A compaction runs as its own provider turn, and so does work the provider
// resumes inside its own history. Without a row, every item those turns emit
// correlates to no turn: the activities lose their turn id and the timeline stops
// grouping them, which reads to a user as the conversation falling apart. Idempotent
// because the provider re-announces a turn on resume.
func (s *Store) AdoptProviderTurn(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	generation, turnID, providerTurnID string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.AdoptProviderConversationTurn(ctx, gen.AdoptProviderConversationTurnParams{
		ID:                   turnID,
		ConversationID:       conversationID,
		HandledBySessionID:   session,
		ProviderTurnID:       providerTurnID,
		ControllerGeneration: generation,
		RequestedAt:          now,
		StartedAt:            sql.NullTime{Time: now, Valid: true},
	}); err != nil {
		return fmt.Errorf("adopt provider turn %s: %w", providerTurnID, err)
	}
	return nil
}

// AppendImportedUserMessage records a user prompt recovered from the provider's
// native thread. The enclosing turn must already have been adopted from the
// history's turn.started event.
//
// Idempotency is by provider turn rather than only by clientMessageID. A prompt AO
// originally sent in Chat mode already belongs to that turn but may carry a
// different client id from the history adapter; checking the turn is what keeps a
// Chat -> TUI -> Chat round trip from duplicating it.
func (s *Store) AppendImportedUserMessage(
	ctx context.Context,
	conversationID, providerTurnID string,
	msg domain.ConversationMessage,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	_, err := q.SelectConversationUserMessageByTurn(ctx,
		gen.SelectConversationUserMessageByTurnParams{
			ConversationID: conversationID,
			ProviderTurnID: providerTurnID,
		})
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup imported user message for turn %s: %w", providerTurnID, err)
	}

	turn, err := q.SelectConversationTurnByProviderID(ctx,
		gen.SelectConversationTurnByProviderIDParams{
			ConversationID: conversationID,
			ProviderTurnID: providerTurnID,
		})
	if err != nil {
		return fmt.Errorf("lookup imported provider turn %s: %w", providerTurnID, err)
	}
	sequence, err := q.NextConversationSequence(ctx, gen.NextConversationSequenceParams{
		UpdatedAt: now,
		ID:        conversationID,
	})
	if err != nil {
		return fmt.Errorf("allocate imported user message sequence: %w", err)
	}
	if err := q.InsertConversationMessage(ctx, gen.InsertConversationMessageParams{
		ID:                  msg.ID,
		ConversationID:      conversationID,
		TurnID:              sql.NullString{String: turn.ID, Valid: true},
		Sequence:            sequence,
		Role:                domain.MessageRoleUser,
		Origin:              domain.MessageOriginHuman,
		Text:                msg.Text,
		ProviderItemID:      msg.ProviderItemID,
		ClientMessageID:     msg.ClientMessageID,
		DeliveryContentJson: msg.DeliveryContentJSON,
		CreatedAt:           now,
		UpdatedAt:           now,
	}); err != nil {
		return fmt.Errorf("insert imported user message for turn %s: %w", providerTurnID, err)
	}
	return nil
}

// BindTurnToProvider records the provider's turn id once a send is accepted and
// marks the turn running.
func (s *Store) BindTurnToProvider(ctx context.Context, turnID, providerTurnID string, now time.Time) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.BindConversationTurnProviderID(ctx, gen.BindConversationTurnProviderIDParams{
		ProviderTurnID: providerTurnID,
		StartedAt:      sql.NullTime{Time: now, Valid: true},
		ID:             turnID,
	}); err != nil {
		return fmt.Errorf("bind turn %s to provider turn %s: %w", turnID, providerTurnID, err)
	}
	if err := q.MarkConversationTurnStarted(ctx, gen.MarkConversationTurnStartedParams{
		StartedAt: sql.NullTime{Time: now, Valid: true},
		ID:        turnID,
	}); err != nil {
		return fmt.Errorf("mark turn %s started: %w", turnID, err)
	}
	return nil
}

// SettleTurn records a turn's terminal state. An interrupted turn is not an
// error and carries no message.
func (s *Store) SettleTurn(
	ctx context.Context,
	conversationID, providerTurnID string,
	state domain.TurnState,
	errMessage string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	turn, err := q.SelectConversationTurnByProviderID(ctx,
		gen.SelectConversationTurnByProviderIDParams{
			ConversationID: conversationID,
			ProviderTurnID: providerTurnID,
		})
	if errors.Is(err, sql.ErrNoRows) {
		// A turn AO never recorded, e.g. one started by a previous controller
		// before a restart. Not an error: there is nothing to settle.
		return nil
	}
	if err != nil {
		return fmt.Errorf("select turn %s: %w", providerTurnID, err)
	}

	if err := q.SettleConversationTurn(ctx, gen.SettleConversationTurnParams{
		State:        state,
		ErrorMessage: errMessage,
		CompletedAt:  sql.NullTime{Time: now, Valid: true},
		ID:           turn.ID,
	}); err != nil {
		return fmt.Errorf("settle turn %s: %w", turn.ID, err)
	}
	if err := q.SettleRunningConversationActivitiesForTurn(ctx,
		gen.SettleRunningConversationActivitiesForTurnParams{
			Status:         terminalActivityStatus(state),
			UpdatedAt:      now,
			ConversationID: conversationID,
			TurnID:         sql.NullString{String: turn.ID, Valid: true},
		}); err != nil {
		return fmt.Errorf("settle running activities for turn %s: %w", turn.ID, err)
	}
	return nil
}

// terminalActivityStatus mirrors why the enclosing turn ended. Interrupted work
// is neither successful nor failed: it stopped because the user asked it to.
func terminalActivityStatus(state domain.TurnState) domain.ActivityStatus {
	switch state {
	case domain.TurnStateCompleted:
		return domain.ActivityStatusCompleted
	case domain.TurnStateInterrupted:
		return domain.ActivityStatusCancelled
	default:
		return domain.ActivityStatusFailed
	}
}

// SettleOrphanedTurns marks anything a dead controller left in flight. A turn the
// controller was running is not evidence the work finished, so it settles as
// failed rather than silently completing.
func (s *Store) SettleOrphanedTurns(ctx context.Context, session domain.SessionID, now time.Time) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.FailOrphanedConversationActivities(ctx,
		gen.FailOrphanedConversationActivitiesParams{
			UpdatedAt:          now,
			HandledBySessionID: session,
		}); err != nil {
		return fmt.Errorf("settle orphaned activities for %s: %w", session, err)
	}
	if err := q.SettleOrphanedConversationTurns(ctx,
		gen.SettleOrphanedConversationTurnsParams{
			CompletedAt:        sql.NullTime{Time: now, Valid: true},
			HandledBySessionID: session,
		}); err != nil {
		return fmt.Errorf("settle orphaned turns for %s: %w", session, err)
	}
	return nil
}

// ListVisibleRunningTurnProviderIDs returns the same active-branch running turns,
// in the same order, that a conversation snapshot exposes to clients. Interrupt
// uses the full set because a root and nested provider turn may overlap; settling
// only one would leave the UI's Working state behind.
func (s *Store) ListVisibleRunningTurnProviderIDs(
	ctx context.Context,
	conversationID string,
) ([]string, error) {
	providerTurnIDs, err := s.qr.ListVisibleRunningTurnsForConversation(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list visible running turns for %s: %w", conversationID, err)
	}
	return providerTurnIDs, nil
}

// SetConversationSettings records the provider choices for the next turn.
//
// An empty field is stored as NULL rather than as an empty string, so "the user
// cleared this" and "the user never chose" stay the same thing: fall back to the
// provider's default.
func (s *Store) SetConversationSettings(
	ctx context.Context,
	conversationID string,
	settings domain.ConversationSettings,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.UpdateConversationTurnSettings(ctx, gen.UpdateConversationTurnSettingsParams{
		Model:           nullableString(settings.Model),
		ReasoningEffort: nullableString(settings.ReasoningEffort),
		ApprovalMode:    nullableString(string(settings.ApprovalMode)),
		UpdatedAt:       now,
		ID:              conversationID,
	}); err != nil {
		return fmt.Errorf("set conversation settings for %s: %w", conversationID, err)
	}
	return nil
}

// MarkCompacted records that the conversation's history was summarized to reclaim
// context.
//
// Separate from the timeline row the caller writes alongside it, and deliberately
// not folded into UpsertActivity: this is conversation state ("has compaction ever
// run"), the activity is the narrative ("here is where it happened"), and a
// generic activity writer has no business knowing which kinds move a column on the
// conversation.
func (s *Store) MarkCompacted(ctx context.Context, conversationID string, at time.Time) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.MarkConversationCompacted(ctx, gen.MarkConversationCompactedParams{
		CompactedAt: sql.NullTime{Time: at, Valid: true},
		UpdatedAt:   at,
		ID:          conversationID,
	}); err != nil {
		return fmt.Errorf("mark conversation %s compacted: %w", conversationID, err)
	}
	return nil
}

func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

// RecordUsage stores the conversation's current token position, overwriting the
// previous one.
//
// Latest-wins on purpose. The provider reports usage after every tool call, and
// the history of those reports is not information anyone wants: the question is
// "how full is this conversation now", which has exactly one answer.
func (s *Store) RecordUsage(
	ctx context.Context,
	conversationID string,
	usage domain.ConversationUsage,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationUsage(ctx, gen.UpdateConversationUsageParams{
		ContextUsed:       nullablePositive(usage.ContextUsed),
		ContextWindow:     nullablePositive(usage.ContextWindow),
		UsageInputTokens:  nullablePositive(usage.InputTokens),
		UsageOutputTokens: nullablePositive(usage.OutputTokens),
		UsageCachedTokens: nullablePositive(usage.CachedTokens),
		UsageTotalTokens:  nullablePositive(usage.TotalTokens),
		UsageCost:         nullableCost(usage.Cost),
		UsageCurrency:     nullableString(usage.Currency),
		ID:                conversationID,
	}); err != nil {
		return fmt.Errorf("record usage for %s: %w", conversationID, err)
	}
	return nil
}

// RecordRateLimits stores the account's current quota position, overwriting the
// previous one.
func (s *Store) RecordRateLimits(
	ctx context.Context,
	conversationID string,
	limits domain.ConversationRateLimits,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationRateLimits(ctx, gen.UpdateConversationRateLimitsParams{
		RateLimitPrimaryPercent:    nullablePercent(limits.PrimaryUsedPercent),
		RateLimitSecondaryPercent:  nullablePercent(limits.SecondaryUsedPercent),
		RateLimitPrimaryResetsIn:   nullablePositive(limits.PrimaryResetsInSeconds),
		RateLimitSecondaryResetsIn: nullablePositive(limits.SecondaryResetsInSeconds),
		RateLimitPlan:              nullableString(limits.PlanLabel),
		ID:                         conversationID,
	}); err != nil {
		return fmt.Errorf("record rate limits for %s: %w", conversationID, err)
	}
	return nil
}

// nullablePositive stores zero as NULL. For every column that uses it, zero and
// unreported are the same thing to a reader, and NULL is the honest encoding of
// "the provider never said".
func nullablePositive(value int64) sql.NullInt64 {
	if value <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: value, Valid: true}
}

// nullablePercent stores an unreported window as NULL. Negative is the port's
// "not reported" signal, and it must not be written as a real percentage: a
// reader seeing -1 as data would draw a meter running backwards.
func nullablePercent(value float64) sql.NullFloat64 {
	if value < 0 {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: value, Valid: true}
}

// RecordModelReroute stores the provider answering with a model other than the one
// asked for, overwriting the previous report.
//
// Latest wins because there is one current answer to "which model is actually
// replying". The earlier reroutes of a long conversation are not lost information a
// reader wants: the turn each one happened on is recorded with it, and what a client
// needs to say is what is answering now.
func (s *Store) RecordModelReroute(
	ctx context.Context,
	conversationID string,
	reroute domain.ConversationModelReroute,
) error {
	encoded, err := json.Marshal(reroute)
	if err != nil {
		return fmt.Errorf("encode model reroute for %s: %w", conversationID, err)
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationModelReroute(ctx,
		gen.UpdateConversationModelRerouteParams{
			ModelRerouteJson: sql.NullString{String: string(encoded), Valid: true},
			UpdatedAt:        reroute.At,
			ID:               conversationID,
		}); err != nil {
		return fmt.Errorf("record model reroute for %s: %w", conversationID, err)
	}
	return nil
}

// RecordAccount stores what the provider says about the account this conversation
// runs under.
//
// The caller passes the whole account, not a patch: the provider reports auth mode
// and plan together on account/updated and neither on a credential demand, so
// merging is the caller's job and it needs the previous value to do it. Doing it
// here would need a read inside the write lock for a field nothing queries.
func (s *Store) RecordAccount(
	ctx context.Context,
	conversationID string,
	account domain.ConversationAccount,
	now time.Time,
) error {
	encoded, err := json.Marshal(account)
	if err != nil {
		return fmt.Errorf("encode account for %s: %w", conversationID, err)
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationAccount(ctx, gen.UpdateConversationAccountParams{
		AccountJson: sql.NullString{String: string(encoded), Valid: true},
		UpdatedAt:   now,
		ID:          conversationID,
	}); err != nil {
		return fmt.Errorf("record account for %s: %w", conversationID, err)
	}
	return nil
}

// RecordThreadState stores the provider's lifecycle view of the thread.
//
// updated_at is deliberately not bumped by the statement this runs: thread status
// flips active/idle on every turn, and marking the conversation modified twice per
// message would make every chat session look freshly active forever.
func (s *Store) RecordThreadState(
	ctx context.Context,
	conversationID string,
	state domain.ConversationThreadState,
) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode thread state for %s: %w", conversationID, err)
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationThreadState(ctx,
		gen.UpdateConversationThreadStateParams{
			ThreadStateJson: sql.NullString{String: string(encoded), Valid: true},
			ID:              conversationID,
		}); err != nil {
		return fmt.Errorf("record thread state for %s: %w", conversationID, err)
	}
	return nil
}

// RecordMCPServers stores the tool servers' startup state.
//
// The whole list is written, not one server: the provider reports servers one at a
// time and re-reports all of them on every turn, so the caller merges by name and
// this stores the result. An empty list is stored as an empty array rather than
// NULL, because "AO asked and there are none" is a different answer from "AO never
// asked".
func (s *Store) RecordMCPServers(
	ctx context.Context,
	conversationID string,
	servers []domain.ConversationMCPServer,
) error {
	if servers == nil {
		servers = []domain.ConversationMCPServer{}
	}
	encoded, err := json.Marshal(servers)
	if err != nil {
		return fmt.Errorf("encode mcp servers for %s: %w", conversationID, err)
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationMcpServers(ctx,
		gen.UpdateConversationMcpServersParams{
			McpServersJson: sql.NullString{String: string(encoded), Valid: true},
			ID:             conversationID,
		}); err != nil {
		return fmt.Errorf("record mcp servers for %s: %w", conversationID, err)
	}
	return nil
}

// SetTurnPlan overwrites a turn's plan and reports whether the turn was found.
//
// A plan for a turn AO never recorded happens after a restart, when a controller
// reattaches to a provider turn that predates it. There is nothing to update, and
// that is not a failure.
func (s *Store) SetTurnPlan(
	ctx context.Context,
	conversationID, providerTurnID string,
	plan domain.ConversationPlan,
) (bool, error) {
	if providerTurnID == "" {
		// Without a turn id there is no row to attribute this to, and guessing the
		// most recent turn would file one turn's plan under another.
		return false, nil
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return false, fmt.Errorf("encode plan for %s: %w", providerTurnID, err)
	}

	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	rows, err := q.UpdateConversationTurnPlan(ctx, gen.UpdateConversationTurnPlanParams{
		PlanJson:       string(encoded),
		ConversationID: conversationID,
		ProviderTurnID: providerTurnID,
	})
	if err != nil {
		return false, fmt.Errorf("set turn plan for %s: %w", providerTurnID, err)
	}
	return rows > 0, nil
}

// MaxStreamedTextChars caps the provider prose stored for one activity.
//
// Reasoning on a long turn is the case this bounds: a model reasoning at high
// effort can emit tens of kilobytes of summary for one item, and this row is
// re-read by every conversation snapshot poll. 32K is well past what a collapsed
// card shows and past what anyone reads; half the command-output cap because
// reasoning has a settled restatement to fall back on and a program's output does
// not.
const MaxStreamedTextChars = 32 * 1024

// AppendActivityStreamedText folds a streamed provider-prose delta into its
// activity.
//
// Reports whether a row was found. A delta for an activity AO has not recorded is
// an ordinary race, not a failure: the provider can emit a reasoning delta before
// the item/started that creates the row, and the settled summary on item/completed
// still lands.
func (s *Store) AppendActivityStreamedText(
	ctx context.Context,
	conversationID, providerItemID, delta string,
	now time.Time,
) (bool, error) {
	if delta == "" {
		return false, nil
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	rows, err := q.AppendConversationActivityStreamedText(ctx,
		gen.AppendConversationActivityStreamedTextParams{
			Delta:          delta,
			MaxTextChars:   MaxStreamedTextChars,
			UpdatedAt:      now,
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if err != nil {
		return false, fmt.Errorf("append streamed text to %s: %w", providerItemID, err)
	}
	if rows > 0 {
		return true, nil
	}
	found, err := q.ConversationActivityExistsForProviderItem(ctx,
		gen.ConversationActivityExistsForProviderItemParams{
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if err != nil {
		return false, fmt.Errorf("find streamed text activity %s after no-op append: %w", providerItemID, err)
	}
	return found, nil
}

// SettleActivityStreamedText replaces streamed prose with the provider's settled
// version, and reports whether the activity was there to update.
//
// Replacing rather than appending is what makes a dropped delta cosmetic: the
// accumulated text is a preview of the settled text, so once the settled form
// arrives it is the only account worth keeping.
func (s *Store) SettleActivityStreamedText(
	ctx context.Context,
	conversationID, providerItemID, text string,
	now time.Time,
) (bool, error) {
	if providerItemID == "" {
		return false, nil
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	rows, err := q.SettleConversationActivityStreamedText(ctx,
		gen.SettleConversationActivityStreamedTextParams{
			StreamedText:   text,
			UpdatedAt:      now,
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if err != nil {
		return false, fmt.Errorf("settle streamed text on %s: %w", providerItemID, err)
	}
	return rows > 0, nil
}

// NextQueuedTurn returns the oldest message recorded while the agent was busy,
// or ErrNoQueuedTurn when the queue is empty.
//
// The queue is these rows, not a slice in a controller: a message the user typed
// is durable before it is delivered, so a daemon that dies with one queued can
// still account for it.
func (s *Store) NextQueuedTurn(ctx context.Context, conversationID string) (domain.QueuedTurn, error) {
	row, err := s.qr.SelectNextQueuedConversationTurn(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.QueuedTurn{}, ErrNoQueuedTurn
	}
	if err != nil {
		return domain.QueuedTurn{}, fmt.Errorf("select next queued turn for %s: %w", conversationID, err)
	}
	return domain.QueuedTurn{
		TurnID:              row.ID,
		Text:                row.Text,
		ClientMessageID:     row.ClientMessageID,
		Origin:              row.Origin,
		DeliveryContentJSON: row.DeliveryContentJson,
	}, nil
}

// ReserveQueuedTurnForPromotion atomically removes one selected queued turn from
// automatic drain and returns the durable content that must be steered.
func (s *Store) ReserveQueuedTurnForPromotion(
	ctx context.Context,
	conversationID, turnID string,
	now time.Time,
) (domain.QueuedTurn, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ReserveQueuedConversationTurnForPromotion(ctx,
		gen.ReserveQueuedConversationTurnForPromotionParams{
			PromotionStartedAt: sql.NullTime{Time: now, Valid: true},
			ID:                 turnID, ConversationID: conversationID,
		})
	if err != nil {
		return domain.QueuedTurn{}, fmt.Errorf("reserve queued turn %s: %w", turnID, err)
	}
	if rows == 0 {
		return domain.QueuedTurn{}, fmt.Errorf("%w: %s", ErrQueuedTurnNotAvailable, turnID)
	}
	row, err := s.qw.SelectReservedConversationTurnForPromotion(ctx,
		gen.SelectReservedConversationTurnForPromotionParams{ID: turnID, ConversationID: conversationID})
	if err != nil {
		// Do not strand a reservation when its message row is corrupt or absent.
		_, _ = s.qw.ReleaseQueuedConversationTurnPromotion(ctx,
			gen.ReleaseQueuedConversationTurnPromotionParams{ID: turnID, ConversationID: conversationID})
		return domain.QueuedTurn{}, fmt.Errorf("load reserved queued turn %s: %w", turnID, err)
	}
	return domain.QueuedTurn{
		TurnID: row.ID, Text: row.Text, ClientMessageID: row.ClientMessageID,
		Origin: row.Origin, DeliveryContentJSON: row.DeliveryContentJson,
	}, nil
}

// ReleaseQueuedTurnPromotion restores a provider-refused reservation without
// changing its immutable queue order.
func (s *Store) ReleaseQueuedTurnPromotion(
	ctx context.Context,
	conversationID, turnID string,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ReleaseQueuedConversationTurnPromotion(ctx,
		gen.ReleaseQueuedConversationTurnPromotionParams{ID: turnID, ConversationID: conversationID})
	if err != nil {
		return fmt.Errorf("release queued turn promotion %s: %w", turnID, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: %s", ErrQueuedTurnNotAvailable, turnID)
	}
	return nil
}

// CompleteQueuedTurnPromotion records the visible steer and retires its queued
// source together, so snapshots can never show both copies or neither copy.
func (s *Store) CompleteQueuedTurnPromotion(
	ctx context.Context,
	conversationID, sourceTurnID, providerTurnID string,
	activity domain.ConversationActivity,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "complete queued turn promotion", func(q *gen.Queries) error {
		target, err := q.SelectConversationTurnByProviderID(ctx,
			gen.SelectConversationTurnByProviderIDParams{
				ConversationID: conversationID, ProviderTurnID: providerTurnID,
			})
		if err != nil {
			return fmt.Errorf("select promotion target %s: %w", providerTurnID, err)
		}
		txCtx := context.WithValue(ctx, conversationProjectionTxKey{}, q)
		if err := s.UpsertActivity(txCtx, conversationID, providerTurnID, activity, now); err != nil {
			return err
		}
		rows, err := q.CompleteQueuedConversationTurnPromotion(ctx,
			gen.CompleteQueuedConversationTurnPromotionParams{
				CompletedAt:      sql.NullTime{Time: now, Valid: true},
				PromotedToTurnID: sql.NullString{String: target.ID, Valid: true},
				ID:               sourceTurnID,
				ConversationID:   conversationID,
			})
		if err != nil {
			return fmt.Errorf("complete source turn %s: %w", sourceTurnID, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: %s", ErrQueuedTurnNotAvailable, sourceTurnID)
		}
		return nil
	})
}

// CancelQueuedTurns closes out everything queued at or before cutoff.
//
// They settle as interrupted rather than failed: nothing went wrong, the user
// stopped the agent, and a message that was never dispatched did not fail. The
// cutoff keeps a message typed after the stop out of a cancellation it predates.
func (s *Store) CancelQueuedTurns(
	ctx context.Context,
	conversationID string,
	cutoff, now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.CancelQueuedConversationTurns(ctx, gen.CancelQueuedConversationTurnsParams{
		CompletedAt:    sql.NullTime{Time: now, Valid: true},
		ConversationID: conversationID,
		RequestedAt:    cutoff,
	}); err != nil {
		return fmt.Errorf("cancel queued turns for %s: %w", conversationID, err)
	}
	return nil
}

// CancelAllQueuedTurns closes the whole durable queue after an interface
// handoff has synchronously fenced intake and promotion. Unlike an ordinary Stop
// action, there is no post-request message to preserve with a timestamp cutoff.
func (s *Store) CancelAllQueuedTurns(
	ctx context.Context,
	conversationID string,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.CancelAllQueuedConversationTurns(ctx, gen.CancelAllQueuedConversationTurnsParams{
		CompletedAt:    sql.NullTime{Time: now, Valid: true},
		ConversationID: conversationID,
	}); err != nil {
		return fmt.Errorf("cancel all queued turns for %s: %w", conversationID, err)
	}
	return nil
}

// SettleTurnByID records a terminal state for a turn AO can name directly.
//
// Needed for a turn that never reached the provider: it has no provider turn id,
// so it cannot be found the way a running turn is. Settling those by the empty
// provider id would match an arbitrary undispatched turn instead of this one.
func (s *Store) SettleTurnByID(
	ctx context.Context,
	turnID string,
	state domain.TurnState,
	errMessage string,
	now time.Time,
) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.SettleConversationTurn(ctx, gen.SettleConversationTurnParams{
		State:        state,
		ErrorMessage: errMessage,
		CompletedAt:  sql.NullTime{Time: now, Valid: true},
		ID:           turnID,
	}); err != nil {
		return fmt.Errorf("settle turn %s: %w", turnID, err)
	}
	return nil
}

// AppendAssistantDelta folds a streaming delta into its message, creating the
// message on first sight. The provider item id is the correlation key because AO
// does not choose the provider's message identity.
func (s *Store) AppendAssistantDelta(
	ctx context.Context,
	conversationID, providerItemID, providerTurnID, delta, messageID string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	_, err := q.SelectConversationMessageByProviderItem(ctx,
		gen.SelectConversationMessageByProviderItemParams{
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	switch {
	case err == nil:
		if appendErr := q.AppendConversationMessageDelta(ctx,
			gen.AppendConversationMessageDeltaParams{
				Text:           delta,
				UpdatedAt:      now,
				ConversationID: conversationID,
				ProviderItemID: providerItemID,
			}); appendErr != nil {
			return fmt.Errorf("append delta to %s: %w", providerItemID, appendErr)
		}
		return nil

	case errors.Is(err, sql.ErrNoRows):
		turnID := s.turnIDFor(ctx, conversationID, providerTurnID)
		return s.inTx(ctx, "insert assistant message", func(q *gen.Queries) error {
			sequence, seqErr := q.NextConversationSequence(ctx, gen.NextConversationSequenceParams{
				UpdatedAt: now,
				ID:        conversationID,
			})
			if seqErr != nil {
				return fmt.Errorf("allocate sequence: %w", seqErr)
			}
			return q.InsertConversationMessage(ctx, gen.InsertConversationMessageParams{
				ID:             messageID,
				ConversationID: conversationID,
				TurnID:         turnID,
				Sequence:       sequence,
				Role:           domain.MessageRoleAssistant,
				Origin:         domain.MessageOriginProvider,
				Text:           delta,
				Streaming:      1,
				ProviderItemID: providerItemID,
				CreatedAt:      now,
				UpdatedAt:      now,
			})
		})

	default:
		return fmt.Errorf("lookup message %s: %w", providerItemID, err)
	}
}

// SettleAssistantMessage replaces streamed text with the provider's settled text
// and stops the streaming marker.
func (s *Store) SettleAssistantMessage(
	ctx context.Context,
	conversationID, providerItemID, providerTurnID, text, messageID string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	_, err := q.SelectConversationMessageByProviderItem(ctx,
		gen.SelectConversationMessageByProviderItemParams{
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if errors.Is(err, sql.ErrNoRows) {
		// A message whose deltas AO never saw — possible if it completed inside a
		// reconnect window. Record it whole rather than dropping it.
		turnID := s.turnIDFor(ctx, conversationID, providerTurnID)
		return s.inTx(ctx, "insert assistant message", func(q *gen.Queries) error {
			sequence, seqErr := q.NextConversationSequence(ctx, gen.NextConversationSequenceParams{
				UpdatedAt: now,
				ID:        conversationID,
			})
			if seqErr != nil {
				return fmt.Errorf("allocate sequence: %w", seqErr)
			}
			return q.InsertConversationMessage(ctx, gen.InsertConversationMessageParams{
				ID:             messageID,
				ConversationID: conversationID,
				TurnID:         turnID,
				Sequence:       sequence,
				Role:           domain.MessageRoleAssistant,
				Origin:         domain.MessageOriginProvider,
				Text:           text,
				ProviderItemID: providerItemID,
				CreatedAt:      now,
				UpdatedAt:      now,
			})
		})
	}
	if err != nil {
		return fmt.Errorf("lookup message %s: %w", providerItemID, err)
	}

	if err := q.SettleConversationMessage(ctx, gen.SettleConversationMessageParams{
		Text:           text,
		UpdatedAt:      now,
		ConversationID: conversationID,
		ProviderItemID: providerItemID,
	}); err != nil {
		return fmt.Errorf("settle message %s: %w", providerItemID, err)
	}
	return nil
}

// UpsertActivity records or updates a non-message timeline entry.
func (s *Store) UpsertActivity(
	ctx context.Context,
	conversationID, providerTurnID string,
	activity domain.ConversationActivity,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	detail := string(activity.Detail)

	if activity.ProviderItemID != "" {
		_, err := q.SelectConversationActivityByProviderItem(ctx,
			gen.SelectConversationActivityByProviderItemParams{
				ConversationID: conversationID,
				ProviderItemID: activity.ProviderItemID,
			})
		if err == nil {
			if settleErr := q.SettleConversationActivity(ctx,
				gen.SettleConversationActivityParams{
					Status:         activity.Status,
					Summary:        activity.Summary,
					DetailJson:     detail,
					UpdatedAt:      now,
					ConversationID: conversationID,
					ProviderItemID: activity.ProviderItemID,
				}); settleErr != nil {
				return fmt.Errorf("settle activity %s: %w", activity.ProviderItemID, settleErr)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lookup activity %s: %w", activity.ProviderItemID, err)
		}
	}

	turnID := s.turnIDFor(ctx, conversationID, providerTurnID)
	return s.inTx(ctx, "insert activity", func(q *gen.Queries) error {
		sequence, seqErr := q.NextConversationSequence(ctx, gen.NextConversationSequenceParams{
			UpdatedAt: now,
			ID:        conversationID,
		})
		if seqErr != nil {
			return fmt.Errorf("allocate sequence: %w", seqErr)
		}
		return q.InsertConversationActivity(ctx, gen.InsertConversationActivityParams{
			ID:             activity.ID,
			ConversationID: conversationID,
			TurnID:         turnID,
			Sequence:       sequence,
			Kind:           activity.Kind,
			Status:         activity.Status,
			Summary:        activity.Summary,
			DetailJson:     detail,
			RequestID:      activity.RequestID,
			ProviderItemID: activity.ProviderItemID,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	})
}

// MaxCommandOutputChars caps the streamed output stored for one command.
//
// A command that prints without bound (a build, a test run with verbose logging, a
// `yes`) must not put megabytes into a row that every conversation snapshot reads.
// 64K is comfortably more than a person reads in a timeline card and is where the
// full record belongs to a shell in the worktree instead.
//
// Exported so the truncation boundary is testable and so a caller can state the
// limit rather than restating a magic number.
const MaxCommandOutputChars = 64 * 1024

// AppendCommandOutput folds a streamed output delta into its activity.
//
// Reports whether a row was found. A delta for an activity AO has not recorded is
// an ordinary race, not a failure: the provider can emit a delta before the
// item/started that creates the row, and the aggregate on item/completed still
// lands. Silently dropping it is correct; claiming an error is not.
func (s *Store) AppendCommandOutput(
	ctx context.Context,
	conversationID, providerItemID, delta string,
	now time.Time,
) (bool, error) {
	if delta == "" {
		return false, nil
	}
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	rows, err := q.AppendConversationActivityOutput(ctx,
		gen.AppendConversationActivityOutputParams{
			Delta:          delta,
			MaxOutputChars: MaxCommandOutputChars,
			UpdatedAt:      now,
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if err != nil {
		return false, fmt.Errorf("append command output to %s: %w", providerItemID, err)
	}
	if rows > 0 {
		return true, nil
	}
	found, err := q.ConversationActivityExistsForProviderItem(ctx,
		gen.ConversationActivityExistsForProviderItemParams{
			ConversationID: conversationID,
			ProviderItemID: providerItemID,
		})
	if err != nil {
		return false, fmt.Errorf("find command output activity %s after no-op append: %w", providerItemID, err)
	}
	return found, nil
}

// SetTurnDiff overwrites a turn's changed-file summary.
//
// Reports whether the turn was found. A diff for a turn AO never recorded happens
// after a restart, when a controller reattaches to a provider turn that predates
// it, and there is nothing to update.
func (s *Store) SetTurnDiff(
	ctx context.Context,
	conversationID, providerTurnID string,
	diff domain.ConversationTurnDiff,
	now time.Time,
) (bool, error) {
	if providerTurnID == "" {
		// Without a turn id there is no row to attribute this to, and guessing the
		// most recent turn would attach one turn's edits to another.
		return false, nil
	}
	encoded, err := json.Marshal(diff)
	if err != nil {
		return false, fmt.Errorf("encode turn diff for %s: %w", providerTurnID, err)
	}

	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	rows, err := q.UpdateConversationTurnDiff(ctx, gen.UpdateConversationTurnDiffParams{
		DiffJson:       string(encoded),
		ConversationID: conversationID,
		ProviderTurnID: providerTurnID,
	})
	if err != nil {
		return false, fmt.Errorf("set turn diff for %s: %w", providerTurnID, err)
	}
	return rows > 0, nil
}

// ResolveApproval marks an approval answered. It only matches a still-pending
// row, so a card the user left on screen cannot answer a newer request.
func (s *Store) ResolveApproval(
	ctx context.Context,
	conversationID, requestID, detailJSON string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.ResolveConversationApproval(ctx, gen.ResolveConversationApprovalParams{
		DetailJson:     detailJSON,
		UpdatedAt:      now,
		ConversationID: conversationID,
		RequestID:      requestID,
	}); err != nil {
		return fmt.Errorf("resolve approval %s: %w", requestID, err)
	}
	return nil
}

// FailPendingApprovals closes out anything the user can no longer answer, because
// the provider call it was blocking is gone.
func (s *Store) FailPendingApprovals(ctx context.Context, conversationID string, now time.Time) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.FailPendingConversationApprovals(ctx,
		gen.FailPendingConversationApprovalsParams{
			UpdatedAt:      now,
			ConversationID: conversationID,
		}); err != nil {
		return fmt.Errorf("fail pending approvals for %s: %w", conversationID, err)
	}
	return nil
}

// FailPendingInputs closes structured input requests after the provider call
// waiting for them has gone away. Keeping this separate from approvals preserves
// the activity-kind boundary even though both use the same pending/resolved state
// machine.
func (s *Store) FailPendingInputs(ctx context.Context, conversationID string, now time.Time) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.FailPendingConversationInputs(ctx,
		gen.FailPendingConversationInputsParams{
			UpdatedAt:      now,
			ConversationID: conversationID,
		}); err != nil {
		return fmt.Errorf("fail pending inputs for %s: %w", conversationID, err)
	}
	return nil
}

// ProjectProviderEvent commits a raw provider event and the durable projection
// derived from it together. A real provider event identity deduplicates the
// entire unit; an empty identity is deliberately never deduplicated.
func (s *Store) ProjectProviderEvent(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	generation, providerEventID, method, payloadJSON string,
	now time.Time,
	project func(context.Context) error,
) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin provider event projection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := s.qw.WithTx(tx)
	owner, err := q.GetSession(ctx, session)
	if err != nil {
		return false, fmt.Errorf("read controller generation for %s: %w", session, err)
	}
	if owner.ControllerGeneration != generation {
		return false, nil
	}
	inserted, err := q.InsertConversationProviderEvent(ctx, gen.InsertConversationProviderEventParams{
		ConversationID:  conversationID,
		SessionID:       session,
		ProviderEventID: providerEventID,
		Method:          method,
		PayloadJson:     payloadJSON,
		ReceivedAt:      now,
	})
	if err != nil {
		return false, fmt.Errorf("archive provider event %s: %w", method, err)
	}
	if inserted == 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit duplicate provider event %s: %w", method, err)
		}
		return false, nil
	}
	txCtx := context.WithValue(ctx, conversationProjectionTxKey{}, q)
	if err := project(txCtx); err != nil {
		return false, fmt.Errorf("project provider event %s: %w", method, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit provider event %s: %w", method, err)
	}
	return true, nil
}

// ErrConversationTurnNotFound reports a turn id that is not in the conversation it
// was named against. Distinct from a rollback the provider refused: the caller
// pointed at nothing. It aliases the domain sentinel so a service can recognize it
// without importing the storage layer.
var ErrConversationTurnNotFound = domain.ErrNoConversationTurn

// TurnByID reads one turn. It returns ErrConversationTurnNotFound rather than a
// zero value, so a caller cannot mistake "no such turn" for "a turn with no
// provider id".
func (s *Store) TurnByID(ctx context.Context, turnID string) (domain.ConversationTurn, error) {
	row, err := s.qr.SelectConversationTurnByID(ctx, turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationTurn{}, fmt.Errorf("%w: %s", ErrConversationTurnNotFound, turnID)
	}
	if err != nil {
		return domain.ConversationTurn{}, fmt.Errorf("select turn %s: %w", turnID, err)
	}
	return turnToDomain(row), nil
}

// RollbackTurns records that a rollback discarded the named turn and everything
// after it, and returns how many turns that was.
//
// This is the AO half of an operation the provider has already performed: the agent
// has forgotten those turns, and these rows are what stop the user being shown
// prose the agent cannot recall. The three statements commit together because a
// partial result is the one state that reintroduces the disagreement the whole
// operation exists to prevent.
//
// Nothing is deleted. The turns keep their rows and their immutable sequence
// positions, marked with when they were taken back.
func (s *Store) RollbackTurns(
	ctx context.Context,
	conversationID, turnID string,
	now time.Time,
) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	turn, err := s.qr.SelectConversationTurnByID(ctx, turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: %s", ErrConversationTurnNotFound, turnID)
	}
	if err != nil {
		return 0, fmt.Errorf("select turn %s: %w", turnID, err)
	}
	if turn.ConversationID != conversationID {
		// A turn from another conversation would otherwise anchor a rollback by
		// rowid alone and discard an arbitrary range of this one.
		return 0, fmt.Errorf("%w: %s is not in conversation %s",
			ErrConversationTurnNotFound, turnID, conversationID)
	}

	var discarded int64
	err = s.inTx(ctx, "roll back turns", func(q *gen.Queries) error {
		rows, markErr := q.MarkConversationTurnsRolledBack(ctx,
			gen.MarkConversationTurnsRolledBackParams{
				RolledBackAt:   sql.NullTime{Time: now, Valid: true},
				ConversationID: conversationID,
				ID:             turnID,
			})
		if markErr != nil {
			return fmt.Errorf("mark turns rolled back: %w", markErr)
		}
		discarded = rows

		if err := q.InterruptRolledBackQueuedTurns(ctx,
			gen.InterruptRolledBackQueuedTurnsParams{
				CompletedAt:    sql.NullTime{Time: now, Valid: true},
				ConversationID: conversationID,
			}); err != nil {
			return fmt.Errorf("interrupt rolled back queued turns: %w", err)
		}

		if err := q.AttachLegacyCompactionsToRollbackAnchor(ctx,
			gen.AttachLegacyCompactionsToRollbackAnchorParams{
				AnchorTurnID:         sql.NullString{String: turnID, Valid: true},
				UpdatedAt:            now,
				TargetConversationID: conversationID,
			}); err != nil {
			return fmt.Errorf("correlate legacy compactions with rollback: %w", err)
		}

		if err := q.RecomputeConversationCompactedAt(ctx,
			gen.RecomputeConversationCompactedAtParams{
				UpdatedAt:            now,
				TargetConversationID: conversationID,
			}); err != nil {
			return fmt.Errorf("recompute compacted conversation state: %w", err)
		}

		if err := q.FailRolledBackConversationApprovals(ctx,
			gen.FailRolledBackConversationApprovalsParams{
				UpdatedAt:        now,
				ConversationID:   conversationID,
				ConversationID_2: conversationID,
			}); err != nil {
			return fmt.Errorf("fail rolled back approvals: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(discarded), nil
}

// SetProviderTitle records the title the provider reports for this conversation.
func (s *Store) SetProviderTitle(
	ctx context.Context,
	conversationID, title string,
	now time.Time,
) error {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()
	if err := q.UpdateConversationProviderTitle(ctx,
		gen.UpdateConversationProviderTitleParams{
			ProviderTitle: title,
			UpdatedAt:     now,
			ID:            conversationID,
		}); err != nil {
		return fmt.Errorf("set provider title for %s: %w", conversationID, err)
	}
	return nil
}

// ApplyProviderTitle pushes a provider thread title into the session's display
// name, and reports whether it landed.
//
// applied=false is the ordinary outcome, not a failure: it means the session
// already carries a name a person chose, and that name wins. The check is the
// UPDATE's own guard rather than a read followed by a write, because a manual
// rename arriving between those two would be silently overwritten by whatever the
// provider said a moment earlier.
//
// Writing display_name is what fans the new label out to every live surface: the
// sessions_cdc_update trigger includes display_name in its guard, so the existing
// session_updated event carries it without a second notification path.
func (s *Store) ApplyProviderTitle(
	ctx context.Context,
	conversationID string,
	session domain.SessionID,
	title string,
	now time.Time,
) (bool, error) {
	q, unlock := s.conversationWriter(ctx)
	defer unlock()

	conversation, err := q.SelectConversationByID(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrConversationNotFound
	}
	if err != nil {
		return false, fmt.Errorf("select conversation %s: %w", conversationID, err)
	}

	var applied bool
	err = s.inTx(ctx, "apply provider title", func(q *gen.Queries) error {
		rows, updateErr := q.ApplyConversationTitleToSession(ctx,
			gen.ApplyConversationTitleToSessionParams{
				DisplayName: title,
				UpdatedAt:   now,
				ID:          session,
				// The witness: only a label AO itself last wrote may be replaced.
				DisplayName_2: conversation.AppliedTitle,
			})
		if updateErr != nil {
			return fmt.Errorf("apply title to session %s: %w", session, updateErr)
		}
		if rows == 0 {
			return nil
		}
		applied = true
		return q.UpdateConversationAppliedTitle(ctx, gen.UpdateConversationAppliedTitleParams{
			AppliedTitle: title,
			UpdatedAt:    now,
			ID:           conversationID,
		})
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

// ConversationSnapshot is the durable read model for one conversation.
type ConversationSnapshot struct {
	Conversation               domain.ConversationRecord
	Turns                      []domain.ConversationTurn
	Messages                   []domain.ConversationMessage
	Activities                 []domain.ConversationActivity
	BranchPoints               []domain.ConversationBranchPoint
	BranchedFromEarlierMessage bool
	OldestSequence             int64
	HasMoreBefore              bool
}

// DefaultConversationPageSize is the standard bounded read size for conversation snapshots.
const DefaultConversationPageSize = 200

// LoadConversationSnapshotPage reads a bounded slice ending immediately before
// beforeSequence. Zero starts at the live edge. Messages and activities share one
// sequence, so the store merges their bounded reads and trims them as one page;
// a noisy command cannot crowd prose out by making either table unbounded.
func (s *Store) LoadConversationSnapshotPage(
	ctx context.Context,
	conversationID string,
	beforeSequence int64,
	limit int64,
) (ConversationSnapshot, error) {
	conv, err := s.qr.SelectConversationByID(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationSnapshot{}, ErrConversationNotFound
	}
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select conversation %s: %w", conversationID, err)
	}
	if limit <= 0 {
		limit = DefaultConversationPageSize
	}
	if limit > 500 {
		limit = 500
	}
	if beforeSequence <= 0 || beforeSequence > conv.LatestSequence+1 {
		beforeSequence = conv.LatestSequence + 1
	}
	fetchLimit := limit + 1

	messageRows, err := s.qr.SelectConversationMessagesPage(ctx, gen.SelectConversationMessagesPageParams{
		ConversationID: conversationID,
		BeforeSequence: beforeSequence,
		PageLimit:      fetchLimit,
	})
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select message page: %w", err)
	}
	activityRows, err := s.qr.SelectConversationActivitiesPage(ctx, gen.SelectConversationActivitiesPageParams{
		ConversationID: conversationID,
		BeforeSequence: beforeSequence,
		PageLimit:      fetchLimit,
	})
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select activity page: %w", err)
	}

	sequences := make([]int64, 0, len(messageRows)+len(activityRows))
	for _, row := range messageRows {
		sequences = append(sequences, row.Sequence)
	}
	for _, row := range activityRows {
		sequences = append(sequences, row.Sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] > sequences[j] })
	hasMore := int64(len(sequences)) > limit
	if int64(len(sequences)) > limit {
		sequences = sequences[:limit]
	}
	oldest := beforeSequence
	if len(sequences) > 0 {
		oldest = sequences[len(sequences)-1]
	}

	turnRows, err := s.qr.SelectConversationTurnsPage(ctx, gen.SelectConversationTurnsPageParams{
		ConversationID: conversationID,
		OldestSequence: oldest,
		BeforeSequence: beforeSequence,
	})
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select turn page: %w", err)
	}

	snapshot := ConversationSnapshot{
		Conversation:               conversationToDomain(conv),
		BranchPoints:               s.conversationBranchPoints(ctx, conversationID),
		BranchedFromEarlierMessage: s.conversationBranchedFromEarlierMessage(ctx, conversationID, conv.ActiveBranchID),
		OldestSequence:             oldest,
		HasMoreBefore:              hasMore,
	}
	for _, row := range turnRows {
		snapshot.Turns = append(snapshot.Turns, turnToDomain(row))
	}
	// SQL returns newest-first so LIMIT is useful; the API contract remains
	// oldest-first inside each page.
	for i := len(messageRows) - 1; i >= 0; i-- {
		if messageRows[i].Sequence >= oldest {
			snapshot.Messages = append(snapshot.Messages, messageToDomain(messageRows[i]))
		}
	}
	for i := len(activityRows) - 1; i >= 0; i-- {
		if activityRows[i].Sequence >= oldest {
			snapshot.Activities = append(snapshot.Activities, activityToDomain(activityRows[i]))
		}
	}
	return snapshot, nil
}

// LoadConversationSnapshot reads a whole conversation. Items come back ordered by
// sequence so the caller never has to sort.
func (s *Store) LoadConversationSnapshot(
	ctx context.Context,
	conversationID string,
) (ConversationSnapshot, error) {
	conv, err := s.qr.SelectConversationByID(ctx, conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationSnapshot{}, ErrConversationNotFound
	}
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select conversation %s: %w", conversationID, err)
	}

	turnRows, err := s.qr.SelectConversationTurns(ctx, conversationID)
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select turns: %w", err)
	}
	// Messages and activities exclude anything attached to a rolled-back turn: the
	// agent has forgotten those, and a timeline that still showed them would be
	// describing a conversation the agent is not in.
	messageRows, err := s.qr.SelectConversationMessages(ctx, conversationID)
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select messages: %w", err)
	}
	activityRows, err := s.qr.SelectConversationActivities(ctx, conversationID)
	if err != nil {
		return ConversationSnapshot{}, fmt.Errorf("select activities: %w", err)
	}

	snapshot := ConversationSnapshot{
		Conversation:               conversationToDomain(conv),
		BranchPoints:               s.conversationBranchPoints(ctx, conversationID),
		BranchedFromEarlierMessage: s.conversationBranchedFromEarlierMessage(ctx, conversationID, conv.ActiveBranchID),
	}
	for _, row := range turnRows {
		snapshot.Turns = append(snapshot.Turns, turnToDomain(row))
	}
	for _, row := range messageRows {
		snapshot.Messages = append(snapshot.Messages, messageToDomain(row))
	}
	for _, row := range activityRows {
		snapshot.Activities = append(snapshot.Activities, activityToDomain(row))
	}
	return snapshot, nil
}

func (s *Store) conversationBranchedFromEarlierMessage(
	ctx context.Context,
	conversationID, activeBranchID string,
) bool {
	branch, err := s.ConversationBranch(ctx, conversationID, activeBranchID)
	return err == nil && branch.ParentBranchID != ""
}

// conversationBranchPoints builds prompt-local sibling navigation from durable
// branch rows. Reads cannot fail merely because navigation metadata does, so a
// branch-list error leaves the timeline intact and returns no points.
func (s *Store) conversationBranchPoints(
	ctx context.Context,
	conversationID string,
) []domain.ConversationBranchPoint {
	branches, err := s.ConversationBranches(ctx, conversationID)
	if err != nil {
		return nil
	}
	type groupKey struct {
		sourceBranchID string
		replacedTurnID string
	}
	type member struct {
		branchID string
		turnID   string
	}
	groups := make(map[groupKey][]member)
	groupByReplacement := make(map[string]groupKey)
	order := make([]groupKey, 0)
	for _, branch := range branches {
		if branch.ParentBranchID == "" || branch.ReplacedTurnID == "" || branch.ReplacementTurnID == "" {
			continue
		}
		key := groupKey{sourceBranchID: branch.ParentBranchID, replacedTurnID: branch.ReplacedTurnID}
		if existing, ok := groupByReplacement[branch.ReplacedTurnID]; ok {
			key = existing
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
			groups[key] = []member{{branchID: key.sourceBranchID, turnID: key.replacedTurnID}}
		}
		groups[key] = append(groups[key], member{branchID: branch.ID, turnID: branch.ReplacementTurnID})
		groupByReplacement[branch.ReplacementTurnID] = key
	}

	points := make([]domain.ConversationBranchPoint, 0, len(branches))
	for _, key := range order {
		members := groups[key]
		for index, current := range members {
			point := domain.ConversationBranchPoint{
				TurnID: current.turnID, Position: index + 1, Total: len(members),
			}
			if index > 0 {
				point.PreviousBranchID = members[index-1].branchID
			}
			if index+1 < len(members) {
				point.NextBranchID = members[index+1].branchID
			}
			points = append(points, point)
		}
	}
	return points
}

/* ---- helpers ---------------------------------------------------------- */

// turnIDFor resolves a provider turn id to AO's turn id, or nil when the turn is
// unknown. An item with no turn still belongs in the timeline, so an unresolved
// lookup is not an error.
func (s *Store) turnIDFor(ctx context.Context, conversationID, providerTurnID string) sql.NullString {
	if providerTurnID == "" {
		return sql.NullString{}
	}
	row, err := s.conversationReader(ctx).SelectConversationTurnByProviderID(ctx,
		gen.SelectConversationTurnByProviderIDParams{
			ConversationID: conversationID,
			ProviderTurnID: providerTurnID,
		})
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: row.ID, Valid: true}
}

func conversationToDomain(row gen.Conversation) domain.ConversationRecord {
	rec := domain.ConversationRecord{
		ID:             row.ID,
		Scope:          row.Scope,
		ProjectID:      row.ProjectID,
		ActiveBranchID: row.ActiveBranchID,
		LatestSequence: row.LatestSequence,
		Settings: domain.ConversationSettings{
			Model:           row.Model.String,
			ReasoningEffort: row.ReasoningEffort.String,
			ApprovalMode:    domain.PermissionMode(row.ApprovalMode.String),
		},
		ProviderTitle: row.ProviderTitle,
		AppliedTitle:  row.AppliedTitle,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	if row.CurrentSessionID != nil {
		rec.SessionID = *row.CurrentSessionID
	}
	rec.Usage = usageFromRow(row)
	rec.RateLimits = rateLimitsFromRow(row)
	if row.CompactedAt.Valid {
		compacted := row.CompactedAt.Time
		rec.CompactedAt = &compacted
	}
	rec.ModelReroute = decodeJSONColumn[domain.ConversationModelReroute](row.ModelRerouteJson)
	rec.Account = decodeJSONColumn[domain.ConversationAccount](row.AccountJson)
	rec.ThreadState = decodeJSONColumn[domain.ConversationThreadState](row.ThreadStateJson)
	if servers := decodeJSONColumn[[]domain.ConversationMCPServer](row.McpServersJson); servers != nil {
		rec.MCPServers = *servers
	}
	return rec
}

func conversationBranchToDomain(row gen.SelectConversationBranchRow) domain.ConversationBranch {
	return domain.ConversationBranch{
		ID:                     row.ID,
		ConversationID:         row.ConversationID,
		SessionID:              domain.SessionID(row.SessionID.String),
		ProviderConversationID: row.ProviderConversationID,
		ParentBranchID:         row.ParentBranchID.String,
		ForkAfterTurnID:        row.ForkAfterTurnID.String,
		ReplacedTurnID:         row.ReplacedTurnID.String,
		ReplacementTurnID:      row.ReplacementTurnID.String,
		ForkAfterSequence:      row.ForkAfterSequence,
		Active:                 row.Active,
		CreatedAt:              row.CreatedAt,
	}
}

func conversationBranchListToDomain(row gen.SelectConversationBranchesRow) domain.ConversationBranch {
	return domain.ConversationBranch{
		ID:                     row.ID,
		ConversationID:         row.ConversationID,
		SessionID:              domain.SessionID(row.SessionID.String),
		ProviderConversationID: row.ProviderConversationID,
		ParentBranchID:         row.ParentBranchID.String,
		ForkAfterTurnID:        row.ForkAfterTurnID.String,
		ReplacedTurnID:         row.ReplacedTurnID.String,
		ReplacementTurnID:      row.ReplacementTurnID.String,
		ForkAfterSequence:      row.ForkAfterSequence,
		Active:                 row.Active,
		CreatedAt:              row.CreatedAt,
	}
}

// decodeJSONColumn reads one of the conversation's latest-wins JSON columns.
//
// A payload this build cannot parse yields nil rather than a zero value, which
// matters because nil is how a reader learns the provider never reported. A
// half-decoded account or thread state would be worse than none: only the first
// looks authoritative.
func decodeJSONColumn[T any](column sql.NullString) *T {
	if !column.Valid || column.String == "" {
		return nil
	}
	var value T
	if err := json.Unmarshal([]byte(column.String), &value); err != nil {
		return nil
	}
	return &value
}

// usageFromRow returns nil when the provider never reported, so a client can tell
// "no usage yet" from "a conversation using zero tokens" -- the second cannot
// happen, and showing an empty meter for the first would be a claim AO has not
// earned.
func usageFromRow(row gen.Conversation) *domain.ConversationUsage {
	if !row.ContextUsed.Valid && !row.UsageTotalTokens.Valid && !row.UsageCost.Valid {
		return nil
	}
	usage := &domain.ConversationUsage{
		ContextUsed:   row.ContextUsed.Int64,
		ContextWindow: row.ContextWindow.Int64,
		InputTokens:   row.UsageInputTokens.Int64,
		OutputTokens:  row.UsageOutputTokens.Int64,
		CachedTokens:  row.UsageCachedTokens.Int64,
		TotalTokens:   row.UsageTotalTokens.Int64,
		Currency:      row.UsageCurrency.String,
	}
	if row.UsageCost.Valid {
		cost := row.UsageCost.Float64
		usage.Cost = &cost
	}
	return usage
}

func nullableCost(value *float64) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

// rateLimitsFromRow reconstitutes the quota position, restoring the port's
// negative "not reported" convention for a window the provider omitted.
func rateLimitsFromRow(row gen.Conversation) *domain.ConversationRateLimits {
	if !row.RateLimitPrimaryPercent.Valid && !row.RateLimitSecondaryPercent.Valid {
		return nil
	}
	limits := &domain.ConversationRateLimits{
		PrimaryUsedPercent:       -1,
		SecondaryUsedPercent:     -1,
		PrimaryResetsInSeconds:   row.RateLimitPrimaryResetsIn.Int64,
		SecondaryResetsInSeconds: row.RateLimitSecondaryResetsIn.Int64,
		PlanLabel:                row.RateLimitPlan.String,
	}
	if row.RateLimitPrimaryPercent.Valid {
		limits.PrimaryUsedPercent = row.RateLimitPrimaryPercent.Float64
	}
	if row.RateLimitSecondaryPercent.Valid {
		limits.SecondaryUsedPercent = row.RateLimitSecondaryPercent.Float64
	}
	return limits
}

func turnToDomain(row gen.ConversationTurn) domain.ConversationTurn {
	turn := domain.ConversationTurn{
		ID:                 row.ID,
		ConversationID:     row.ConversationID,
		HandledBySessionID: row.HandledBySessionID,
		ProviderTurnID:     row.ProviderTurnID,
		State:              row.State,
		ErrorMessage:       row.ErrorMessage,
		RequestedAt:        row.RequestedAt,
	}
	if row.StartedAt.Valid {
		started := row.StartedAt.Time
		turn.StartedAt = &started
	}
	if row.CompletedAt.Valid {
		completed := row.CompletedAt.Time
		turn.CompletedAt = &completed
	}
	if row.DiffJson != "" {
		var diff domain.ConversationTurnDiff
		// A payload this build cannot parse is dropped rather than surfaced half
		// decoded: a diff summary that lists some of the files is worse than one
		// that admits it has nothing, because only the first looks authoritative.
		if err := json.Unmarshal([]byte(row.DiffJson), &diff); err == nil {
			turn.Diff = &diff
		}
	}
	if row.RolledBackAt.Valid {
		rolledBack := row.RolledBackAt.Time
		turn.RolledBackAt = &rolledBack
	}
	if row.PlanJson != "" {
		var plan domain.ConversationPlan
		// Dropped rather than surfaced half decoded, the same rule the diff follows: a
		// plan showing some of its steps is worse than one that admits it has nothing,
		// because only the first looks like the agent's actual plan.
		if err := json.Unmarshal([]byte(row.PlanJson), &plan); err == nil {
			turn.Plan = &plan
		}
	}
	return turn
}

func messageToDomain(row gen.ConversationMessage) domain.ConversationMessage {
	msg := domain.ConversationMessage{
		ID:              row.ID,
		ConversationID:  row.ConversationID,
		Sequence:        row.Sequence,
		Revision:        row.Revision,
		Role:            row.Role,
		Origin:          row.Origin,
		Text:            row.Text,
		Streaming:       row.Streaming != 0,
		ProviderItemID:  row.ProviderItemID,
		ClientMessageID: row.ClientMessageID,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
	if row.TurnID.Valid {
		msg.TurnID = row.TurnID.String
	}
	return msg
}

func activityToDomain(row gen.ConversationActivity) domain.ConversationActivity {
	activity := domain.ConversationActivity{
		ID:                     row.ID,
		ConversationID:         row.ConversationID,
		Sequence:               row.Sequence,
		Revision:               row.Revision,
		Kind:                   row.Kind,
		Status:                 row.Status,
		Summary:                row.Summary,
		RequestID:              row.RequestID,
		ProviderItemID:         row.ProviderItemID,
		CreatedAt:              row.CreatedAt,
		UpdatedAt:              row.UpdatedAt,
		CommandOutput:          row.CommandOutput,
		CommandOutputTruncated: row.CommandOutputTruncated != 0,
		StreamedText:           row.StreamedText,
		StreamedTextTruncated:  row.StreamedTextTruncated != 0,
	}
	if row.TurnID.Valid {
		activity.TurnID = row.TurnID.String
	}
	if row.DetailJson != "" && json.Valid([]byte(row.DetailJson)) {
		activity.Detail = []byte(row.DetailJson)
	}
	return activity
}

// ProviderEventsSince reads the raw provider-event archive forward from an id.
// This is the recovery path: when a projection turns out to be wrong, these rows
// are the only account of what the provider actually said.
func (s *Store) ProviderEventsSince(
	ctx context.Context,
	conversationID string,
	afterID int64,
	limit int64,
) ([]gen.ConversationProviderEvent, error) {
	rows, err := s.qr.SelectConversationProviderEvents(ctx,
		gen.SelectConversationProviderEventsParams{
			ConversationID: conversationID,
			ID:             afterID,
			PageLimit:      limit,
		})
	if err != nil {
		return nil, fmt.Errorf("select provider events for %s: %w", conversationID, err)
	}
	return rows, nil
}
