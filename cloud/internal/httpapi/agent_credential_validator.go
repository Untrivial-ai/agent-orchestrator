package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultGitHubAPIURL = "https://api.github.com"

type agentCredentialValidator struct {
	client        *http.Client
	githubBaseURL string
}

func newAgentCredentialValidator(client *http.Client) *agentCredentialValidator {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &agentCredentialValidator{
		client:        client,
		githubBaseURL: defaultGitHubAPIURL,
	}
}

func normalizeAgentCredentialSecret(value string) []byte {
	return []byte(strings.Join(strings.Fields(value), ""))
}

// Validate checks a credential before it is persisted. OpenCode credentials
// (the only coding-agent provider) are deliberately not probed at connect time:
// the sandbox worker resolves the key when a session runs, and a control-plane
// reachability probe would either reject throwaway dev keys or leak which
// provider a customer uses. The credential is stored and surfaced as "valid" so
// session creation proceeds; a bad key fails loudly in the worker instead.
//
// GitHub personal access tokens are the one exception: they back repository
// checkout rather than the coding agent, and a bad token is caught here so it
// never surfaces later as an opaque checkout failure inside a sandbox.
func (v *agentCredentialValidator) Validate(
	ctx context.Context,
	agent, credentialType string,
	secret []byte,
) error {
	switch agent {
	case "github":
		if credentialType != "personal_access_token" {
			return errInvalidAgentCredential
		}
		return v.validateBearerEndpoint(
			ctx,
			"GitHub",
			strings.TrimRight(v.githubBaseURL, "/")+"/user",
			secret,
		)
	default:
		if agent != "opencode" {
			return errInvalidAgentCredential
		}
		if credentialType != "api_key" {
			return errInvalidAgentCredential
		}
		return nil
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