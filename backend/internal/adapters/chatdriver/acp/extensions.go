package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	persistentInteractionApproval = "approval"
	persistentInteractionInput    = "input"
)

type persistentInteractionCommand struct {
	EventID         string                   `json:"eventId,omitempty"`
	RequestID       string                   `json:"requestId"`
	Kind            string                   `json:"kind"`
	ProviderPending bool                     `json:"providerPending,omitempty"`
	Decision        *persistentDecision      `json:"decision,omitempty"`
	Input           *ports.ChatInputResponse `json:"input,omitempty"`
}

type persistentDecision struct {
	ID string `json:"id"`
}

// ClientApprovalRequest describes a provider extension's blocking approval in
// AO's durable, provider-neutral vocabulary.
type ClientApprovalRequest struct {
	Summary      string
	ActivityKind domain.ActivityKind
	Detail       []byte
	Decisions    []ports.ChatDecisionOption
}

// ClientExtensionBridge exposes only the durable user-interaction operations a
// provider extension may need while its JSON-RPC request is blocked.
type ClientExtensionBridge interface {
	RequestInput(context.Context, ports.ChatInputRequest) (ports.ChatInputResponse, error)
	RequestApproval(context.Context, ClientApprovalRequest) (string, error)
	UpdatePlan(*domain.ConversationPlan)
}

// ClientExtensionHandler handles provider-defined agent-to-client JSON-RPC
// methods, including providers whose legacy names do not use ACP's `_` prefix.
// handled=false delegates to the standard ACP method dispatcher.
type ClientExtensionHandler func(
	context.Context,
	ClientExtensionBridge,
	string,
	json.RawMessage,
) (result any, handled bool, err error)

type stableExtensionBridge struct {
	conversation *conversation
	requestID    string
}

func (b stableExtensionBridge) RequestInput(
	ctx context.Context,
	request ports.ChatInputRequest,
) (ports.ChatInputResponse, error) {
	return b.conversation.requestInput(ctx, b.requestID, request)
}

func (b stableExtensionBridge) RequestApproval(
	ctx context.Context,
	request ClientApprovalRequest,
) (string, error) {
	return b.conversation.requestApproval(ctx, b.requestID, request)
}

func (b stableExtensionBridge) UpdatePlan(plan *domain.ConversationPlan) {
	b.conversation.UpdatePlan(plan)
}

// HandleExtensionMethod is the SDK's narrow extension hook. Legacy wire method
// aliases are restored before dispatch so provider handlers use documented names.
func (c *conversation) HandleExtensionMethod(
	ctx context.Context,
	method string,
	params json.RawMessage,
) (any, error) {
	if method == persistenthost.ACPInteractionCommandMethod {
		var command persistentInteractionCommand
		if err := json.Unmarshal(params, &command); err != nil {
			return nil, err
		}
		validPayload := (command.Kind == persistentInteractionApproval && command.Decision != nil && command.Decision.ID != "") ||
			(command.Kind == persistentInteractionInput && command.Input != nil && command.Input.Action.Valid())
		if command.EventID == "" || command.RequestID == "" || !validPayload {
			return nil, errors.New("invalid persistent ACP interaction command")
		}
		c.applyPersistentInteraction(command)
		return nil, nil
	}
	if method == persistenthost.ACPPromptResultMethod {
		var payload struct {
			Result  json.RawMessage      `json:"result"`
			Error   *acpsdk.RequestError `json:"error"`
			EventID string               `json:"eventId"`
		}
		if err := json.Unmarshal(params, &payload); err != nil {
			return nil, err
		}
		var response acpsdk.PromptResponse
		var promptErr error
		if payload.Error != nil {
			promptErr = payload.Error
		} else if err := json.Unmarshal(payload.Result, &response); err != nil {
			promptErr = err
		}
		if response.Meta == nil {
			response.Meta = make(map[string]any)
		}
		response.Meta[persistenthost.ACPEventIDMetaKey] = payload.EventID
		c.mu.Lock()
		turnID := c.activeTurn
		ignore := c.ignorePromptResult
		if ignore {
			c.ignorePromptResult = false
		}
		c.mu.Unlock()
		if ignore {
			return nil, nil
		}
		if turnID == "" {
			return nil, errors.New("persistent ACP prompt result has no durable active turn")
		}
		c.finishPrompt(turnID, response, promptErr)
		return nil, nil
	}
	original, configured := c.extensionMethods[method]
	if !configured || c.extensionFor == nil {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	var envelope struct {
		Meta map[string]any `json:"_meta"`
	}
	_ = json.Unmarshal(params, &envelope)
	bridge := stableExtensionBridge{conversation: c, requestID: stableACPRequestID(envelope.Meta)}
	result, handled, err := c.extensionFor(ctx, bridge, original, params)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, acpsdk.NewMethodNotFound(original)
	}
	return result, nil
}

func (c *conversation) recordPersistentInteraction(
	ctx context.Context,
	command persistentInteractionCommand,
) (string, error) {
	if c.proc == nil || c.proc.terminate == nil {
		return "", nil
	}
	raw, err := c.conn.CallExtension(ctx, persistenthost.ACPInteractionCommandMethod, command)
	if err != nil {
		return "", fmt.Errorf("record persistent ACP interaction: %w", err)
	}
	var accepted struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil || accepted.EventID == "" {
		return "", errors.New("persistent ACP host returned an invalid interaction acceptance")
	}
	return accepted.EventID, nil
}

func (c *conversation) applyPersistentInteraction(command persistentInteractionCommand) {
	if command.ProviderPending {
		c.mu.Lock()
		switch command.Kind {
		case persistentInteractionApproval:
			request := c.pending[command.RequestID]
			if request == nil {
				if c.accepted == nil {
					c.accepted = make(map[string]persistentInteractionCommand)
				}
				c.accepted[command.RequestID] = command
				c.mu.Unlock()
				return
			}
			delete(c.pending, command.RequestID)
			c.mu.Unlock()
			<-request.ready
			c.emit(persistentInteractionEvent(command))
			request.result <- command.Decision.ID
			return
		case persistentInteractionInput:
			request := c.pendingInputs[command.RequestID]
			if request == nil {
				if c.accepted == nil {
					c.accepted = make(map[string]persistentInteractionCommand)
				}
				c.accepted[command.RequestID] = command
				c.mu.Unlock()
				return
			}
			delete(c.pendingInputs, command.RequestID)
			c.mu.Unlock()
			<-request.ready
			c.emit(persistentInteractionEvent(command))
			request.result <- *command.Input
			return
		}
	}
	c.emit(persistentInteractionEvent(command))
}

func persistentInteractionEvent(command persistentInteractionCommand) ports.ChatEvent {
	event := ports.ChatEvent{ProviderEventID: command.EventID, RequestID: command.RequestID}
	switch command.Kind {
	case persistentInteractionApproval:
		event.Kind = ports.ChatEventApprovalResolved
		event.Detail, _ = json.Marshal(map[string]string{"decision": command.Decision.ID})
	case persistentInteractionInput:
		event.Kind = ports.ChatEventInputResolved
		event.Detail, _ = json.Marshal(map[string]any{
			"action": command.Input.Action, "content": command.Input.Content,
		})
	}
	return event
}
