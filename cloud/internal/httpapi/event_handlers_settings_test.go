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
		{"read-only mode", sendMessageRequest{Text: "hello", Mode: "read-only"}, true},
		{"standard mode", sendMessageRequest{Text: "hello", Mode: "standard"}, true},
		{"trusted mode", sendMessageRequest{Text: "hello", Mode: "trusted"}, true},
		{"approval selection", sendMessageRequest{Text: "hello", ApprovalMode: "accept-edits"}, true},
		{"invalid approval", sendMessageRequest{Text: "hello", ApprovalMode: "all"}, false},
		{"invalid flag-like model", sendMessageRequest{Text: "hello", Model: "--config"}, false},
		{"invalid effort", sendMessageRequest{Text: "hello", ReasoningEffort: "unlimited"}, false},
		{"invalid mode", sendMessageRequest{Text: "hello", Mode: "unrestricted"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateSendMessageRequest(test.input); (got == nil) != test.valid {
				t.Fatalf("validation error = %v, want valid %v", got, test.valid)
			}
		})
	}
}
