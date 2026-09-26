package agentcreds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnsupportedClaudeProvidersStayUnknownWithoutModels(t *testing.T) {
	for _, provider := range []Provider{ProviderBedrock, ProviderVertex, ProviderFoundry} {
		t.Run(string(provider), func(t *testing.T) {
			result := New(nil).ValidateResolvedLocal(context.Background(), provider, Credential{}, false, ResolveOptions{})
			if result.State != StateUnknown || result.Provider != provider || len(result.Models) != 0 {
				t.Fatalf("result = %+v, want an unverified provider with no discovered models", result)
			}
			if !strings.Contains(result.Detail, "not supported") {
				t.Fatalf("detail = %q, want explicit unsupported-validation guidance", result.Detail)
			}
		})
	}
}

func TestValidateLocalDowngradesResolverBugsToUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"x-api-key header is required"}}`))
	}))
	defer server.Close()

	validator := New(server.Client())
	result := validator.ValidateLocal(context.Background(), "gateway", ResolveOptions{Env: envFrom(map[string]string{
		"ANTHROPIC_API_KEY": "present-but-somehow-not-sent", "ANTHROPIC_BASE_URL": server.URL,
	})})
	if result.State != StateUnknown {
		t.Fatalf("state = %q, want unknown", result.State)
	}
}

func TestValidateLocalKeepsGenuineRejections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key is invalid."}}`))
	}))
	defer server.Close()

	validator := New(server.Client())
	result := validator.ValidateLocal(context.Background(), "gateway", ResolveOptions{Env: envFrom(map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-revoked", "ANTHROPIC_BASE_URL": server.URL,
	})})
	if result.State != StateInvalid {
		t.Fatalf("state = %q (%s), want invalid", result.State, result.Detail)
	}
	if result.Source != "ANTHROPIC_API_KEY" {
		t.Fatalf("source = %q, want the env var that supplied the credential", result.Source)
	}
}
