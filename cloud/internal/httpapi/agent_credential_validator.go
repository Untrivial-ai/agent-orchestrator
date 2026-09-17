package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAnthropicAPIURL = "https://api.anthropic.com"
	defaultOpenAIAPIURL    = "https://api.openai.com/v1"
	defaultCursorAPIURL    = "https://api.cursor.com"
)

type agentCredentialValidator struct {
	client           *http.Client
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

func (v *agentCredentialValidator) validateClaude(
	ctx context.Context,
	credentialType string,
	secret []byte,
) error {
	// #nosec G101 -- this checks a public credential-format prefix.
	if credentialType == "oauth_token" &&
		(!strings.HasPrefix(string(secret), "sk-ant-oat01-") || len(secret) < 80) {
		return errInvalidAgentCredential
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(v.anthropicBaseURL, "/")+"/v1/messages",
		bytes.NewReader([]byte(`{}`)),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("User-Agent", "claude-code/2.1.220")
	switch credentialType {
	case "api_key":
		request.Header.Set("x-api-key", string(secret))
	case "oauth_token":
		request.Header.Set("Authorization", "Bearer "+string(secret))
		request.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		request.Header.Set("x-app", "cli")
	default:
		return errInvalidAgentCredential
	}
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("validate Claude credential: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errInvalidAgentCredential
	case http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests:
		return nil
	default:
		return fmt.Errorf(
			"validate Claude credential: provider returned HTTP %d",
			response.StatusCode,
		)
	}
}

func (v *agentCredentialValidator) validateBearerEndpoint(
	ctx context.Context,
	provider, endpoint string,
	secret []byte,
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
	request.Header.Set("Authorization", "Bearer "+string(secret))
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
