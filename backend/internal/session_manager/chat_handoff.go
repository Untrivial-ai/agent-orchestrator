package sessionmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// prepareChatProviderHandoff is read-only. A mismatch alone is never proof: only
// the coordinator's exact native identity may introduce an independent context.
// Historical repairs additionally require that this session still owns the
// project narrative; a retired session cannot take it back from its replacement.
func (m *Manager) prepareChatProviderHandoff(ctx context.Context, rec domain.SessionRecord, liveHandoff bool) (*domain.ChatProviderHandoff, error) {
	store, ok := m.store.(chatProviderOwnershipStore)
	if !ok || rec.Metadata.ProviderConversationID == "" || domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat {
		return nil, nil
	}
	transition, found, err := store.GetLatestSessionInterfaceTransition(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if !found || transition.SessionID != rec.ID || transition.SourceMode != domain.SessionModeTUI ||
		transition.TargetMode != domain.SessionModeChat || transition.NativeConversationID != rec.Metadata.ProviderConversationID {
		return nil, nil
	}
	if liveHandoff {
		if transition.Phase != domain.SessionInterfaceTransitionTargetStarting && transition.Phase != domain.SessionInterfaceTransitionActivating {
			return nil, nil
		}
	} else if !rec.IsTerminated || transition.Phase != domain.SessionInterfaceTransitionCompleted {
		return nil, nil
	}
	conversation, err := store.ConversationForSession(ctx, rec.ID)
	if errors.Is(err, domain.ErrNoConversation) && liveHandoff && rec.Kind == domain.KindOrchestrator {
		if projects, ok := m.store.(interface {
			ProjectConversation(context.Context, domain.ProjectID) (domain.ConversationRecord, error)
		}); ok {
			conversation, err = projects.ProjectConversation(ctx, rec.ProjectID)
			if errors.Is(err, domain.ErrNoConversation) {
				return nil, nil // first Chat use in this project
			}
		}
	}
	if errors.Is(err, domain.ErrNoConversation) && liveHandoff && rec.Kind != domain.KindOrchestrator {
		return nil, nil // first Chat use for a worker
	}
	if err != nil {
		return nil, err
	}
	if conversation.SessionID != rec.ID {
		previous, found, err := m.store.GetSession(ctx, conversation.SessionID)
		if err != nil {
			return nil, err
		}
		if !liveHandoff || !found || !previous.IsTerminated || rec.IsTerminated ||
			previous.ProjectID != rec.ProjectID || !rec.CreatedAt.After(previous.CreatedAt) {
			return nil, fmt.Errorf("project conversation %s is owned by another session", conversation.ID)
		}
		current, found, err := m.activeOrchestratorSessionID(ctx, rec.ProjectID)
		if err != nil {
			return nil, err
		}
		if !found || current != rec.ID {
			return nil, errors.New("only the current orchestrator may adopt project history")
		}
	}
	branch, err := store.ConversationBranch(ctx, conversation.ID, conversation.ActiveBranchID)
	if err != nil {
		return nil, err
	}
	if branch.SessionID == rec.ID && branch.ProviderConversationID == rec.Metadata.ProviderConversationID {
		return nil, nil // also makes retry after a committed boundary idempotent
	}
	if branch.SessionID == "" || branch.ProviderConversationID == "" {
		if conversation.LatestSequence > 0 || conversation.SessionID != rec.ID {
			return nil, errors.New("cannot prove ownership of the previous Chat history")
		}
		return nil, nil // positively unused root needs no context boundary
	}
	return &domain.ChatProviderHandoff{
		BoundaryID:     interfaceTransitionProviderBoundaryID(transition.ID),
		ConversationID: conversation.ID, PreviousSessionID: conversation.SessionID,
		PreviousBranchID: branch.ID, PreviousSequence: conversation.LatestSequence,
		ExpectedControllerOwner: rec.ControllerOwner(),
	}, nil
}

// A missing native transcript is not permission to reset established Chat work.
// This check belongs above the adapter's command selection so it covers every
// harness, including adapters whose native IDs are assigned by the provider.
func (m *Manager) protectChatHistoryOnFreshRestore(ctx context.Context, rec domain.SessionRecord) error {
	store, ok := m.store.(interface {
		ConversationForSession(context.Context, domain.SessionID) (domain.ConversationRecord, error)
		HasConversationTurns(context.Context, string) (bool, error)
	})
	if !ok {
		if rec.Metadata.ProviderConversationID != "" {
			return fmt.Errorf("%w: cannot verify whether existing Chat history is unused", ErrNotResumable)
		}
		return nil
	}
	conversation, err := store.ConversationForSession(ctx, rec.ID)
	if errors.Is(err, domain.ErrNoConversation) {
		if rec.Metadata.ProviderConversationID != "" {
			return fmt.Errorf("%w: existing Chat conversation ownership is unavailable", ErrNotResumable)
		}
		return nil
	}
	if err != nil {
		return err
	}
	hasTurns, err := store.HasConversationTurns(ctx, conversation.ID)
	if err != nil {
		return err
	}
	if conversation.LatestSequence > 0 || hasTurns {
		return fmt.Errorf("%w: native conversation is unavailable; restore its transcript to resume existing Chat history (no fresh conversation was started)", ErrNotResumable)
	}
	return nil
}
