package grokacp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type inputBridge struct {
	request  ports.ChatInputRequest
	response ports.ChatInputResponse
}

func (b *inputBridge) RequestInput(_ context.Context, r ports.ChatInputRequest) (ports.ChatInputResponse, error) {
	b.request = r
	return b.response, nil
}
func (b *inputBridge) RequestApproval(context.Context, acpdriver.ClientApprovalRequest) (string, error) {
	panic("not an approval")
}
func (b *inputBridge) UpdatePlan(*domain.ConversationPlan) {}

func TestGrokQuestionExtensionPreservesCustomNotesAndSelectionPreview(t *testing.T) {
	raw := json.RawMessage(`{"sessionId":"session","toolCallId":"tool","questions":[{"question":"Which?","options":[{"label":"A","description":"First","preview":"preview"}]},{"question":"Why?","options":[{"label":"B"}]}]}`)
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "wrapped"}[wrapped], func(t *testing.T) {
			payload := raw
			if wrapped {
				payload = json.RawMessage(`{"params":` + string(raw) + `}`)
			}
			bridge := &inputBridge{response: ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"question_0": "A", "question_1_custom": "because"}}}
			result, handled, err := handleExtension(context.Background(), bridge, askMethod, payload)
			if err != nil || !handled {
				t.Fatalf("extension: %v %v", handled, err)
			}
			encoded, _ := json.Marshal(result)
			expected := `{"annotations":{"Which?":{"preview":"preview"},"Why?":{"notes":"because"}},"answers":{"Which?":["A"],"Why?":["Other"]},"outcome":"accepted"}`
			if string(encoded) != expected {
				t.Fatalf("reply: %s", encoded)
			}
			if bridge.request.Mode != ports.ChatInputModeForm {
				t.Fatal("question did not reach input bridge")
			}
		})
	}
}

func TestGrokQuestionCancellationAndMalformedIdentity(t *testing.T) {
	bridge := &inputBridge{response: ports.ChatInputResponse{Action: ports.ChatInputActionCancel}}
	result, handled, err := handleExtension(context.Background(), bridge, "_"+askMethod, json.RawMessage(`{"sessionId":"s","toolCallId":"t","questions":[{"question":"Why?"}]}`))
	if err != nil || !handled || !reflect.DeepEqual(result, map[string]any{"outcome": "cancelled"}) {
		t.Fatalf("cancel: %v %v %v", result, handled, err)
	}
	if _, _, err := handleExtension(context.Background(), bridge, askMethod, json.RawMessage(`{"questions":[{"question":"Why?"}]}`)); err == nil {
		t.Fatal("accepted uncorrelated request")
	}
	if _, handled, err := handleExtension(context.Background(), bridge, "other", nil); handled || err != nil {
		t.Fatal("claimed another extension")
	}
}

func TestGrokLaunchUsesExplicitPermissionMode(t *testing.T) {
	for mode, want := range map[ports.PermissionMode]string{ports.PermissionModeDefault: "default", ports.PermissionModeAcceptEdits: "acceptEdits", ports.PermissionModeAuto: "auto", ports.PermissionModeBypassPermissions: "bypassPermissions"} {
		args, _, err := configure(context.Background(), acpdriver.LaunchConfig{Permissions: mode, Model: "grok", SystemPrompt: "rules"})
		if err != nil || !reflect.DeepEqual(args, []string{"--permission-mode", want, "--rules", "rules", "--model", "grok", "agent", "stdio"}) {
			t.Fatalf("launch: %v %v", args, err)
		}
	}
}
