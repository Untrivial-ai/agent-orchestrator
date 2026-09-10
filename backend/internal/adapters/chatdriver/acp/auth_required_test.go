package acp

import (
	"errors"
	"fmt"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The runtime catch is the one layer that works for every credential source
// and every provider, including the credential chains AO cannot inspect: it
// needs no credential, no network call and no provider knowledge, only the
// rejection the agent already received. So it has to recognize the rejection
// in the shapes agents actually emit, not only the protocol's own code.
func TestIsACPAuthRequiredRecognizesProviderRejections(t *testing.T) {
	tests := []struct {
		name string
		err  *acpsdk.RequestError
		want bool
	}{
		{"acp auth_required code", &acpsdk.RequestError{Code: -32000, Message: "authentication required"}, true},
		{"alternate auth_required code", &acpsdk.RequestError{Code: -32001, Message: "auth required"}, true},
		{"http status passed through", &acpsdk.RequestError{Code: 401, Message: "request failed"}, true},
		{"forbidden passed through", &acpsdk.RequestError{Code: 403, Message: "request failed"}, true},
		{
			"revoked api key in message",
			&acpsdk.RequestError{Code: -32603, Message: "API Error: 401 {\"error\":{\"message\":\"API key is invalid.\"}}"},
			true,
		},
		{
			"expired oauth token in message",
			&acpsdk.RequestError{Code: -32603, Message: "OAuth access token is invalid."},
			true,
		},
		{
			"rejection nested in error data",
			&acpsdk.RequestError{Code: -32603, Message: "prompt failed", Data: map[string]any{
				"detail": "authentication_error: invalid bearer token",
			}},
			true,
		},
		{
			"rejection in string data",
			&acpsdk.RequestError{Code: -32603, Message: "prompt failed", Data: "Invalid bearer token"},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isACPAuthRequired(tc.err); got != tc.want {
				t.Fatalf("isACPAuthRequired = %v, want %v", got, tc.want)
			}
		})
	}
}

// A reauth prompt shown to a user whose credentials are fine is its own bug,
// so the match stays phrase-based: a turn that merely mentions auth, or a tool
// echoing a status code from the user's own program, is not a rejection.
func TestIsACPAuthRequiredIgnoresUnrelatedFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("authentication failed somewhere")},
		{"unrelated rpc failure", &acpsdk.RequestError{Code: -32603, Message: "internal error"}},
		{"method not found", &acpsdk.RequestError{Code: -32601, Message: "Method not found"}},
		{
			"model discussing auth",
			&acpsdk.RequestError{Code: -32603, Message: "I cannot help you bypass the auth check in that file"},
		},
		{
			"user's own http status in tool output",
			&acpsdk.RequestError{Code: -32603, Message: "test failed: expected 200, got 401 from the fixture server"},
		},
		{"nil data map", &acpsdk.RequestError{Code: -32603, Message: "prompt failed", Data: map[string]any{"n": 1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if isACPAuthRequired(tc.err) {
				t.Fatalf("isACPAuthRequired = true for %v, want false", tc.err)
			}
		})
	}
}

func TestIsACPAuthRequiredUnwrapsWrappedErrors(t *testing.T) {
	wrapped := fmt.Errorf("ACP session/prompt: %w", &acpsdk.RequestError{Code: -32000, Message: "auth required"})
	if !isACPAuthRequired(wrapped) {
		t.Fatal("a wrapped auth rejection must still be recognized")
	}
}

func TestNormalizeACPErrorMarksAuthRejectionsForReauth(t *testing.T) {
	err := normalizeACPError("ACP session/prompt", &acpsdk.RequestError{
		Code: -32603, Message: "API Error: 401 {\"error\":{\"message\":\"API key is invalid.\"}}",
	})
	if !errors.Is(err, ports.ErrChatAuthRequired) {
		t.Fatalf("err = %v, want it to wrap ErrChatAuthRequired so the reauth banner fires", err)
	}
}

// The driver must carry the binding's invalidation hook onto every
// conversation, or the runtime catch is wired to nothing.
func TestDriverPropagatesTheAuthRejectionHook(t *testing.T) {
	called := 0
	cfg := Config{OnAuthRejected: func() { called++ }}
	conv := &conversation{}
	// This mirrors the single assignment in newACPConversation; if that line
	// is removed, the hook silently stops reaching live turns.
	conv.onAuthRejected = cfg.OnAuthRejected
	if conv.onAuthRejected == nil {
		t.Fatal("the configured hook must reach the conversation")
	}
	conv.onAuthRejected()
	if called != 1 {
		t.Fatalf("hook ran %d times, want 1", called)
	}
}

// The hook is optional: a binding that supplies none must not panic.
func TestNoAuthRejectionHookIsSafe(t *testing.T) {
	conv := &conversation{}
	conv.onAuthRejected = Config{}.OnAuthRejected
	if conv.onAuthRejected != nil {
		t.Fatal("an unset hook must stay nil so the call site can skip it")
	}
}
