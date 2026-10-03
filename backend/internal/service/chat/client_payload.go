package chat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// clientPayloadHash binds a client message ID to the request as received,
// before AO may append worker reports or edit a queued message.
func clientPayloadHash(msg ports.ChatUserMessage) (string, error) {
	if msg.ClientMessageID == "" {
		return "", nil
	}
	content := msg.Content
	if len(content) == 0 {
		content = nil
	}
	payload, err := json.Marshal(struct {
		Text           string
		Content        []ports.ChatContent
		Origin         domain.MessageOrigin
		AuthoredByUser bool
		Settings       ports.ChatTurnSettings
	}{msg.Text, content, normalizeOrigin(msg.Origin), msg.AuthoredByUser, msg.Settings})
	if err != nil {
		return "", fmt.Errorf("encode client message payload: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func legacyMessageMatches(existing domain.ConversationMessage, msg ports.ChatUserMessage) bool {
	if existing.Text != msg.Text || existing.Origin != normalizeOrigin(msg.Origin) {
		return false
	}
	if len(msg.Content) == 0 {
		return existing.DeliveryContentJSON == ""
	}
	content, err := json.Marshal(msg.Content)
	return err == nil && existing.DeliveryContentJSON == string(content)
}
