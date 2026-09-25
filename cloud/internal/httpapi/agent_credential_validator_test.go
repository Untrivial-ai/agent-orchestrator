package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestCursorCredentialValidationTreatsPlanGatedFreePlanKeyAsValid(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		wantOK      bool
		wantInvalid bool
	}{
		{
			name:   "authenticated",
			status: http.StatusOK,
			body:   `{"data":{"email":"user@example.com"}}`,
			wantOK: true,
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			wantOK: true,
		},
		{
			name: "plan gated free plan key",
			status: http.StatusForbidden,
			body: `{"error":{"code":"plan_required",` +
				`"message":"Cloud Agent is not available for free users."}}`,
			wantOK: true,
		},
		{
			name:        "unknown key",
			status:      http.StatusUnauthorized,
			body:        `{"code":"error","message":"Invalid User API Key"}`,
			wantInvalid: true,
		},
		{
			name:        "unrelated forbidden response",
			status:      http.StatusForbidden,
			body:        `{"error":{"code":"account_suspended"}}`,
			wantInvalid: true,
		},
		{
			name:   "provider outage",
			status: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			validator := newAgentCredentialValidator(server.Client())
			validator.cursorBaseURL = server.URL

			err := validator.Validate(context.Background(), "cursor", "api_key", []byte("crsr_probe"))
			switch {
			case tc.wantOK && err != nil:
				t.Fatalf("Validate error = %v, want accepted", err)
			case tc.wantInvalid && err != errInvalidAgentCredential:
				t.Fatalf("Validate error = %v, want invalid credential", err)
			case !tc.wantOK && !tc.wantInvalid && (err == nil || err == errInvalidAgentCredential):
				t.Fatalf("Validate error = %v, want provider failure", err)
			}
		})
	}
}

func TestOpenAICredentialValidationStillRejectsForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"plan_required"}}`))
	}))
	defer server.Close()
	validator := newAgentCredentialValidator(server.Client())
	validator.openAIBaseURL = server.URL

	err := validator.Validate(context.Background(), "codex", "api_key", []byte("sk-probe"))
	if err != errInvalidAgentCredential {
		t.Fatalf("Validate error = %v, want invalid credential", err)
	}
}
