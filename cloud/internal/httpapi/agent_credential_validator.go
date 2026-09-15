package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

const (
	defaultAnthropicAPIURL = "https://api.anthropic.com"
	defaultOpenAIAPIURL    = "https://api.openai.com/v1"
	defaultCursorAPIURL    = "https://api.cursor.com"
	anthropicAPIVersion    = "2023-06-01"
)

type agentCredentialValidator struct {
	client           *http.Client
	creds            *agentcreds.Validator
	anthropicBaseURL string
	openAIBaseURL    string
	cursorBaseURL    string
}

func newAgentCredentialValidator(client *http.Client) *agentCredentialValidator {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &agentCredentialValidator{
		client:           client,
		creds:            agentcreds.New(agentcreds.WithHTTPClient(client)),
		anthropicBaseURL: defaultAnthropicAPIURL,
		openAIBaseURL:    defaultOpenAIAPIURL,
		cursorBaseURL:    defaultCursorAPIURL,
	}
}

func normalizeAgentCredentialSecret(value string) []byte {
	return []byte(strings.Join(strings.Fields(value), ""))
}

func (v *agentCredentialValidator) Validate(
	ctx context.Context,
	agent, credentialType string,
	secret []byte,
) error {
	switch agent {
	case "claude-code":
		return v.validateClaude(ctx, credentialType, secret)
	case "codex":
		if credentialType != "api_key" && credentialType != "access_token" {
			return errInvalidAgentCredential
		}
		return v.validateBearerEndpoint(
			ctx,
			"OpenAI",
			strings.TrimRight(v.openAIBaseURL, "/")+"/models",
			secret,
		)
	case "cursor":
		if credentialType != "api_key" {
			return errInvalidAgentCredential
		}
		return v.validateBearerEndpoint(
			ctx,
			"Cursor",
			strings.TrimRight(v.cursorBaseURL, "/")+"/v1/me",
			secret,
		)
	default:
		return errInvalidAgentCredential
	}
}

// validateClaude delegates to the shared agentcreds package, which owns the
// header-per-credential-kind rule and the three-state classification for every
// Anthropic surface — first-party, gateway, Bedrock, Vertex, and Foundry.
//
// Cloud and the desktop daemon resolve credentials in opposite directions:
// Cloud is handed a secret and must never let it touch a manifest, a log, or
// an argv, while the daemon has to discover which of several local sources
// wins. Only the probe is common, so only the probe is shared. Resolution
// stays on each side of that line where it belongs.
func (v *agentCredentialValidator) validateClaude(
	ctx context.Context,
	credentialType string,
	secret []byte,
) error {
	var kind agentcreds.Kind
	switch credentialType {
	case "api_key":
		kind = agentcreds.KindAPIKey
	case "oauth_token":
		// Covers both `claude setup-token` output and Pro/Max login tokens:
		// they are the same sk-ant-oat01- credential class.
		kind = agentcreds.KindOAuthToken
	default:
		return errInvalidAgentCredential
	}
	result := v.creds.Validate(ctx, agentcreds.Credential{
		Kind: kind, Secret: string(secret), Provider: agentcreds.ProviderFirstParty,
		BaseURL: v.anthropicBaseURL,
	})
	switch result.State {
	case agentcreds.StateInvalid:
		return errInvalidAgentCredential
	case agentcreds.StateValid:
		return nil
	default:
		// Unknown is not a rejection. Surface it as a transport-style error so
		// the caller can retry rather than telling the user their credential
		// is bad.
		if result.Err != nil {
			return fmt.Errorf("validate Claude credential: %w", result.Err)
		}
		return fmt.Errorf("validate Claude credential: %s", result.Detail)
	}
}

func (v *agentCredentialValidator) validateBearerEndpoint(
	ctx context.Context,
	provider, endpoint string,
	secret []byte,
) error {
	return v.validateAuthedEndpoint(ctx, provider, endpoint, map[string]string{
		"Authorization": "Bearer " + string(secret),
	})
}

func (v *agentCredentialValidator) validateAuthedEndpoint(
	ctx context.Context,
	provider, endpoint string,
	headers map[string]string,
) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint,
		http.NoBody,
	)
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("validate %s credential: %w", provider, err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errInvalidAgentCredential
	case http.StatusOK, http.StatusTooManyRequests:
		return nil
	default:
		return fmt.Errorf(
			"validate %s credential: provider returned HTTP %d",
			provider,
			response.StatusCode,
		)
	}
}

var errInvalidAgentCredential = errors.New(
	"coding-agent credential is invalid or expired",
)
