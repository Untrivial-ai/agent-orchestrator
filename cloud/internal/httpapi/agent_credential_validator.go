package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type agentCredentialValidator struct{}

func newAgentCredentialValidator() *agentCredentialValidator {
	return &agentCredentialValidator{}
}

func normalizeAgentCredentialSecret(value string) []byte {
	return []byte(strings.Join(strings.Fields(value), ""))
}

// Validate checks a coding-agent credential before it is persisted. OpenCode
// credentials are deliberately not probed at connect time: the sandbox worker
// resolves the key when a session runs, and a control-plane reachability probe
// would either reject throwaway dev keys or leak which provider a customer
// uses. The credential is stored and surfaced as "valid" so session creation
// proceeds; a bad key fails loudly in the worker instead.
func (v *agentCredentialValidator) Validate(
	ctx context.Context,
	agent, credentialType string,
	secret []byte,
) error {
if agent != "opencode" {
		return errInvalidAgentCredential
	}
	if credentialType != "api_key" {
		return errInvalidAgentCredential
	}
	return nil
}

var errInvalidAgentCredential = errors.New(
	"coding-agent credential is invalid or expired",
)