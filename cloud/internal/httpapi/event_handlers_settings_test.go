package httpapi

import "testing"

func TestValidateSendMessageRequestSettings(t *testing.T) {
	tests := []struct {
		name  string
		input sendMessageRequest
		valid bool
	}{
		{"valid codex selection", sendMessageRequest{Text: "hello", Model: "gpt-5.6-codex", ReasoningEffort: "high"}, true},
		{"maximum effort", sendMessageRequest{Text: "hello", ReasoningEffort: "max"}, true},
		{"ultra effort", sendMessageRequest{Text: "hello", ReasoningEffort: "ultra"}, true},
		{"invalid flag-like model", sendMessageRequest{Text: "hello", Model: "--config"}, false},
		{"invalid effort", sendMessageRequest{Text: "hello", ReasoningEffort: "unlimited"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateSendMessageRequest(test.input); (got == nil) != test.valid {
				t.Fatalf("validation error = %v, want valid %v", got, test.valid)
			}
		})
	}
}
