package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) sideReferenceContext(ctx context.Context, source *Controller, ref ports.ChatExcerptReference) (string, error) {
	msg := ports.ChatUserMessage{Excerpts: []ports.ChatExcerptReference{ref}}
	if err := hydrateExcerptReferences(ctx, source, s.reader, &msg); err != nil {
		return "", err
	}
	if len(msg.Content) != 1 || msg.Content[0].Excerpt == nil {
		return "", ErrExcerptStale
	}
	var b strings.Builder
	for _, message := range msg.Content[0].Excerpt.Messages {
		fmt.Fprintf(&b, "%s:\n---\n%s\n---\n", message.Role, message.Text)
	}
	return b.String(), nil
}

// sideSeedHistory is used only when the provider cannot fork natively. It
// supplies the visible completed transcript through the anchor as quoted data.
func (s *Service) sideSeedHistory(ctx context.Context, source *Controller, anchor string) (string, error) {
	if anchor == "" {
		return "", nil
	}
	rows, err := s.reader.LoadConversationSnapshot(ctx, source.conversation.ID)
	if err != nil {
		return "", err
	}
	allowed := map[string]bool{}
	found := false
	for _, turn := range rows.Turns {
		if turn.RolledBackAt == nil && turn.State == domain.TurnStateCompleted {
			allowed[turn.ID] = true
		}
		if turn.ID == anchor {
			found = true
			break
		}
	}
	if !found {
		return "", ErrSideAnchorUnavailable
	}
	var b strings.Builder
	for _, message := range rows.Messages {
		if !allowed[message.TurnID] || message.Streaming || strings.TrimSpace(message.Text) == "" {
			continue
		}
		if message.Role != domain.MessageRoleUser && message.Role != domain.MessageRoleAssistant {
			continue
		}
		fmt.Fprintf(&b, "%s:\n---\n%s\n---\n", message.Role, message.Text)
		if b.Len() > 512*1024 {
			return "", fmt.Errorf("side history exceeds reconstruction limit")
		}
	}
	return b.String(), nil
}
