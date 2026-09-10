package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestValidator(t *testing.T, handler http.HandlerFunc) *agentCredentialValidator {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	validator := newAgentCredentialValidator(server.Client())
	validator.anthropicBaseURL = server.URL
	validator.openAIBaseURL = server.URL
	validator.cursorBaseURL = server.URL
	return validator
}

// A 400 must NOT count as success. The previous implementation POSTed an empty
// body to /v1/messages and treated 400 as valid, which fails open if the
// provider ever validates the body before the credential.
func TestValidateClaudeStatusMapping(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		status   int
		wantErr  error
		wantFail bool
	}{
		{name: "ok", status: http.StatusOK},
		{name: "rate limited", status: http.StatusTooManyRequests},
		{name: "unauthorized", status: http.StatusUnauthorized, wantErr: errInvalidAgentCredential},
		{name: "forbidden", status: http.StatusForbidden, wantErr: errInvalidAgentCredential},
		{name: "bad request is not a pass", status: http.StatusBadRequest, wantFail: true},
		{name: "server error", status: http.StatusInternalServerError, wantFail: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			validator := newTestValidator(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(testCase.status)
			})
			err := validator.Validate(context.Background(), "claude-code", "oauth_token", []byte("sk-ant-oat01-token"))
			switch {
			case testCase.wantErr != nil:
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("got %v, want %v", err, testCase.wantErr)
				}
			case testCase.wantFail:
				if err == nil {
					t.Fatal("want an error")
				}
				if errors.Is(err, errInvalidAgentCredential) {
					t.Fatal("must not report the credential as invalid")
				}
			default:
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
			}
		})
	}
}

func TestValidateClaudeSendsCorrectRequest(t *testing.T) {
	for _, testCase := range []struct {
		credentialType string
		wantHeader     string
		wantValue      string
		absentHeader   string
	}{
		{"api_key", "X-Api-Key", "secret-value", "Authorization"},
		{"oauth_token", "Authorization", "Bearer secret-value", "X-Api-Key"},
	} {
		t.Run(testCase.credentialType, func(t *testing.T) {
			validator := newTestValidator(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("method = %s, want GET", r.Method)
				}
				if r.URL.Path != "/v1/models" {
					t.Errorf("path = %s, want /v1/models", r.URL.Path)
				}
				if got := r.Header.Get(testCase.wantHeader); got != testCase.wantValue {
					t.Errorf("%s = %q, want %q", testCase.wantHeader, got, testCase.wantValue)
				}
				if got := r.Header.Get(testCase.absentHeader); got != "" {
					t.Errorf("%s should be absent, got %q", testCase.absentHeader, got)
				}
				if got := r.Header.Get("anthropic-version"); got != anthropicAPIVersion {
					t.Errorf("anthropic-version = %q", got)
				}
				w.WriteHeader(http.StatusOK)
			})
			if err := validator.Validate(context.Background(), "claude-code", testCase.credentialType, []byte("secret-value")); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// `claude setup-token` mints an sk-ant-oat01- token. It must validate without
// the old hardcoded prefix/length precheck rejecting it locally.
func TestValidateClaudeAcceptsSetupToken(t *testing.T) {
	setupToken := "sk-ant-oat01-" + strings.Repeat("aB3", 30)
	validator := newTestValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := validator.Validate(context.Background(), "claude-code", "oauth_token", []byte(setupToken)); err != nil {
		t.Fatalf("setup token rejected: %v", err)
	}
}

func TestValidateRejectsUnknownTypeWithoutCallingProvider(t *testing.T) {
	called := false
	validator := newTestValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	err := validator.Validate(context.Background(), "claude-code", "bogus_type", []byte("x"))
	if !errors.Is(err, errInvalidAgentCredential) {
		t.Fatalf("got %v, want errInvalidAgentCredential", err)
	}
	if called {
		t.Fatal("must not reach the provider")
	}
}

// A transport failure must not be reported as a bad credential: the handler
// maps errInvalidAgentCredential to 422 and everything else to 502.
func TestValidateClaudeTransportFailureIsNotInvalidCredential(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	client := server.Client()
	url := server.URL
	server.Close()

	validator := newAgentCredentialValidator(client)
	validator.anthropicBaseURL = url

	err := validator.Validate(context.Background(), "claude-code", "api_key", []byte("k"))
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, errInvalidAgentCredential) {
		t.Fatal("transport failure must not report the credential as invalid")
	}
}
