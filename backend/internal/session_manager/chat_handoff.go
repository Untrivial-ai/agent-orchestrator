package sessionmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A mismatch alone is never proof: only the coordinator's exact native identity
// may introduce an independent context. These lookups do not transfer ownership.
func (m *Manager) chatProviderTransition(ctx context.Context, rec domain.SessionRecord) (*domain.SessionInterfaceTransition, error) {
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
	return &transition, nil
}

func (m *Manager) prepareLiveChatProviderHandoff(ctx context.Context, rec domain.SessionRecord) (*domain.ChatProviderHandoff, error) {
	transition, err := m.chatProviderTransition(ctx, rec)
	if err != nil || transition == nil {
		return nil, err
	}
	if transition.Phase != domain.SessionInterfaceTransitionTargetStarting && transition.Phase != domain.SessionInterfaceTransitionActivating {
		return nil, nil
	}
	store, ok := m.store.(chatProviderOwnershipStore)
	if !ok {
		return nil, errors.New("native handoff requires conversation ownership storage")
	}
	conversation, err := store.ConversationForSession(ctx, rec.ID)
	if errors.Is(err, domain.ErrNoConversation) && rec.Kind == domain.KindOrchestrator {
		if projects, ok := m.store.(interface {
			ProjectConversation(context.Context, domain.ProjectID) (domain.ConversationRecord, error)
		}); ok {
			conversation, err = projects.ProjectConversation(ctx, rec.ProjectID)
			if errors.Is(err, domain.ErrNoConversation) {
				return nil, nil // first Chat use in this project
			}
		}
	}
	if errors.Is(err, domain.ErrNoConversation) && rec.Kind != domain.KindOrchestrator {
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
		if !found || !previous.IsTerminated || rec.IsTerminated ||
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
	return reserveChatProviderHandoff(ctx, store, rec, *transition, conversation)
}

// Recovery may repair only this session's history, never adopt a successor's.
func (m *Manager) prepareRecoveredChatProviderHandoff(ctx context.Context, rec domain.SessionRecord) (*domain.ChatProviderHandoff, error) {
	if !rec.IsTerminated {
		return nil, nil
	}
	transition, err := m.chatProviderTransition(ctx, rec)
	if err != nil || transition == nil {
		return nil, err
	}
	if transition.Phase != domain.SessionInterfaceTransitionCompleted {
		return nil, nil
	}
	store, ok := m.store.(chatProviderOwnershipStore)
	if !ok {
		return nil, errors.New("native handoff recovery requires conversation ownership storage")
	}
	conversation, err := store.ConversationForSession(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if conversation.SessionID != rec.ID {
		return nil, fmt.Errorf("project conversation %s is owned by another session", conversation.ID)
	}
	return reserveChatProviderHandoff(ctx, store, rec, *transition, conversation)
}

func reserveChatProviderHandoff(ctx context.Context, store chatProviderOwnershipStore, rec domain.SessionRecord, transition domain.SessionInterfaceTransition, conversation domain.ConversationRecord) (*domain.ChatProviderHandoff, error) {
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
