package ports

import (
	"fmt"
	"sort"
	"strings"
)

// ChatQuestion preserves provider question identity independently of form field names.
type ChatQuestion struct {
	ID       string
	Prompt   string
	Options  []ChatQuestionOption
	Multiple bool
	Custom   bool
	Secret   bool
}

// ChatInputAnswerText gives a message-delivered answer its question context.
func ChatInputAnswerText(request ChatInputRequest, content map[string]any) (string, error) {
	properties, _ := request.Schema["properties"].(map[string]any)
	keys := make([]string, 0, len(properties))
	for key := range properties {
		if !strings.HasSuffix(key, "_custom") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		value := content[key]
		if custom, ok := content[key+"_custom"].(string); ok && strings.TrimSpace(custom) != "" {
			value = custom
		}
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		property, _ := properties[key].(map[string]any)
		prompt, _ := property["title"].(string)
		if prompt == "" {
			prompt = key
		}
		answer := fmt.Sprint(value)
		switch values := value.(type) {
		case []string:
			answer = strings.Join(values, ", ")
		case []any:
			parts := make([]string, len(values))
			for i, v := range values {
				parts[i] = fmt.Sprint(v)
			}
			answer = strings.Join(parts, ", ")
		}
		if strings.TrimSpace(answer) == "" {
			continue
		}
		lines = append(lines, prompt+"\n"+answer)
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%w: answer at least one question", ErrChatDecisionNotOffered)
	}
	return strings.Join(lines, "\n\n"), nil
}

// ChatQuestionOption is one provider-offered answer and its display copy.
type ChatQuestionOption struct {
	Value       string
	Label       string
	Description string
}

// ChatQuestionForm uses the same per-question grouping as ACP question bridges.
func ChatQuestionForm(questions []ChatQuestion) (ChatInputRequest, error) {
	if len(questions) == 0 {
		return ChatInputRequest{}, fmt.Errorf("question request is empty")
	}
	properties := map[string]any{}
	seen := map[string]bool{}
	for i, q := range questions {
		if q.ID == "" || seen[q.ID] || strings.TrimSpace(q.Prompt) == "" {
			return ChatInputRequest{}, fmt.Errorf("invalid question identity or prompt")
		}
		seen[q.ID] = true
		key := fmt.Sprintf("question_%d", i)
		field := map[string]any{"type": "string", "title": q.Prompt}
		options := make([]any, 0, len(q.Options))
		values := map[string]bool{}
		for _, option := range q.Options {
			if values[option.Value] {
				return ChatInputRequest{}, fmt.Errorf("duplicate question option")
			}
			values[option.Value] = true
			options = append(options, map[string]any{"const": option.Value, "title": option.Label, "description": option.Description})
		}
		if q.Multiple {
			field["type"] = "array"
			field["items"] = map[string]any{"anyOf": options}
		} else if len(options) > 0 {
			field["oneOf"] = options
		}
		if q.Secret {
			field["format"] = "password"
		}
		properties[key] = field
		if q.Custom && len(options) > 0 {
			custom := map[string]any{"type": "string", "title": "Other"}
			if q.Secret {
				custom["format"] = "password"
			}
			properties[key+"_custom"] = custom
		}
	}
	return ChatInputRequest{Mode: ChatInputModeForm, Message: questions[0].Prompt, Schema: map[string]any{"type": "object", "properties": properties}}, nil
}

// ChatQuestionAnswers maps form values back onto provider question IDs. A custom
// answer takes precedence over the offered selection for the same question.
func ChatQuestionAnswers(questions []ChatQuestion, content map[string]any) map[string][]string {
	answers := map[string][]string{}
	for i, q := range questions {
		key := fmt.Sprintf("question_%d", i)
		if custom, ok := content[key+"_custom"].(string); ok && strings.TrimSpace(custom) != "" {
			answers[q.ID] = []string{custom}
			continue
		}
		switch value := content[key].(type) {
		case string:
			answers[q.ID] = []string{value}
		case []string:
			answers[q.ID] = value
		case []any:
			for _, item := range value {
				if text, ok := item.(string); ok {
					answers[q.ID] = append(answers[q.ID], text)
				}
			}
		}
	}
	return answers
}
