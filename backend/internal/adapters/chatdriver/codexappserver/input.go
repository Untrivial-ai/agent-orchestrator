package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.ChatInputResponder = (*conversation)(nil)

func asyncQuestionInput(questions []codexproto.AsyncUserInputQuestion) (ports.ChatInputRequest, error) {
	neutral := make([]ports.ChatQuestion, 0, len(questions))
	for i, question := range questions {
		q := ports.ChatQuestion{ID: fmt.Sprint(i), Prompt: question.Title, Custom: true}
		for _, label := range question.Options {
			q.Options = append(q.Options, ports.ChatQuestionOption{Value: label, Label: label})
		}
		neutral = append(neutral, q)
	}
	request, err := ports.ChatQuestionForm(neutral)
	request.ResponseMode = "message"
	if err == nil {
		properties, _ := request.Schema["properties"].(map[string]any)
		for i, q := range questions {
			if len(q.Options) > 0 {
				property, _ := properties[fmt.Sprintf("question_%d", i)].(map[string]any)
				property["default"] = q.Options[0]
			}
		}
	}
	return request, err
}

func (c *conversation) handleInputRequest(ctx context.Context, req serverRequest) (any, error) {
	requestID := rawID(req.ID)
	if requestID == "" {
		return nil, fmt.Errorf("input request has no id")
	}
	var input ports.ChatInputRequest
	var questions []ports.ChatQuestion
	var turnID, itemID string
	if req.Method == codexproto.MethodItemToolRequestUserInput {
		var params codexproto.ToolRequestUserInputParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		if params.ThreadID != c.threadID {
			return nil, fmt.Errorf("input request belongs to another thread")
		}
		turnID, itemID = params.TurnID, params.ItemID
		for _, question := range params.Questions {
			q := ports.ChatQuestion{ID: question.ID, Prompt: question.Question, Custom: question.IsOther == nil || *question.IsOther, Secret: question.IsSecret != nil && *question.IsSecret}
			for _, option := range question.Options {
				q.Options = append(q.Options, ports.ChatQuestionOption{Value: option.Label, Label: option.Label, Description: option.Description})
			}
			questions = append(questions, q)
		}
		var err error
		input, err = ports.ChatQuestionForm(questions)
		if err != nil {
			return nil, err
		}
	} else {
		// Some versions include correlation fields outside the tagged MCP payload.
		var params struct {
			codexproto.McpServerElicitationRequestParams
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		if params.ThreadID != "" && params.ThreadID != c.threadID {
			return nil, fmt.Errorf("elicitation belongs to another thread")
		}
		turnID = params.TurnID
		input = ports.ChatInputRequest{Mode: ports.ChatInputMode(params.Mode), Message: deref(params.Message), URL: deref(params.URL), ElicitationID: deref(params.ElicitationID)}
		if input.Mode != ports.ChatInputModeForm && input.Mode != ports.ChatInputModeURL {
			return nil, fmt.Errorf("unsupported elicitation mode %s", input.Mode)
		}
		if input.Mode == ports.ChatInputModeForm {
			if err := json.Unmarshal(params.RequestedSchema, &input.Schema); err != nil {
				return nil, err
			}
		}
	}
	parked := &parkedRequest{ch: make(chan ports.ChatDecision, 1), method: req.Method, input: &input, questions: questions}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errConversationClosed
	}
	if turnID == "" {
		turnID = c.activeTurn
	}
	if _, exists := c.pending[requestID]; exists {
		c.mu.Unlock()
		return nil, fmt.Errorf("input request id %s is already pending", requestID)
	}
	c.pending[requestID] = parked
	c.mu.Unlock()
	c.emit(ports.ChatEvent{Kind: ports.ChatEventInputRequested, ProviderTurnID: turnID, ProviderItemID: itemID, RequestID: requestID, Input: &input})
	select {
	case reply, ok := <-parked.ch:
		if !ok {
			return nil, errConversationClosed
		}
		return json.RawMessage(reply.Raw), nil
	case <-ctx.Done():
		c.discardPending(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventInputResolved, RequestID: requestID})
		return nil, ctx.Err()
	case <-time.After(approvalWait):
		c.discardPending(requestID)
		c.emit(ports.ChatEvent{Kind: ports.ChatEventInputResolved, RequestID: requestID})
		if req.Method == codexproto.MethodItemToolRequestUserInput {
			return map[string]any{"answers": map[string]any{}}, nil
		}
		return map[string]any{"action": "cancel"}, nil
	}
}

func (c *conversation) ResolveInput(ctx context.Context, requestID string, response ports.ChatInputResponse) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	requestID = c.nativeID(requestID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errConversationClosed
	}
	parked, ok := c.pending[requestID]
	if !ok || parked.input == nil {
		return ports.ErrChatRequestNotPending
	}
	if err := ports.ValidateChatInputResponse(*parked.input, response); err != nil {
		return err
	}
	var reply any
	if parked.method == codexproto.MethodItemToolRequestUserInput {
		answers := map[string]any{}
		if response.Action == ports.ChatInputActionAccept {
			for id, values := range ports.ChatQuestionAnswers(parked.questions, response.Content) {
				answers[id] = map[string]any{"answers": values}
			}
		}
		reply = map[string]any{"answers": answers}
	} else {
		reply = map[string]any{"action": response.Action}
		if response.Action == ports.ChatInputActionAccept && parked.input.Mode == ports.ChatInputModeForm {
			reply = map[string]any{"action": response.Action, "content": response.Content}
		}
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	parked.ch <- ports.ChatDecision{Raw: raw}
	delete(c.pending, requestID)
	return nil
}
