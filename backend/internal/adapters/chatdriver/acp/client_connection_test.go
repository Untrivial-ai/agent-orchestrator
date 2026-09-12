package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestExtensionMethodReaderRoutesOnlyConfiguredLegacyMethodsThroughSDK(t *testing.T) {
	clientToAgentR, clientToAgentW := io.Pipe()
	agentToClientR, agentToClientW := io.Pipe()
	conv := &conversation{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending:        make(map[string]*parkedPermission),
		pendingInputs:  make(map[string]*parkedInput),
		capabilities:   make(ports.ChatCapabilities),
		messages:       make(map[string]string),
		thoughts:       make(map[string]string),
		nestedMessages: make(map[string]nestedMessageState),
		tools:          make(map[string]*toolState),
		events:         make(chan ports.ChatEvent, 16),
		activeTurn:     "turn-1",
	}
	extension := func(
		ctx context.Context,
		bridge ClientExtensionBridge,
		method string,
		_ json.RawMessage,
	) (any, bool, error) {
		switch method {
		case "cursor/ask_question":
			response, err := bridge.RequestInput(ctx, ports.ChatInputRequest{
				Mode: ports.ChatInputModeForm, Message: "Need input",
				Schema: map[string]any{
					"type": "object", "properties": map[string]any{
						"mode": map[string]any{"type": "string", "enum": []any{"agent", "plan"}},
					}, "required": []any{"mode"},
				},
			})
			if err != nil {
				return nil, true, err
			}
			return map[string]any{"outcome": map[string]any{
				"outcome": "answered", "value": response.Content["mode"],
			}}, true, nil
		case "cursor/create_plan":
			bridge.UpdatePlan(&domain.ConversationPlan{Steps: []domain.ConversationPlanStep{{
				Text: "Verify", Status: domain.PlanStepPending,
			}}})
			selected, err := bridge.RequestApproval(ctx, ClientApprovalRequest{
				Summary: "Approve Cursor plan", ActivityKind: domain.ActivityKindPlan,
				Decisions: []ports.ChatDecisionOption{{ID: "accept", Label: "Approve"}},
			})
			if err != nil {
				return nil, true, err
			}
			outcome := "cancelled"
			if selected == "accept" {
				outcome = "accepted"
			}
			return map[string]any{"outcome": map[string]any{"outcome": outcome}}, true, nil
		default:
			return nil, false, nil
		}
	}
	conv.extensionFor = extension
	conv.extensionMethods = map[string]string{
		"_cursor/ask_question": "cursor/ask_question",
		"_cursor/create_plan":  "cursor/create_plan",
	}
	peerOutput := newExtensionMethodReader(agentToClientR, map[string]string{
		"cursor/ask_question": "_cursor/ask_question",
		"cursor/create_plan":  "_cursor/create_plan",
	})
	conv.conn = acpsdk.NewClientSideConnection(conv, clientToAgentW, peerOutput)
	reader := bufio.NewReader(clientToAgentR)
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			_ = clientToAgentW.Close()
			_ = clientToAgentR.Close()
			_ = agentToClientW.Close()
			_ = agentToClientR.Close()
		})
	})

	if _, err := agentToClientW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"cursor/ask_question","params":{}}` + "\n")); err != nil {
		t.Fatalf("write ask request: %v", err)
	}
	input := nextEvent(t, conv.Events())
	if input.Kind != ports.ChatEventInputRequested || input.Input == nil {
		t.Fatalf("ask event = %#v", input)
	}
	if err := conv.ResolveInput(context.Background(), input.RequestID, ports.ChatInputResponse{
		Action: ports.ChatInputActionAccept, Content: map[string]any{"mode": "agent"},
	}); err != nil {
		t.Fatalf("resolve ask: %v", err)
	}
	resolved := nextEvent(t, conv.Events())
	if resolved.Kind != ports.ChatEventInputResolved {
		t.Fatalf("resolved ask event = %#v", resolved)
	}
	if response := readWireResponse(t, reader); response.ID != 1 || response.Outcome != "answered" {
		t.Fatalf("ask wire response = %#v", response)
	}

	if _, err := agentToClientW.Write([]byte(`{"jsonrpc":"2.0","id":3,"method":"cursor/ask_question","params":{}}` + "\n")); err != nil {
		t.Fatalf("write replayed ask request: %v", err)
	}
	replayedInput := nextEvent(t, conv.Events())
	if replayedInput.Kind != ports.ChatEventInputRequested {
		t.Fatalf("replayed ask event = %#v", replayedInput)
	}
	command, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": persistenthost.ACPInteractionCommandMethod,
		"params": persistentInteractionCommand{
			EventID: "acp-host:input", RequestID: replayedInput.RequestID,
			Kind: persistentInteractionInput, ProviderPending: true,
			Input: &ports.ChatInputResponse{
				Action: ports.ChatInputActionAccept, Content: map[string]any{"mode": "plan"},
			},
		},
	})
	if _, err := agentToClientW.Write(append(command, '\n')); err != nil {
		t.Fatalf("write accepted input command: %v", err)
	}
	replayedResolved := nextEvent(t, conv.Events())
	if replayedResolved.Kind != ports.ChatEventInputResolved ||
		replayedResolved.ProviderEventID != "acp-host:input" {
		t.Fatalf("replayed input resolution = %#v", replayedResolved)
	}
	if response := readWireResponse(t, reader); response.ID != 3 || response.Outcome != "answered" {
		t.Fatalf("replayed ask wire response = %#v", response)
	}

	if _, err := agentToClientW.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"cursor/create_plan","params":{}}` + "\n")); err != nil {
		t.Fatalf("write plan request: %v", err)
	}
	plan := nextEvent(t, conv.Events())
	approval := nextEvent(t, conv.Events())
	if plan.Kind != ports.ChatEventPlanUpdated || approval.Kind != ports.ChatEventApprovalRequested {
		t.Fatalf("plan events = %#v, %#v", plan, approval)
	}
	if err := conv.ResolveRequest(context.Background(), approval.RequestID, ports.ChatDecision{ID: "accept"}); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	_ = nextEvent(t, conv.Events()) // approval.resolved
	if response := readWireResponse(t, reader); response.ID != 2 || response.Outcome != "accepted" {
		t.Fatalf("plan wire response = %#v", response)
	}

	// JSON-RPC cancellation must unblock the provider request and return Cursor's
	// cancelled outcome instead of leaving the agent parked indefinitely.
	if _, err := agentToClientW.Write([]byte(`{"jsonrpc":"2.0","id":3,"method":"cursor/create_plan","params":{}}` + "\n")); err != nil {
		t.Fatalf("write cancellable plan request: %v", err)
	}
	_ = nextEvent(t, conv.Events()) // plan.updated
	cancellable := nextEvent(t, conv.Events())
	if _, err := agentToClientW.Write([]byte(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":3}}` + "\n")); err != nil {
		t.Fatalf("write cancel notification: %v", err)
	}
	resolved = nextEvent(t, conv.Events())
	if resolved.Kind != ports.ChatEventApprovalResolved || resolved.RequestID != cancellable.RequestID {
		t.Fatalf("cancelled approval event = %#v", resolved)
	}
	if response := readWireResponse(t, reader); response.ID != 3 || response.Outcome != "cancelled" {
		t.Fatalf("cancelled plan wire response = %#v", response)
	}
}

func TestExtensionMethodReaderRejectsOversizedFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{name: "terminated", input: strings.Repeat("x", 33) + "\n"},
		{name: "unterminated", input: strings.Repeat("x", 33)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &extensionMethodReader{
				reader:   bufio.NewReaderSize(strings.NewReader(tc.input), 8),
				aliases:  map[string]string{"cursor/ask_question": "_cursor/ask_question"},
				maxFrame: 32,
			}
			output, err := io.ReadAll(reader)
			if !errors.Is(err, errACPFrameTooLarge) {
				t.Fatalf("ReadAll error = %v, want %v", err, errACPFrameTooLarge)
			}
			if len(output) != 0 {
				t.Fatalf("ReadAll returned %d bytes from rejected frame", len(output))
			}
		})
	}
}

type wireResponse struct {
	ID      int
	Outcome string
}

func readWireResponse(t *testing.T, reader *bufio.Reader) wireResponse {
	t.Helper()
	line := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		value, err := reader.ReadBytes('\n')
		if err != nil {
			errCh <- err
			return
		}
		line <- value
	}()
	select {
	case err := <-errCh:
		t.Fatalf("read wire response: %v", err)
	case value := <-line:
		var envelope struct {
			ID     int `json:"id"`
			Result struct {
				Outcome struct {
					Outcome string `json:"outcome"`
				} `json:"outcome"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(value, &envelope); err != nil {
			t.Fatalf("decode wire response %q: %v", value, err)
		}
		if envelope.Error != nil {
			t.Fatalf("wire response error: %s", value)
		}
		return wireResponse{ID: envelope.ID, Outcome: envelope.Result.Outcome.Outcome}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading wire response")
	}
	return wireResponse{}
}
