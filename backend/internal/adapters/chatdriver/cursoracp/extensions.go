package cursoracp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	askQuestionMethod = "cursor/ask_question"
	createPlanMethod  = "cursor/create_plan"
)

type askQuestionRequest struct {
	ToolCallID string        `json:"toolCallId"`
	Title      string        `json:"title"`
	Questions  []askQuestion `json:"questions"`
}

type askQuestion struct {
	ID            string              `json:"id"`
	Prompt        string              `json:"prompt"`
	Options       []askQuestionOption `json:"options"`
	AllowMultiple bool                `json:"allowMultiple"`
}

type askQuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type createPlanRequest struct {
	ToolCallID string     `json:"toolCallId"`
	Name       string     `json:"name"`
	Overview   string     `json:"overview"`
	Plan       string     `json:"plan"`
	Todos      []planTodo `json:"todos"`
}

type planTodo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

func handleExtension(
	ctx context.Context,
	bridge acpdriver.ClientExtensionBridge,
	method string,
	raw json.RawMessage,
) (any, bool, error) {
	switch method {
	case askQuestionMethod:
		result, err := handleAskQuestion(ctx, bridge, raw)
		return result, true, err
	case createPlanMethod:
		result, err := handleCreatePlan(ctx, bridge, raw)
		return result, true, err
	default:
		return nil, false, nil
	}
}

func handleAskQuestion(
	ctx context.Context,
	bridge acpdriver.ClientExtensionBridge,
	raw json.RawMessage,
) (any, error) {
	var params askQuestionRequest
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode cursor/ask_question: %w", err)
	}
	if len(params.Questions) == 0 {
		return nil, errors.New("cursor/ask_question contains no questions")
	}
	properties := make(map[string]any, len(params.Questions))
	required := make([]string, 0, len(params.Questions))
	for _, question := range params.Questions {
		if strings.TrimSpace(question.ID) == "" || len(question.Options) == 0 {
			return nil, errors.New("cursor/ask_question contains an invalid question")
		}
		choices := make([]any, 0, len(question.Options))
		for _, option := range question.Options {
			choices = append(choices, map[string]any{"const": option.ID, "title": option.Label})
		}
		property := map[string]any{"title": question.Prompt}
		if question.AllowMultiple {
			property["type"] = "array"
			property["items"] = map[string]any{"anyOf": choices}
		} else {
			property["type"] = "string"
			property["oneOf"] = choices
		}
		properties[question.ID] = property
		required = append(required, question.ID)
	}
	message := strings.TrimSpace(params.Title)
	if message == "" {
		message = "Cursor needs input"
	}
	response, err := bridge.RequestInput(ctx, ports.ChatInputRequest{
		Mode: ports.ChatInputModeForm, Message: message,
		Schema: map[string]any{"type": "object", "properties": properties, "required": required},
	})
	if err != nil {
		return nil, err
	}
	switch response.Action {
	case ports.ChatInputActionAccept:
		answers := make([]map[string]any, 0, len(params.Questions))
		for _, question := range params.Questions {
			selected := selectedOptionIDs(response.Content[question.ID])
			answers = append(answers, map[string]any{
				"questionId": question.ID, "selectedOptionIds": selected,
			})
		}
		return map[string]any{"outcome": map[string]any{"outcome": "answered", "answers": answers}}, nil
	case ports.ChatInputActionDecline:
		return map[string]any{"outcome": map[string]any{"outcome": "skipped"}}, nil
	default:
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
}

func selectedOptionIDs(value any) []string {
	switch selected := value.(type) {
	case string:
		return []string{selected}
	case []string:
		return append([]string(nil), selected...)
	case []any:
		out := make([]string, 0, len(selected))
		for _, value := range selected {
			if id, ok := value.(string); ok {
				out = append(out, id)
			}
		}
		return out
	default:
		return []string{}
	}
}

func handleCreatePlan(
	ctx context.Context,
	bridge acpdriver.ClientExtensionBridge,
	raw json.RawMessage,
) (any, error) {
	var params createPlanRequest
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("decode cursor/create_plan: %w", err)
	}
	plan := &domain.ConversationPlan{Steps: make([]domain.ConversationPlanStep, 0, len(params.Todos))}
	for _, todo := range params.Todos {
		status := domain.PlanStepPending
		switch todo.Status {
		case "in_progress":
			status = domain.PlanStepInProgress
		case "completed":
			status = domain.PlanStepCompleted
		}
		plan.Steps = append(plan.Steps, domain.ConversationPlanStep{Text: todo.Content, Status: status})
	}
	bridge.UpdatePlan(plan)
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = "proposed changes"
	}
	selected, err := bridge.RequestApproval(ctx, acpdriver.ClientApprovalRequest{
		Summary:      "Approve Cursor plan: " + name,
		ActivityKind: domain.ActivityKindPlan,
		Decisions: []ports.ChatDecisionOption{
			{ID: "accept", Label: "Approve plan"},
			{ID: "reject", Label: "Reject plan"},
		},
	})
	if err != nil {
		return nil, err
	}
	switch selected {
	case "accept":
		return map[string]any{"outcome": map[string]any{"outcome": "accepted"}}, nil
	case "reject":
		return map[string]any{"outcome": map[string]any{"outcome": "rejected"}}, nil
	default:
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
}
