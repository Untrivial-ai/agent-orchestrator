package httpapi

import (
	"context"
	"testing"
)

func TestOpenCodeCredentialValidation(t *testing.T) {
	validator := newAgentCredentialValidator(nil)
	if err := validator.Validate(context.Background(), "opencode", "api_key", []byte("sk-abc")); err != nil {
		t.Fatalf("validate opencode api_key: %v", err)
	}
	if err := validator.Validate(context.Background(), "opencode", "oauth_token", []byte("token")); err != errInvalidAgentCredential {
		t.Errorf("opencode non-api_key error = %v, want invalid credential", err)
	}
	if err := validator.Validate(context.Background(), "claude-code", "api_key", []byte("sk-abc")); err != errInvalidAgentCredential {
		t.Errorf("stripped harness error = %v, want invalid credential", err)
	}
}