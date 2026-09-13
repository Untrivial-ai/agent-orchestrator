package codexappserver

import (
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Native IDs stay on the wire; AO projection IDs belong to an ownership scope.
// Length-prefixing the scope keeps opaque delimiters from creating collisions.
func (c *conversation) scopePrefix() string {
	if c.providerScopeID == "" {
		return ""
	}
	return "codex:" + strconv.Itoa(len(c.providerScopeID)) + ":" + c.providerScopeID
}

func (c *conversation) scopedID(id string) string {
	if id == "" {
		return ""
	}
	return c.scopePrefix() + id
}

func (c *conversation) nativeID(id string) string {
	return strings.TrimPrefix(id, c.scopePrefix())
}

func (c *conversation) scopedEvent(event ports.ChatEvent) ports.ChatEvent {
	if c.providerScopeID == "" {
		return event
	}
	event.ProviderTurnID = c.scopedID(event.ProviderTurnID)
	event.ProviderItemID = c.scopedID(event.ProviderItemID)
	event.ProviderEventID = c.scopedID(event.ProviderEventID)
	event.RequestID = c.scopedID(event.RequestID)
	if event.ClientMessageID != "" {
		// AO-issued IDs still identify already-persisted sends within this scope.
		// The imported row itself needs a scoped key when a native thread is
		// revisited under a different ownership boundary.
		event.ProviderItemAliases = append(event.ProviderItemAliases, event.ClientMessageID)
		event.ClientMessageID = c.scopedID(event.ClientMessageID)
	}
	return event
}
