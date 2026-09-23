package httpapi

import (
	"context"
	"testing"
)

func TestCodexAuthJSONValidationIsOpaqueButRequiresDocument(t *testing.T) {
	validator := newAgentCredentialValidator(nil)
	if err := validator.Validate(context.Background(), "codex", "auth_json", []byte(`{"tokens":{"access_token":"opaque"}}`)); err != nil {
		t.Fatalf("validate native Codex auth document: %v", err)
	}
	for _, secret := range []string{"", "not-json", "null", "[]"} {
		if err := validator.Validate(context.Background(), "codex", "auth_json", []byte(secret)); err != errInvalidAgentCredential {
			t.Errorf("Validate(%q) error = %v, want invalid credential", secret, err)
		}
	}
}
