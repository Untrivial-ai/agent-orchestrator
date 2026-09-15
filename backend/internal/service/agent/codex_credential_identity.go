package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// codexCredentialIdentity contains only locally parsed matching material. The
// API key is kept in memory for direct comparison and is never persisted,
// logged, or exposed through the API.
type codexCredentialIdentity struct {
	Method            domain.CodexAuthMethod
	ProviderAccountID string
	APIKey            string
}

func parseCodexCredentialIdentity(data []byte) (codexCredentialIdentity, error) {
	var document struct {
		OpenAIAPIKey *string `json:"OPENAI_API_KEY"`
		Tokens       *struct {
			AccountID    string `json:"account_id"`
			AccessToken  string `json:"access_token"`
			IDToken      string `json:"id_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return codexCredentialIdentity{}, errors.New("codex credential is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return codexCredentialIdentity{}, errors.New("codex credential contains trailing data")
	}
	if document.OpenAIAPIKey != nil && strings.TrimSpace(*document.OpenAIAPIKey) != "" {
		return codexCredentialIdentity{Method: domain.CodexAuthMethodAPIKey, APIKey: *document.OpenAIAPIKey}, nil
	}
	if document.Tokens == nil || (strings.TrimSpace(document.Tokens.AccessToken) == "" && strings.TrimSpace(document.Tokens.IDToken) == "" && strings.TrimSpace(document.Tokens.RefreshToken) == "") {
		return codexCredentialIdentity{}, errors.New("codex credential does not contain a supported login")
	}
	accountID := strings.TrimSpace(document.Tokens.AccountID)
	if accountID != "" && !safeProviderAccountID(accountID) {
		return codexCredentialIdentity{}, errors.New("codex credential contains an invalid account identity")
	}
	return codexCredentialIdentity{Method: domain.CodexAuthMethodChatGPT, ProviderAccountID: accountID}, nil
}

func inspectCodexCredentialIdentity(data []byte) (codexCredentialIdentity, bool) {
	identity, err := parseCodexCredentialIdentity(data)
	return identity, err == nil
}

func safeProviderAccountID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}
