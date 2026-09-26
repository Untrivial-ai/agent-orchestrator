package httpapi

import (
	"context"
	"encoding/json"
	"strings"
)

// agentCredentialSpec encapsulates one cloud harness's control-plane credential
// business logic: which credential types it accepts, which are opaque documents
// that must be stored verbatim, and how each is validated before it is encrypted
// and stored. Adding a harness is a matter of implementing this interface and
// registering it below -- the handlers and validator hold no per-harness
// switches. (GitHub PAT validation lives outside this registry: it is not a
// coding-agent harness.)
type agentCredentialSpec interface {
	credentialTypes() []string
	// preserveRawSecret reports credential types whose bytes must be stored
	// verbatim (opaque JSON documents), skipping token normalization.
	preserveRawSecret(credentialType string) bool
	validate(ctx context.Context, credentialType string, secret []byte, v *agentCredentialValidator) error
}

// agentCredentialSpecs is the single registry of supported cloud harnesses.
var agentCredentialSpecs = map[string]agentCredentialSpec{
	"claude-code": claudeSpec{},
	"codex":       codexSpec{},
	"cursor":      cursorSpec{},
	"opencode":    opencodeSpec{},
}

func agentCredentialSpecFor(agent string) (agentCredentialSpec, bool) {
	s, ok := agentCredentialSpecs[agent]
	return s, ok
}

func specAcceptsCredentialType(s agentCredentialSpec, credentialType string) bool {
	for _, t := range s.credentialTypes() {
		if t == credentialType {
			return true
		}
	}
	return false
}

type claudeSpec struct{}

func (claudeSpec) credentialTypes() []string          { return []string{"api_key", "oauth_token"} }
func (claudeSpec) preserveRawSecret(string) bool      { return false }
func (claudeSpec) validate(ctx context.Context, credentialType string, secret []byte, v *agentCredentialValidator) error {
	return v.validateClaude(ctx, credentialType, secret)
}

type codexSpec struct{}

func (codexSpec) credentialTypes() []string { return []string{"api_key", "access_token", "auth_json"} }
func (codexSpec) preserveRawSecret(credentialType string) bool {
	return credentialType == "auth_json"
}
func (codexSpec) validate(ctx context.Context, credentialType string, secret []byte, v *agentCredentialValidator) error {
	if credentialType == "auth_json" {
		// Codex owns this refreshable document; a non-empty JSON object is the only
		// safe store-time check (its fields are not AO's to inspect).
		return validateAuthDocument(secret)
	}
	if credentialType != "api_key" && credentialType != "access_token" {
		return errInvalidAgentCredential
	}
	return v.validateBearerEndpoint(ctx, "OpenAI", strings.TrimRight(v.openAIBaseURL, "/")+"/models", secret)
}

type cursorSpec struct{}

func (cursorSpec) credentialTypes() []string     { return []string{"api_key"} }
func (cursorSpec) preserveRawSecret(string) bool { return false }
func (cursorSpec) validate(ctx context.Context, credentialType string, secret []byte, v *agentCredentialValidator) error {
	if credentialType != "api_key" {
		return errInvalidAgentCredential
	}
	return v.validateBearerEndpoint(ctx, "Cursor", strings.TrimRight(v.cursorBaseURL, "/")+"/v1/me", secret)
}

type opencodeSpec struct{}

// opencode stores interactive logins in a local sqlite database (not a portable
// file), so its cloud-portable credential is a provider API key. It is
// multi-provider, so it exposes one credential type per provider; the worker
// injects each as the matching env var (see opencodeProviderEnv).
func (opencodeSpec) credentialTypes() []string {
	return []string{"opencode_api_key", "anthropic_api_key", "openai_api_key", "openrouter_api_key"}
}
func (opencodeSpec) preserveRawSecret(string) bool { return false }
func (opencodeSpec) validate(_ context.Context, credentialType string, secret []byte, _ *agentCredentialValidator) error {
	// No single validation endpoint spans opencode's providers, so a well-formed
	// (accepted type, non-empty) key is accepted; the worker runtime is the arbiter.
	if !specAcceptsCredentialType(opencodeSpec{}, credentialType) || len(strings.TrimSpace(string(secret))) == 0 {
		return errInvalidAgentCredential
	}
	return nil
}

// validateAuthDocument accepts a non-empty JSON object -- the only safe store-time
// check for an opaque, refreshable auth document.
func validateAuthDocument(secret []byte) error {
	var document map[string]json.RawMessage
	if json.Unmarshal(secret, &document) != nil || document == nil {
		return errInvalidAgentCredential
	}
	return nil
}
