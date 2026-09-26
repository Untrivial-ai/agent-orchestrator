package unrealagent

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const protocolVersion = 1

type command struct {
	Version        int    `json:"version"`
	Type           string `json:"type"`
	RequestID      string `json:"requestId"`
	ProviderTurnID string `json:"providerTurnId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	Text           string `json:"text,omitempty"`
	Effort         string `json:"effort,omitempty"`
	EventID        string `json:"eventId,omitempty"`
}

type frame struct {
	Version   int        `json:"version"`
	Type      string     `json:"type"`
	RequestID string     `json:"requestId,omitempty"`
	EventID   string     `json:"eventId,omitempty"`
	Error     string     `json:"error,omitempty"`
	Event     *wireEvent `json:"event,omitempty"`
}

type wireEvent struct {
	Kind                   ports.ChatEventKind       `json:"kind"`
	ProviderTurnID         string                    `json:"providerTurnId,omitempty"`
	ProviderConversationID string                    `json:"providerConversationId,omitempty"`
	ProviderItemID         string                    `json:"providerItemId,omitempty"`
	TurnState              domain.TurnState          `json:"turnState,omitempty"`
	Delta                  string                    `json:"delta,omitempty"`
	Text                   string                    `json:"text,omitempty"`
	ActivityKind           domain.ActivityKind       `json:"activityKind,omitempty"`
	ActivityStatus         domain.ActivityStatus     `json:"activityStatus,omitempty"`
	Summary                string                    `json:"summary,omitempty"`
	Detail                 json.RawMessage           `json:"detail,omitempty"`
	ControllerState        ports.ChatControllerState `json:"controllerState,omitempty"`
	Usage                  *ports.ChatUsage          `json:"usage,omitempty"`
	Error                  string                    `json:"error,omitempty"`
}

func (event wireEvent) chatEvent(eventID string) ports.ChatEvent {
	converted := ports.ChatEvent{
		Kind: event.Kind, ProviderEventID: eventID,
		ProviderTurnID: event.ProviderTurnID, ProviderConversationID: event.ProviderConversationID,
		ProviderItemID: event.ProviderItemID, TurnState: event.TurnState,
		Delta: event.Delta, Text: event.Text,
		ActivityKind: event.ActivityKind, ActivityStatus: event.ActivityStatus,
		Summary: event.Summary, Detail: event.Detail,
		ControllerState: event.ControllerState, Usage: event.Usage,
	}
	if event.Error != "" {
		converted.Err = ports.NewChatProviderFailure("Unreal Agent", event.Error, nil)
	}
	return converted
}
