package acp

import (
	"context"
	"encoding/json"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ClientApprovalRequest describes a provider extension's blocking approval in
// AO's durable, provider-neutral vocabulary.
type ClientApprovalRequest struct {
	Summary      string
	ActivityKind domain.ActivityKind
	Detail       []byte
	Decisions    []ports.ChatDecisionOption
}

// ClientExtensionBridge exposes only the conversation operations a provider
// extension may need while its JSON-RPC request is blocked. Approval and input
// remain durable AO interactions; configuration and continuation let an
// accepted provider workflow resume inside that same durable turn.
type ClientExtensionBridge interface {
	RequestInput(context.Context, ports.ChatInputRequest) (ports.ChatInputResponse, error)
	RequestApproval(context.Context, ClientApprovalRequest) (string, error)
	RequestTurnContinuation(context.Context, string) error
	SetConfigOption(context.Context, string, ports.ChatConfigOptionValue) ([]ports.ChatConfigOption, error)
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

// HandleExtensionMethod is the SDK's narrow extension hook. Legacy wire method
// aliases are restored before dispatch so provider handlers use documented names.
func (c *conversation) HandleExtensionMethod(
	ctx context.Context,
	method string,
	params json.RawMessage,
) (any, error) {
	original, configured := c.extensionMethods[method]
	if !configured || c.extensionFor == nil {
		return nil, acpsdk.NewMethodNotFound(method)
	}
	result, handled, err := c.extensionFor(ctx, c, original, params)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, acpsdk.NewMethodNotFound(original)
	}
	return result, nil
}
