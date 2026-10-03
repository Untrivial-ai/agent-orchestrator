package ports

import (
	"errors"
	"testing"
)

func TestQuestionAnswersPreserveIdentityAndCustomChoice(t *testing.T) {
	questions := []ChatQuestion{{ID: "choice", Prompt: "Choose", Options: []ChatQuestionOption{{Value: "a", Label: "A"}}, Custom: true}, {ID: "many", Prompt: "Select", Multiple: true, Options: []ChatQuestionOption{{Value: "x"}, {Value: "y"}}}, {ID: "secret", Prompt: "Password", Secret: true}}
	request, err := ChatQuestionForm(questions)
	if err != nil {
		t.Fatal(err)
	}
	content := map[string]any{"question_0": "a", "question_0_custom": "my own", "question_1": []any{"x", "y"}, "question_2": "secret"}
	if err := ValidateChatInputResponse(request, ChatInputResponse{Action: ChatInputActionAccept, Content: content}); err != nil {
		t.Fatal(err)
	}
	answers := ChatQuestionAnswers(questions, content)
	if answers["choice"][0] != "my own" || len(answers["many"]) != 2 || answers["secret"][0] != "secret" {
		t.Fatalf("answers: %+v", answers)
	}
	properties := request.Schema["properties"].(map[string]any)
	if properties["question_2"].(map[string]any)["format"] != "password" {
		t.Fatal("secret lost")
	}
	text, err := ChatInputAnswerText(request, content)
	if err != nil {
		t.Fatal(err)
	}
	if text != "Choose\nmy own\n\nSelect\nx, y\n\nPassword\nsecret" {
		t.Fatalf("answer text %q", text)
	}
}

func TestInputValidationDoesNotAcceptInventedOrOutOfRangeAnswers(t *testing.T) {
	request := ChatInputRequest{Mode: ChatInputModeForm, Schema: map[string]any{"required": []any{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer", "minimum": 1, "maximum": 3}, "name": map[string]any{"type": "string", "minLength": 2, "maxLength": 4}, "choice": map[string]any{"type": "string", "enum": []any{"yes", "no"}}}}}
	for name, content := range map[string]map[string]any{"missing": {}, "fraction": {"count": 1.5}, "low": {"count": 0}, "high": {"count": 4}, "short": {"count": 1, "name": "x"}, "long": {"count": 1, "name": "12345"}, "invented": {"count": 1, "choice": "maybe"}, "unknown": {"count": 1, "extra": "x"}} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateChatInputResponse(request, ChatInputResponse{Action: ChatInputActionAccept, Content: content}); !errors.Is(err, ErrChatDecisionNotOffered) {
				t.Fatalf("validation %v", err)
			}
		})
	}
	for _, action := range []ChatInputAction{ChatInputActionDecline, ChatInputActionCancel} {
		if err := ValidateChatInputResponse(request, ChatInputResponse{Action: action}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEmptyOrDuplicateQuestionsAreRejected(t *testing.T) {
	for _, questions := range [][]ChatQuestion{nil, {{ID: "x", Prompt: ""}}, {{ID: "x", Prompt: "One"}, {ID: "x", Prompt: "Two"}}} {
		if _, err := ChatQuestionForm(questions); err == nil {
			t.Fatal("malformed question accepted")
		}
	}
	request, _ := ChatQuestionForm([]ChatQuestion{{ID: "x", Prompt: "How?"}})
	if _, err := ChatInputAnswerText(request, map[string]any{}); !errors.Is(err, ErrChatDecisionNotOffered) {
		t.Fatalf("empty answer: %v", err)
	}
}
