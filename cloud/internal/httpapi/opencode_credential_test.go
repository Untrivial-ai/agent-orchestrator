package httpapi

import (
	"context"
	"errors"
	"testing"
)

func TestValidAgentProviderIncludesOpenCode(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex", "cursor", "opencode"} {
		if !validAgentProvider(agent) {
			t.Errorf("validAgentProvider(%q) = false, want true", agent)
		}
	}
	if validAgentProvider("nope") {
		t.Error("validAgentProvider(nope) = true, want false")
	}
}

func TestValidAgentCredentialTypeOpenCode(t *testing.T) {
	// opencode's cloud credential is a per-provider API key (its interactive login
	// lives in a local sqlite db, not a portable file), injected as an env var.
	for _, ct := range []string{"opencode_api_key", "anthropic_api_key", "openai_api_key", "openrouter_api_key"} {
		if !validAgentCredentialType("opencode", ct) {
			t.Errorf("opencode should accept %q", ct)
		}
	}
	for _, ct := range []string{"api_key", "auth_json", "oauth_token", ""} {
		if validAgentCredentialType("opencode", ct) {
			t.Errorf("opencode should reject credential type %q", ct)
		}
	}
}

func TestValidateOpenCodeProviderKey(t *testing.T) {
	v := newAgentCredentialValidator(nil)
	ctx := context.Background()

	// A non-empty provider key is accepted without a network probe (multi-provider).
	if err := v.Validate(ctx, "opencode", "anthropic_api_key", []byte("sk-ant-xxx")); err != nil {
		t.Fatalf("valid opencode provider key rejected: %v", err)
	}
	// Empty / unsupported type is rejected.
	if err := v.Validate(ctx, "opencode", "anthropic_api_key", []byte("   ")); !errors.Is(err, errInvalidAgentCredential) {
		t.Errorf("blank opencode key: err = %v, want errInvalidAgentCredential", err)
	}
	if err := v.Validate(ctx, "opencode", "api_key", []byte("x")); !errors.Is(err, errInvalidAgentCredential) {
		t.Errorf("bare api_key should be rejected for opencode; err = %v", err)
	}
}
