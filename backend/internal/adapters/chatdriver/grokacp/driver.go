// Package grokacp binds Grok Build's native ACP transport and question extension.
package grokacp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const askMethod = "x.ai/ask_user_question"

// New binds the installed Grok executable to its native ACP question extension.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:      domain.HarnessGrok,
		Configure:    configure,
		Capabilities: ports.ChatCapabilities{ports.ChatCapabilityInteractive: true},
		SessionOptions: func(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
			if settings.Model == "" {
				return nil
			}
			return []acpdriver.SessionOption{{ID: "model", Value: settings.Model}}
		},
		ClientExtension: handleExtension, ClientExtensionAliases: map[string]string{askMethod: "_x.ai/ask_user_question"},
		ValidateTurnSettings: func(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
			if settings.Approval != "" && ports.NormalizePermissionMode(settings.Approval) != ports.NormalizePermissionMode(initial) {
				return fmt.Errorf("%w: Grok permission changes require restarting Chat", ports.ErrChatConfigOptionInvalid)
			}
			return nil
		},
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	mode := "default"
	switch ports.NormalizePermissionMode(cfg.Permissions) {
	case ports.PermissionModeAcceptEdits:
		mode = "acceptEdits"
	case ports.PermissionModeAuto:
		mode = "auto"
	case ports.PermissionModeBypassPermissions:
		mode = "bypassPermissions"
	}
	args := []string{"--permission-mode", mode}
	if cfg.SystemPrompt != "" {
		args = append(args, "--rules", cfg.SystemPrompt)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	return append(args, "agent", "stdio"), nil, nil
}

type questionParams struct {
	SessionID  string `json:"sessionId"`
	ToolCallID string `json:"toolCallId"`
	Questions  []struct {
		ID          string `json:"id"`
		Question    string `json:"question"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
			Preview     string `json:"preview"`
		} `json:"options"`
	} `json:"questions"`
}

func handleExtension(ctx context.Context, bridge acpdriver.ClientExtensionBridge, method string, raw json.RawMessage) (any, bool, error) {
	if method != askMethod && method != "_"+askMethod {
		return nil, false, nil
	}
	var wrapped struct {
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, true, err
	}
	if len(wrapped.Params) > 0 {
		raw = wrapped.Params
	}
	var params questionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, true, err
	}
	if params.SessionID == "" || params.ToolCallID == "" {
		return nil, true, fmt.Errorf("question request has no session or tool identity")
	}
	questions := make([]ports.ChatQuestion, 0, len(params.Questions))
	for _, question := range params.Questions {
		id := question.ID
		if id == "" {
			id = question.Question
		}
		q := ports.ChatQuestion{ID: id, Prompt: question.Question, Custom: true, Multiple: question.MultiSelect}
		for _, option := range question.Options {
			q.Options = append(q.Options, ports.ChatQuestionOption{Value: option.Label, Label: option.Label, Description: option.Description})
		}
		questions = append(questions, q)
	}
	request, err := ports.ChatQuestionForm(questions)
	if err != nil {
		return nil, true, err
	}
	response, err := bridge.RequestInput(ctx, request)
	if err != nil {
		return nil, true, err
	}
	if response.Action != ports.ChatInputActionAccept {
		return map[string]any{"outcome": "cancelled"}, true, nil
	}
	if err := ports.ValidateChatInputResponse(request, response); err != nil {
		return nil, true, err
	}
	values := ports.ChatQuestionAnswers(questions, response.Content)
	answers := map[string][]string{}
	annotations := map[string]any{}
	for i, question := range params.Questions {
		var labels, notes []string
		preview := ""
		for _, value := range values[questions[i].ID] {
			found := false
			for _, option := range question.Options {
				if value == option.Label {
					labels = append(labels, value)
					found = true
					if !question.MultiSelect {
						preview = option.Preview
					}
					break
				}
			}
			if !found {
				notes = append(notes, value)
			}
		}
		if len(labels) == 0 {
			labels = []string{"Other"}
		}
		answers[question.Question] = labels
		annotation := map[string]string{}
		if len(notes) > 0 {
			annotation["notes"] = strings.Join(notes, "\n")
		}
		if preview != "" {
			annotation["preview"] = preview
		}
		if len(annotation) > 0 {
			annotations[question.Question] = annotation
		}
	}
	result := map[string]any{"outcome": "accepted", "answers": answers}
	if len(annotations) > 0 {
		result["annotations"] = annotations
	}
	return result, true, nil
}
