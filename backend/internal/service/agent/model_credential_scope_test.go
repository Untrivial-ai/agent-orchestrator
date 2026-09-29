package agent

import (
	"context"
	"testing"
)

func TestCredentialTypeFromScope(t *testing.T) {
	for _, tc := range []struct {
		scope string
		want  string
		ok    bool
	}{
		{scope: "@cred:anthropic_api_key", want: "anthropic_api_key", ok: true},
		{scope: "@cred:openrouter_api_key", want: "openrouter_api_key", ok: true},
		{scope: "", ok: false},
		{scope: "@cred:", ok: false},
		{scope: "my-project", ok: false},
		{scope: "agent-orchestrator", ok: false},
	} {
		got, ok := credentialTypeFromScope(tc.scope)
		if ok != tc.ok || got != tc.want {
			t.Errorf("credentialTypeFromScope(%q) = (%q,%v), want (%q,%v)", tc.scope, got, ok, tc.want, tc.ok)
		}
	}
}

func TestModelCatalogScopePreservesCredentialScope(t *testing.T) {
	// A credential scope has no backing project, so it must survive verbatim
	// (never collapse to the device-global "") even with no project lookup wired.
	s := &Service{}
	got, err := s.modelCatalogScope(context.Background(), "@cred:anthropic_api_key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "@cred:anthropic_api_key" {
		t.Fatalf("scope = %q, want the credential scope preserved", got)
	}
}

func TestModelDiscoveryRequestSetsCredentialTypeWithoutProjectLookup(t *testing.T) {
	// s.projects is nil: a credential scope must not attempt a project lookup and
	// must carry the credential type onto the request for the adapter to honor.
	s := &Service{}
	request, err := s.modelDiscoveryRequest(context.Background(), "opencode", "@cred:openai_api_key", "/bin/opencode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if request.CredentialType != "openai_api_key" {
		t.Fatalf("CredentialType = %q, want openai_api_key", request.CredentialType)
	}
	if request.WorkingDir != "" {
		t.Fatalf("WorkingDir = %q, want empty for a credential scope", request.WorkingDir)
	}
}
